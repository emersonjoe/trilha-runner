package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/emersonjoe/trilha-runner/deployer"
	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-runner/queue"
	"github.com/emersonjoe/trilha-runner/runner"
	"github.com/emersonjoe/trilha-runner/syncer"
)

type stringList []string

func (values *stringList) String() string { return fmt.Sprint([]string(*values)) }
func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

type workerOptions struct {
	Once          bool
	Every         time.Duration
	Driver        string
	Command       string
	Attempts      int
	RetryBase     time.Duration
	WorkspaceRoot string
	Repository    string
	DefaultBranch string
	Push          bool
	Name          string
	Capacity      int
}

type workerOutcome struct {
	item   queue.Item
	result *runner.Result
	report queue.Result
}

func runRemoteWorker(ctx context.Context, out io.Writer, remote queue.Remote, base *runner.Runner, delivery deployer.Config, deliveryEnabled bool, options workerOptions) error {
	capacity := options.Capacity
	if capacity <= 0 {
		capacity = 1
	}
	if options.Once {
		capacity = 1
	}
	remote.Capacity = capacity
	remote.Running = 0
	if err := remote.Heartbeat(ctx, "idle"); err != nil {
		return err
	}
	outcomes := make(chan workerOutcome, capacity)
	var materialize sync.Mutex
	running := 0
	for {
		claimed := false
		for running < capacity {
			claim := remote
			claim.Running = running
			item, err := claim.Next(ctx)
			if errors.Is(err, queue.ErrEmpty) {
				break
			}
			if err != nil {
				if ctx.Err() != nil {
					// The worker was told to stop while claiming: a shutdown, not a failure.
					return nil
				}
				return err
			}
			claimed = true
			running++
			remote.Running = running
			_ = remote.Heartbeat(ctx, fmt.Sprintf("running %d/%d", running, capacity))
			workRemote := remote
			go func(item queue.Item, workRemote queue.Remote) {
				outcomes <- executeWorkerItem(ctx, workRemote, base, item, options, &materialize)
			}(item, workRemote)
			if options.Once {
				break
			}
		}

		if running == 0 && !claimed {
			if deliveryEnabled {
				work, err := remote.NextDeployment(ctx)
				if err == nil {
					remote.Running = 1
					_ = remote.Heartbeat(ctx, "deploying "+work.Deployment.Environment)
					result := delivery.Execute(ctx, work)
					if err := remote.DoneDeployment(ctx, work, queue.DeploymentResult{Passed: result.Passed, Log: result.Log, Health: result.Health, Error: result.Error}); err != nil {
						return err
					}
					remote.Running = 0
					_ = remote.Heartbeat(ctx, "idle")
					fmt.Fprintf(out, "%s → %s (%s)\n", work.Deployment.ID, map[bool]string{true: "succeeded", false: "failed"}[result.Passed], work.Deployment.Environment)
					if options.Once {
						return nil
					}
					continue
				}
				if !errors.Is(err, queue.ErrEmpty) {
					return err
				}
			}
			if options.Once {
				fmt.Fprintln(out, "nothing to run")
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(options.Every):
			}
			continue
		}

		var timer <-chan time.Time
		if running < capacity {
			timer = time.After(options.Every)
		}
		select {
		case <-ctx.Done():
			return nil
		case outcome := <-outcomes:
			running--
			remote.Running = running
			if err := remote.Done(ctx, outcome.item, outcome.report); err != nil {
				return err
			}
			if outcome.result != nil {
				printResult(out, outcome.result)
			}
			status := "idle"
			if running > 0 {
				status = fmt.Sprintf("running %d/%d", running, capacity)
			}
			_ = remote.Heartbeat(ctx, status)
			if options.Once {
				return nil
			}
		case <-timer:
		}
	}
}

