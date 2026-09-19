package runner

import (
	"context"
	"errors"
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
	for attemptNumber := 1; attemptNumber <= l.maxAttempts; attemptNumber++ {
		attemptStart := time.Now()
		l.result.Attempt = attemptNumber
		l.runner.logf("attempt %d/%d: driver %s starting", attemptNumber, l.maxAttempts, l.driver.Name())
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
			l.result.Attempts = append(l.result.Attempts, attemptFromOutput(attemptNumber, "failed", failureClass, attemptStart, output))
			diff, _ := l.manager.DiffStat(l.ctx, l.tree)
			checkpoint = checkpoints.save(attemptNumber, output, executionErr, nil, diff)
			if err := l.recordExecutionFailure(attemptNumber, stage, executionErr, failureClass); err != nil {
				return l.fail("evidence", err)
			}
			if retryable && attemptNumber < l.maxAttempts {
				delay := l.runner.retryDelay(attemptNumber, executionErr)
				l.result.Attempts[len(l.result.Attempts)-1].Status = "recovered"
				l.runner.logf("attempt %d failed transiently; retrying in %s", attemptNumber, delay.Round(time.Millisecond))
				if err := waitRetry(l.ctx, delay); err != nil {
					return l.fail("execution", err)
				}
				continue
			}
			l.result.FailureClass = failureClass
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
			l.runner.logf("%s is now %s (%d attempts, passed=true)", l.item.ID, task.Review, attemptNumber)
			return l.result, nil
		}
		if err := taskcompat.Move(l.runner.Store, l.item, task.Failed); err != nil {
			return l.fail("status", err)
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
		if err := waitRetry(l.ctx, delay); err != nil {
			return l.fail("verify", err)
		}
	}
	return l.result, errors.New("runner: attempts exhausted")
}

func attemptFromOutput(number int, status, failureClass string, started time.Time, output driver.Output) Attempt {
	tokens := fieldInt(output.Meta["tokens"], 0)
	return Attempt{Number: number, Status: status, FailureClass: failureClass, Model: output.Meta["model"], TotalTokens: tokens, EstimatedCost: estimateCost(tokens), Started: started, Finished: time.Now()}
}
