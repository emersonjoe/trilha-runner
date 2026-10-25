package runner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-runner/internal/taskcompat"
	"github.com/emersonjoe/trilha-runner/worktree"
	"github.com/emersonjoe/trilha-spec/agent"
	"github.com/emersonjoe/trilha-spec/task"
)

type attemptLoop struct {
	runner      *Runner
	ctx         context.Context
	item        *task.Task
	manifest    *agent.Agent
	driver      driver.Driver
	manager     worktree.Manager
	tree        worktree.Worktree
	dir         string
	env         []string
	prefix      []string
	ai          *driver.AIConfig
	prompt      string
	checks      []string
	maxAttempts int
	result      *Result
	started     time.Time
	fail        func(string, error) (*Result, error)
}

func (l *attemptLoop) run() (*Result, error) {
	checkpoints := checkpointStore{path: filepath.Join(l.runner.Layout.Runs(), l.item.ID, "checkpoint.json")}
	checkpoint := checkpoints.load()
	logPath := filepath.Join(l.runner.Layout.Runs(), l.item.ID, "agent.log")
	// Before the agent is asked anything: every program the checks call must
	// exist where the checks will run. A missing one fails every attempt the
	// same way, so the run ends here, with one attempt and no driver call.
	if tool, check, lookErr := l.preflight(); tool != "" {
		return l.failEnvironment(1, "preflight", fmt.Errorf("check %q cannot run: %w", check, lookErr), map[string]string{"tool": tool, "check": check})
	}
	for attemptNumber := 1; attemptNumber <= l.maxAttempts; attemptNumber++ {
		attemptStart := time.Now()
		l.result.Attempt = attemptNumber
		l.runner.logf("attempt %d/%d: driver %s starting", attemptNumber, l.maxAttempts, l.driver.Name())
		l.runner.progress(Event{Phase: "consulting_ai", Attempt: attemptNumber, MaxAttempts: l.maxAttempts, Model: l.model()})
		output, executionErr := l.driver.Execute(l.ctx, driver.Job{Task: l.item, Agent: l.manifest, Prompt: l.prompt, Dir: l.dir, Command: l.runner.Command, Env: l.env, Attempt: attemptNumber, Checkpoint: checkpoint, AllowedChecks: l.checks, AI: l.ai, Prefix: l.prefix})
		if err := l.captureOutput(logPath, output); err != nil {
			return l.fail("evidence", err)
		}
		if executionErr != nil {
			stage := "execution"
			var policyError *driver.PolicyError
			if errors.As(executionErr, &policyError) {
				stage = "policy"
			}
			failureClass, retryable := classifyRetry(executionErr)
			if stage == "policy" {
				failureClass, retryable = "policy", false
			}
			errorCode := errorCodeFor(failureClass)
			attempt := attemptFromOutput(attemptNumber, "failed", failureClass, attemptStart, output)
			attempt.ErrorCode = errorCode
			l.result.Attempts = append(l.result.Attempts, attempt)
			diff, _ := l.manager.DiffStat(l.ctx, l.tree)
			checkpoint = checkpoints.save(attemptNumber, output, executionErr, nil, diff)
			if err := l.recordExecutionFailure(attemptNumber, stage, executionErr, failureClass, errorCode, nil); err != nil {
				return l.fail("evidence", err)
			}
			if retryable && attemptNumber < l.maxAttempts {
				delay := l.runner.retryDelay(attemptNumber, executionErr)
				l.result.Attempts[len(l.result.Attempts)-1].Status = "recovered"
				l.runner.logf("attempt %d failed transiently; retrying in %s", attemptNumber, delay.Round(time.Millisecond))
				l.runner.progress(Event{Phase: "retry_wait", Attempt: attemptNumber, MaxAttempts: l.maxAttempts, AttemptStatus: "failed", FailureClass: failureClass})
				if err := waitRetry(l.ctx, delay); err != nil {
					return l.fail("execution", err)
				}
				continue
			}
			l.result.FailureClass, l.result.ErrorCode = failureClass, errorCode
			if moveErr := taskcompat.Move(l.runner.Store, l.item, task.Failed); moveErr == nil {
				l.result.Status = task.Failed
			}
			l.result.Elapsed = time.Since(l.started)
			return l.result, executionErr
		}

		passed, nextCheckpoint, err := l.verify(attemptNumber, attemptStart, output, logPath, checkpoints)
		checkpoint = nextCheckpoint
		if err != nil {
			return l.fail("verify", err)
		}
		if passed {
			if err := taskcompat.Move(l.runner.Store, l.item, task.Review); err != nil {
				return l.fail("status", err)
			}
			l.result.Passed = true
			l.result.Status = task.Review
			l.result.Elapsed = time.Since(l.started)
			l.runner.progress(Event{Phase: "integrating", Attempt: attemptNumber, MaxAttempts: l.maxAttempts, AttemptStatus: "passed"})
			l.runner.logf("%s is now %s (%d attempts, passed=true)", l.item.ID, task.Review, attemptNumber)
			return l.result, nil
		}
		if err := taskcompat.Move(l.runner.Store, l.item, task.Failed); err != nil {
			return l.fail("status", err)
		}
		if last := l.result.Attempts[len(l.result.Attempts)-1]; last.FailureClass == failureEnvironment {
			// The checks could not run for want of a tool. A corrective
			// attempt would ask the agent to fix what only the worker can.
			l.result.Status = task.Failed
			l.result.FailureClass, l.result.ErrorCode = failureEnvironment, errorMissingTool
			l.result.Elapsed = time.Since(l.started)
			l.runner.logf("a check needs a program this worker does not have; no corrective attempt")
			return l.result, errors.New("verification failed: a check needs a program that is not installed (exit 127)")
		}
		if attemptNumber == l.maxAttempts {
			l.result.Status = task.Failed
			l.result.FailureClass = "checks_failed"
			l.result.Elapsed = time.Since(l.started)
			return l.result, errors.New("verification failed")
		}
		l.result.Attempts[len(l.result.Attempts)-1].Status = "recovered"
		if err := taskcompat.Move(l.runner.Store, l.item, task.Ready); err != nil {
			return l.fail("status", err)
		}
		if err := taskcompat.Move(l.runner.Store, l.item, task.Running); err != nil {
			return l.fail("status", err)
		}
		delay := l.runner.retryDelay(attemptNumber, errors.New("verification failed"))
		l.runner.logf("checks failed; corrective attempt %d starts in %s", attemptNumber+1, delay.Round(time.Millisecond))
		l.runner.progress(Event{Phase: "retry_wait", Attempt: attemptNumber, MaxAttempts: l.maxAttempts, AttemptStatus: "failed", FailureClass: "checks_failed"})
		if err := waitRetry(l.ctx, delay); err != nil {
			return l.fail("verify", err)
		}
	}
	return l.result, errors.New("runner: attempts exhausted")
}

