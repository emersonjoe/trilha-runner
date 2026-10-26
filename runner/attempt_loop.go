package runner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-runner/internal/taskcompat"
	"github.com/emersonjoe/trilha-runner/worktree"
	"github.com/emersonjoe/trilha-spec/agent"
	"github.com/emersonjoe/trilha-spec/execution"
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
	// agent gathers what the agent reports; runDir is the run's directory
	// outside the worktree; self is the binary hooks call (empty in a
	// sandbox); resume is the session the first attempt continues with
	// resumePrompt.
	agent        *agentSession
	runDir       string
	self         string
	resume       string
	resumePrompt string
	// readOnly is a hunt or a report-mode review: no preflight, no commit,
	// no check, no task move; it ends with finishReadOnly.
	readOnly bool
}

// execute runs one attempt of the driver. The first attempt of a
// continuation resumes the agent's own conversation with the answer; if that
// fails (the session is gone: another worker, a cleaned home), the same
// attempt starts a fresh session that reads the question and answer quoted.
func (l *attemptLoop) execute(attemptNumber int, checkpoint string) (driver.Output, error) {
	job := driver.Job{Task: l.item, Agent: l.manifest, Prompt: l.prompt, Dir: l.dir, Command: l.runner.Command, Env: l.env, Attempt: attemptNumber, Checkpoint: checkpoint, AllowedChecks: l.checks, AI: l.ai, Prefix: l.prefix, RunDir: l.runDir, Self: l.self,
		Kind: l.runner.Work.Kind, ReadOnly: l.readOnly}
	for _, comment := range l.runner.Work.ReviewComments {
		job.ReplyTo = append(job.ReplyTo, comment.ID)
	}
	if l.agent != nil {
		job.Signal = l.agent.handle
	}
	if attemptNumber == 1 && l.resume != "" {
		resumed := job
		resumed.Prompt, resumed.Resume = l.resumePrompt, l.resume
		output, err := l.driver.Execute(l.ctx, resumed)
		if err == nil || l.agent.hasQuestion() {
			return output, err
		}
		l.runner.logf("resuming session %s failed (%v); starting a fresh session with the question and answer quoted", l.resume, err)
	}
	return l.driver.Execute(l.ctx, job)
}

func (l *attemptLoop) run() (*Result, error) {
	checkpoints := checkpointStore{path: filepath.Join(l.runDir, "checkpoint.json")}
	checkpoint := checkpoints.load()
	logPath := filepath.Join(l.runDir, "agent.log")
	// Before the agent is asked anything: every program the checks call must
	// exist where the checks will run. A missing one fails every attempt the
	// same way, so the run ends here, with one attempt and no driver call.
	// A read-only run runs no check.
	if l.readOnly {
		checkpoint = ""
	} else if tool, check, lookErr := l.preflight(); tool != "" {
		return l.failEnvironment(1, "preflight", fmt.Errorf("check %q cannot run: %w", check, lookErr), map[string]string{"tool": tool, "check": check})
	}
	for attemptNumber := 1; attemptNumber <= l.maxAttempts; attemptNumber++ {
		attemptStart := time.Now()
		l.result.Attempt = attemptNumber
		l.runner.logf("attempt %d/%d: driver %s starting", attemptNumber, l.maxAttempts, l.driver.Name())
		l.runner.progress(Event{Phase: "consulting_ai", Attempt: attemptNumber, MaxAttempts: l.maxAttempts, Model: l.model()})
		output, executionErr := l.execute(attemptNumber, checkpoint)
		if err := l.captureOutput(logPath, output); err != nil {
			return l.fail("evidence", err)
		}
		if question := l.agent.takeQuestion(); question != nil && l.readOnly {
			l.runner.logf("a read-only run does not stop for questions; the agent asked: %s", execution.Truncate(question.Text, 200))
		} else if question != nil {
			// The agent stopped to ask. Whatever it did so far is kept on the
			// branch; no check runs, no attempt failed, nothing is retried.
			return l.needsInput(attemptNumber, attemptStart, output, question)
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
			if l.readOnly {
				l.result.Status = task.Failed
			} else if moveErr := taskcompat.Move(l.runner.Store, l.item, task.Failed); moveErr == nil {
				l.result.Status = task.Failed
			}
			l.result.Elapsed = time.Since(l.started)
			return l.result, executionErr
		}
		if l.readOnly {
			return l.finishReadOnly(attemptNumber, attemptStart, output)
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
	attempt := Attempt{Number: number, Status: status, FailureClass: failureClass, Model: output.Meta["model"], TotalTokens: tokens, EstimatedCost: outputCost(output, tokens), Started: started, Finished: time.Now()}
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

// needsInput ends the run with the agent's question: the work so far
// committed, one attempt that did not fail, a run record that says it is
// waiting for an answer, and the task blocked here until one comes.
func (l *attemptLoop) needsInput(attemptNumber int, attemptStart time.Time, output driver.Output, question *execution.Question) (*Result, error) {
	sha, err := l.manager.Commit(l.ctx, l.tree, fmt.Sprintf("%s: %s (attempt %d, asked a question)", l.item.ID, l.item.Title, attemptNumber))
	if err != nil {
		return l.fail("commit", err)
	}
	if sha != "" {
		l.result.Commit = sha
	}
	l.result.Attempts = append(l.result.Attempts, attemptFromOutput(attemptNumber, "passed", "", attemptStart, output))
	meta := map[string]string{"driver": l.driver.Name(), "stage": "needs_input", "attempt": strconv.Itoa(attemptNumber), "branch": l.tree.Branch, "commit": l.result.Commit}
	evidence, _, err := task.Record(l.runner.Layout, task.Evidence{Task: l.item.ID, Kind: "run", By: l.runner.By, Note: "needs_input: the agent asked a question and stopped; an answer continues it in a new run", Meta: meta})
	if err != nil {
		return l.fail("evidence", err)
	}
	l.result.Evidence = append(l.result.Evidence, evidence)
	if err := taskcompat.Move(l.runner.Store, l.item, task.Blocked); err != nil {
		return l.fail("status", err)
	}
	l.result.Status = task.Blocked
	l.result.Passed = false
	l.result.Question = question
	l.result.Elapsed = time.Since(l.started)
	l.runner.progress(Event{Phase: string(execution.PhaseAwaitingInput), Message: "the agent asked a question", Attempt: attemptNumber, MaxAttempts: l.maxAttempts, AttemptStatus: "passed"})
	l.runner.logf("%s needs input: the agent asked a question", l.item.ID)
	return l.result, nil
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
