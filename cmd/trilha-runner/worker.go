package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
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

	// heartbeatTicker and execute are seams for tests: the clock that paces
	// heartbeats during a run, and the execution of one claimed item.
	// resultRetryBase is the first backoff of a failed result delivery.
	heartbeatTicker func(time.Duration) (<-chan time.Time, func())
	resultRetryBase time.Duration
	execute         func(ctx context.Context, remote queue.Remote, item queue.Item) workerOutcome
}

// heartbeatEvery is how often a worker with an active run says it is alive
// (RM-013). The cloud calls a worker offline after 90 s without one.
const heartbeatEvery = 30 * time.Second

func realTicker(every time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(every)
	return ticker.C, ticker.Stop
}

// keepAlive sends `running n/N` every tick while at least one run is active,
// so a run of twenty minutes does not leave the worker looking offline. It
// stops with ctx. Best effort, like every heartbeat.
func keepAlive(ctx context.Context, remote queue.Remote, running *atomic.Int32, capacity int, ticker func(time.Duration) (<-chan time.Time, func())) {
	ticks, stop := ticker(heartbeatEvery)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			n := int(running.Load())
			if n == 0 {
				continue
			}
			beat := remote
			beat.Running = n
			_ = beat.Heartbeat(ctx, fmt.Sprintf("running %d/%d", n, capacity))
		}
	}
}

type workerOutcome struct {
	item   queue.Item
	result *runner.Result
	report queue.Result
	// root is the checkout that ran the item, where its outbox lives.
	root string
}

func runRemoteWorker(ctx context.Context, out io.Writer, remote queue.Remote, base *runner.Runner, delivery deployer.Config, deliveryEnabled bool, options workerOptions) error {
	// A run in progress reports its own infrastructure failures in
	// Result.Error; this heartbeat only covers the worker itself stopping —
	// SIGTERM from systemd today — reported best-effort so the fleet panel
	// does not have to infer a shutdown from a heartbeat that just stops.
	defer func() {
		if ctx.Err() != nil {
			stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = remote.Heartbeat(stopCtx, "stopping")
		}
	}()
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
	baseRoot := ""
	if base != nil {
		baseRoot = base.Layout.Root
	}
	// Results a previous process could not deliver go first.
	if err := flushOutbox(ctx, remote, outboxRoots(baseRoot, options.WorkspaceRoot), options.resultRetryBase); err != nil {
		return nil // only a shutdown stops a flush
	}
	outcomes := make(chan workerOutcome, capacity)
	var materialize sync.Mutex
	running := 0
	var active atomic.Int32
	ticker := options.heartbeatTicker
	if ticker == nil {
		ticker = realTicker
	}
	execute := options.execute
	if execute == nil {
		execute = func(ctx context.Context, workRemote queue.Remote, item queue.Item) workerOutcome {
			return executeWorkerItem(ctx, workRemote, base, item, options, &materialize)
		}
	}
	aliveCtx, stopAlive := context.WithCancel(ctx)
	defer stopAlive()
	go keepAlive(aliveCtx, remote, &active, capacity, ticker)
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
			active.Store(int32(running))
			remote.Running = running
			_ = remote.Heartbeat(ctx, fmt.Sprintf("running %d/%d", running, capacity))
			workRemote := remote
			go func(item queue.Item, workRemote queue.Remote) {
				outcomes <- execute(ctx, workRemote, item)
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
			active.Store(int32(running))
			remote.Running = running
			entry := outboxEntry{RunID: outcome.item.ID, TaskID: outcome.item.TaskID, Result: outcome.report}
			root := outcome.root
			if root == "" {
				root = baseRoot
			}
			path, err := writeOutbox(root, entry)
			if err != nil {
				fmt.Fprintf(os.Stderr, "· outbox for %s: %v\n", outcome.item.ID, err)
			}
			if err := deliver(ctx, remote, entry, path, options.resultRetryBase); err != nil {
				return nil // shutting down; the outbox keeps the result
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
	root := base.Layout.Root
	active.AI = itemAI(item.AI)
	active.Progress = progressReporter(ctx, remote, item)
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
						created.Progress = progressReporter(ctx, remote, item)
						applyDriverPreset(created, item.AI)
						active = *created
						root = created.Layout.Root
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
	return workerOutcome{item: item, result: result, report: report, root: root}
}

// progressReporter adapts a runner.Event to a best-effort POST against the
// control plane. The runner's Progress hook never parses Log strings; this
// is the one place the structured event turns into the wire shape.
func progressReporter(ctx context.Context, remote queue.Remote, item queue.Item) func(runner.Event) {
	return func(e runner.Event) {
		_ = remote.Progress(ctx, item, queue.Progress{
			Phase:         e.Phase,
			Message:       e.Message,
			Attempt:       e.Attempt,
			MaxAttempts:   e.MaxAttempts,
			Branch:        e.Branch,
			Model:         e.Model,
			AttemptStatus: e.AttemptStatus,
			FailureClass:  e.FailureClass,
			ErrorCode:     e.ErrorCode,
		})
	}
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
			attempts = append(attempts, queue.Attempt{Number: attempt.Number, Status: attempt.Status, FailureClass: attempt.FailureClass, ErrorCode: attempt.ErrorCode, RepairReason: attempt.RepairReason, Model: attempt.Model, TotalTokens: attempt.TotalTokens, EstimatedCost: attempt.EstimatedCost, Provider: attempt.Provider, TokensIn: attempt.TokensIn, TokensOut: attempt.TokensOut, Cost: attempt.Cost, Currency: attempt.Currency, Started: attempt.Started, Finished: attempt.Finished})
		}
		report = queue.Result{ProtocolVersion: result.ProtocolVersion, Passed: result.Passed, Status: string(result.Status), Branch: result.Branch, Commit: result.Commit, Evidence: result.Evidence, Log: result.Output, Attempt: result.Attempt, FailureClass: result.FailureClass, ErrorCode: result.ErrorCode, Model: result.Model, InputTokens: result.InputTokens, OutputTokens: result.OutputTokens, TotalTokens: result.TotalTokens, EstimatedCost: result.EstimatedCost, DurationMS: result.Elapsed.Milliseconds(), FilesChanged: result.FilesChanged, DiffSummary: result.DiffSummary, AgentReport: result.AgentReport, Attempts: attempts}
	}
	if runErr != nil {
		report.Error = runErr.Error()
		if report.Status == "" {
			report.Status = "failed"
		}
	}
	return report
}