func attemptFromOutput(number int, status, failureClass string, started time.Time, output driver.Output) Attempt {
	tokens := fieldInt(output.Meta["tokens"], 0)
	attempt := Attempt{Number: number, Status: status, FailureClass: failureClass, Model: output.Meta["model"], TotalTokens: tokens, EstimatedCost: estimateCost(tokens), Started: started, Finished: time.Now()}
	// The cost under the protocol's names (§10), as far as the driver told us.
	attempt.Provider = output.Meta["provider"]
	attempt.TokensIn = fieldInt(output.Meta["input_tokens"], 0)
	attempt.TokensOut = fieldInt(output.Meta["output_tokens"], 0)
	if attempt.EstimatedCost > 0 {
		attempt.Cost, attempt.Currency = attempt.EstimatedCost, "USD"
	}
	return attempt
}

// model answers the model the next attempt will ask for, mirroring the
// resolution order driver.AI itself applies: the claim's AI config, then the
// agent manifest. Neither is known yet at consulting_ai time from the
// driver's output, so this is the best information available up front.
func (l *attemptLoop) model() string {
	if l.ai != nil && l.ai.Model != "" {
		return l.ai.Model
	}
	if l.manifest != nil && l.manifest.Model != "" {
		return l.manifest.Model
	}
	return ""
}

// failEnvironment ends the run on the spot because the worker lacks a program
// it needs: one failed attempt that called no driver, a run record naming the
// stage and what is missing, the task failed. There is no retry — asking the
// agent again installs nothing (#13, #14).
func (l *attemptLoop) failEnvironment(attemptNumber int, stage string, cause error, extra map[string]string) (*Result, error) {
	now := time.Now()
	l.result.Attempt = attemptNumber
	l.runner.logf("%s: %v", stage, cause)
	l.result.Attempts = append(l.result.Attempts, Attempt{Number: attemptNumber, Status: "failed", FailureClass: failureEnvironment, ErrorCode: errorMissingTool, Started: now, Finished: now})
	if err := l.recordExecutionFailure(attemptNumber, stage, cause, failureEnvironment, errorMissingTool, extra); err != nil {
		return l.fail("evidence", err)
	}
	l.result.FailureClass, l.result.ErrorCode = failureEnvironment, errorMissingTool
	if moveErr := taskcompat.Move(l.runner.Store, l.item, task.Failed); moveErr == nil {
		l.result.Status = task.Failed
	}
	l.result.Elapsed = time.Since(l.started)
	return l.result, cause
}
