package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-spec/task"
)

// scripted answers each attempt from a list of errors (nil writes the file
// the check looks for) and keeps the jobs it was given.
type scripted struct {
	errs []error
	jobs []driver.Job
}

func (s *scripted) Name() string { return "scripted" }

func (s *scripted) Execute(ctx context.Context, job driver.Job) (driver.Output, error) {
	s.jobs = append(s.jobs, job)
	err := s.errs[min(len(s.jobs), len(s.errs))-1]
	if err == nil {
		os.WriteFile(filepath.Join(job.Dir, "done.txt"), []byte("ok\n"), 0o644)
		return driver.Output{Text: "done", Meta: map[string]string{"model": "m1", "tokens": "10"}}, nil
	}
	return driver.Output{Text: "partial", Meta: map[string]string{"model": "m1"}}, err
}

func retrying(t *testing.T, errs ...error) (*Runner, *scripted, *task.Task, *[]Event) {
	t.Helper()
	r, _ := New(repo(t))
	agent := &scripted{errs: errs}
	r.Driver = agent
	r.RetryBase = time.Millisecond
	var events []Event
	r.Progress = func(e Event) { events = append(events, e) }
	tk, _ := r.Store.Create("Flaky provider", func(x *task.Task) {
		x.Status = task.Ready
		x.Acceptance = []string{"done.txt exists"}
		x.Checks = []string{"test -f done.txt"}
	})
	return r, agent, tk, &events
}

// The provider answers 503 once; the run waits, tries again with what the
// first attempt left behind, and reaches review on the second attempt.
func TestTransientProviderFailureIsRetried(t *testing.T) {
	r, agent, tk, events := retrying(t, errors.New("HTTP 503 Service Unavailable"), nil)
	res, err := r.Run(context.Background(), tk.ID)
	if err != nil || res.Status != task.Review || !res.Passed || res.Attempt != 2 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(res.Attempts) != 2 || res.Attempts[0].Status != "recovered" || res.Attempts[0].FailureClass != "provider_transient" || res.Attempts[1].Status != "passed" {
		t.Fatalf("attempts = %+v", res.Attempts)
	}
	if len(agent.jobs) != 2 || agent.jobs[1].Attempt != 2 || !strings.Contains(agent.jobs[1].Checkpoint, "503") {
		t.Fatalf("second job: attempt %d checkpoint %q", agent.jobs[1].Attempt, agent.jobs[1].Checkpoint)
	}
	var waited bool
	for _, e := range *events {
		if e.Phase == "retry_wait" && e.FailureClass == "provider_transient" && e.Attempt == 1 {
			waited = true
		}
	}
	if !waited {
		t.Fatalf("no retry_wait event: %+v", *events)
	}
	ev, _ := task.ListEvidence(r.Layout, tk.ID)
	if len(ev) != 3 || ev[0].Meta["failure_class"] != "provider_transient" || ev[0].Meta["attempt"] != "1" || ev[2].Meta["attempt"] != "2" {
		t.Fatalf("evidence = %+v", ev)
	}
}

// A refused credential does not get better by asking again.
func TestPermanentProviderFailureIsNotRetried(t *testing.T) {
	r, agent, tk, _ := retrying(t, errors.New("HTTP 401 Unauthorized"), nil)
	res, err := r.Run(context.Background(), tk.ID)
	if err == nil || res.Status != task.Failed || res.FailureClass != "provider_permanent" || len(agent.jobs) != 1 {
		t.Fatalf("res=%+v err=%v jobs=%d", res, err, len(agent.jobs))
	}
}

func TestTransientFailuresExhaustAttempts(t *testing.T) {
	r, agent, tk, _ := retrying(t, errors.New("connection reset by peer"))
	r.MaxAttempts = 2
	res, err := r.Run(context.Background(), tk.ID)
	if err == nil || res.Status != task.Failed || res.FailureClass != "provider_transient" || len(agent.jobs) != 2 {
		t.Fatalf("res=%+v err=%v jobs=%d", res, err, len(agent.jobs))
	}
	if got, _ := r.Store.Get(tk.ID); got.Status != task.Failed {
		t.Fatalf("status = %s", got.Status)
	}
}

// Checks that fail lead to a corrective attempt; the task passes through
// ready → running again and reaches review when the checks pass.
func TestFailedChecksGetACorrectiveAttempt(t *testing.T) {
	r, _ := New(repo(t))
	r.RetryBase = time.Millisecond
	var attempts []int
	r.Driver = driverFunc(func(ctx context.Context, job driver.Job) (driver.Output, error) {
		attempts = append(attempts, job.Attempt)
		if job.Attempt == 2 {
			os.WriteFile(filepath.Join(job.Dir, "done.txt"), []byte("ok\n"), 0o644)
		}
		return driver.Output{Text: "tried"}, nil
	})
	tk, _ := r.Store.Create("Needs a second look", func(x *task.Task) {
		x.Status = task.Ready
		x.Acceptance = []string{"done.txt exists"}
		x.Checks = []string{"test -f done.txt"}
	})
	res, err := r.Run(context.Background(), tk.ID)
	if err != nil || res.Status != task.Review || res.Attempt != 2 || len(attempts) != 2 {
		t.Fatalf("res=%+v err=%v attempts=%v", res, err, attempts)
	}
	if res.Attempts[0].Status != "recovered" || res.Attempts[0].FailureClass != "checks_failed" || res.Attempts[1].Status != "passed" {
		t.Fatalf("attempts = %+v", res.Attempts)
	}
}

type driverFunc func(context.Context, driver.Job) (driver.Output, error)

func (driverFunc) Name() string { return "scripted" }
func (f driverFunc) Execute(ctx context.Context, job driver.Job) (driver.Output, error) {
	return f(ctx, job)
}