func executeWorkerItem(ctx context.Context, remote queue.Remote, base *runner.Runner, item queue.Item, options workerOptions, materialize *sync.Mutex) workerOutcome {
	if item.Waiting != "" {
		return workerOutcome{item: item, report: queue.Result{Status: "blocked", Error: item.Waiting, ErrorCode: "REMOTE_DEPENDENCY_WAITING"}}
	}
	active := *base
	active.AI = itemAI(item.AI)
	applyDriverPreset(&active, item.AI)
	bundle, bundleErr := remote.Bundle(ctx, item)
	if bundleErr == nil {
		if waiting := remoteDependencyBlocker(ctx, remote, bundle.Task.DependsOn); waiting != "" {
			return workerOutcome{item: item, report: queue.Result{Status: "blocked", Error: waiting, ErrorCode: "REMOTE_DEPENDENCY_WAITING"}}
		}
		if options.WorkspaceRoot == "" {
			bundleErr = errors.New("worker needs --workspace-root for Cloud-managed work bundles")
		} else {
			bundleRepository := bundle.Repository
			if options.Repository != "" {
				if bundleRepository != "" && bundleRepository != options.Repository {
					bundleErr = errors.New("worker repository does not match the Cloud project")
				}
				bundleRepository = options.Repository
			}
			bundleBranch := bundle.DefaultBranch
			if options.DefaultBranch != "" {
				if bundleBranch != "" && bundleBranch != options.DefaultBranch {
					bundleErr = errors.New("worker default branch does not match the Cloud project")
				}
				bundleBranch = options.DefaultBranch
			}
			if bundleErr == nil {
				materialize.Lock()
				repositoryCheckout, err := syncer.EnsureRepository(ctx, options.WorkspaceRoot, item.Project, bundleRepository, bundleBranch)
				if err == nil {
					_, _, err = syncer.Materialize(ctx, repositoryCheckout, bundle, options.Push)
				}
				materialize.Unlock()
				if err != nil {
					bundleErr = err
				} else {
					created, err := newRunnerAt(repositoryCheckout.Path, options.Driver, options.Command, io.Discard)
					if err != nil {
						bundleErr = err
					} else {
						created.By = "trilha-runner worker " + options.Name
						created.MaxAttempts = options.Attempts
						created.RetryBase = options.RetryBase
						created.AI = itemAI(item.AI)
						applyDriverPreset(created, item.AI)
						active = *created
					}
				}
			}
		}
	}
	var result *runner.Result
	var runErr error
	if bundleErr != nil && !errors.Is(bundleErr, queue.ErrNoBundle) {
		runErr = bundleErr
	} else {
		result, runErr = active.Run(ctx, item.TaskID)
		if runErr == nil && result != nil && options.Push {
			runErr = syncer.PushBranch(ctx, result.Worktree, result.Branch)
		}
	}
	report := resultReport(result, runErr)
	return workerOutcome{item: item, result: result, report: report}
}

func applyDriverPreset(active *runner.Runner, config *queue.AI) {
	if config != nil && driver.IsClaudeCode(config.Provider) {
		active.Driver = driver.ClaudeCode{}
	}
}

func remoteDependencyBlocker(ctx context.Context, remote queue.Remote, dependencies []string) string {
	for _, dependency := range dependencies {
		alias, id, ok := stringsCut(dependency)
		if !ok {
			continue
		}
		status, err := remote.DependencyStatus(ctx, alias, id)
		if err != nil || status != "done" {
			return "waiting:" + dependency
		}
	}
	return ""
}

func stringsCut(value string) (string, string, bool) {
	for index := 0; index < len(value); index++ {
		if value[index] == ':' {
			return value[:index], value[index+1:], true
		}
	}
	return "", "", false
}

func itemAI(config *queue.AI) *driver.AIConfig {
	if config == nil {
		return nil
	}
	return &driver.AIConfig{Provider: config.Provider, BaseURL: config.BaseURL, Model: config.Model, Credential: config.Credential, AllowedHosts: append([]string(nil), config.AllowedHosts...)}
}

func resultReport(result *runner.Result, runErr error) queue.Result {
	report := queue.Result{}
	if result != nil {
		attempts := make([]queue.Attempt, 0, len(result.Attempts))
		for _, attempt := range result.Attempts {
			attempts = append(attempts, queue.Attempt{Number: attempt.Number, Status: attempt.Status, FailureClass: attempt.FailureClass, RepairReason: attempt.RepairReason, Model: attempt.Model, TotalTokens: attempt.TotalTokens, EstimatedCost: attempt.EstimatedCost, Started: attempt.Started, Finished: attempt.Finished})
		}
		report = queue.Result{ProtocolVersion: result.ProtocolVersion, Passed: result.Passed, Status: string(result.Status), Branch: result.Branch, Commit: result.Commit, Evidence: result.Evidence, Log: result.Output, Attempt: result.Attempt, FailureClass: result.FailureClass, Model: result.Model, InputTokens: result.InputTokens, OutputTokens: result.OutputTokens, TotalTokens: result.TotalTokens, EstimatedCost: result.EstimatedCost, DurationMS: result.Elapsed.Milliseconds(), FilesChanged: result.FilesChanged, DiffSummary: result.DiffSummary, AgentReport: result.AgentReport, Attempts: attempts}
	}
	if runErr != nil {
		report.Error = runErr.Error()
		if report.Status == "" {
			report.Status = "failed"
		}
	}
	return report
}
