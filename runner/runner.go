// Package runner executes one task end to end, the local pipeline of the
// Trilha strategy:
//
//	Task → Agent → Worktree → Execution → Verification → Evidence
//
// It reads and writes the protocol through trilha-spec, starts the agent
// through a driver, and leaves behind a branch, evidence records and a
// status the reviewer decides on. Nothing here talks to a control plane;
// that is the queue's job.
package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-runner/sandbox"
	"github.com/emersonjoe/trilha-runner/worktree"
	"github.com/emersonjoe/trilha-spec/agent"
	"github.com/emersonjoe/trilha-spec/ai"
	"github.com/emersonjoe/trilha-spec/spec"
	"github.com/emersonjoe/trilha-spec/task"
)

// Runner runs tasks of one project.
type Runner struct {
	Layout spec.Layout
	Store  *task.Store
	// Driver overrides the manifest's driver when set.
	Driver driver.Driver
	// Command overrides the manifest's command (exec driver).
	Command string
	Sandbox sandbox.Sandbox
	// By is recorded as the author of the evidence.
	By string
	// Log receives progress lines; nil discards them.
	Log func(string)
}

// Result is what a run leaves behind.
type Result struct {
	Task     string          `json:"task"`
	Status   task.Status     `json:"status"`
	Passed   bool            `json:"passed"`
	Branch   string          `json:"branch"`
	Worktree string          `json:"worktree"`
	Commit   string          `json:"commit,omitempty"`
	Driver   string          `json:"driver"`
	Output   string          `json:"output,omitempty"`
	Evidence []task.Evidence `json:"evidence"`
	Elapsed  time.Duration   `json:"elapsed"`
}

// New opens the layout above dir.
func New(dir string) (*Runner, error) {
	st, err := task.Open(dir)
	if err != nil {
		return nil, err
	}
	return &Runner{Layout: st.Layout, Store: st, Sandbox: sandbox.None{}, By: "trilha-runner"}, nil
}

func (r *Runner) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(fmt.Sprintf(format, args...))
	}
}

// Run executes one task. The task must be ready with every dependency
// done; the runner moves it to running, and at the end to review (checks
// passed) or failed. A failure to even start — no driver, no git — leaves
// the task where it was.
func (r *Runner) Run(ctx context.Context, id string) (*Result, error) {
	start := time.Now()
	t, err := r.Store.Get(id)
	if err != nil {
		return nil, err
	}
	if t.Status != task.Ready {
		return nil, fmt.Errorf("task %s is %s, not ready", id, t.Status)
	}
	proj, err := r.Layout.LoadProject()
	if err != nil {
		return nil, err
	}
	var man *agent.Agent
	if name := firstNonEmpty(t.Agent, proj.DefaultAgent); name != "" {
		if man, err = agent.Load(r.Layout, name); err != nil {
			return nil, err
		}
	}
	drv := r.Driver
	if drv == nil {
		name := "exec"
		if man != nil && man.Driver != "" {
			name = man.Driver
		}
		if drv, err = driver.New(name); err != nil {
			return nil, err
		}
	}
	wm := worktree.Manager{Layout: r.Layout}
	if err := wm.IsRepo(ctx); err != nil {
		return nil, err
	}
	pack, err := ai.Build(r.Layout, id)
	if err != nil {
		return nil, err
	}
	if _, err := r.Store.Move(id, task.Running); err != nil {
		return nil, err
	}
	res := &Result{Task: id, Driver: drv.Name()}
	fail := func(stage string, cause error) (*Result, error) {
		r.logf("%s: %v", stage, cause)
		task.Record(r.Layout, task.Evidence{Task: id, Kind: "run", By: r.By, Note: stage + ": " + cause.Error(), Meta: map[string]string{"driver": drv.Name(), "stage": stage}})
		if _, err := r.Store.Move(id, task.Failed); err == nil {
			res.Status = task.Failed
		}
		res.Elapsed = time.Since(start)
		return res, cause
	}

	wt, err := wm.Create(ctx, id)
	if err != nil {
		return fail("worktree", err)
	}
	res.Branch, res.Worktree = wt.Branch, wt.Path
	r.logf("worktree %s on %s", wt.Path, wt.Branch)

	sb := r.Sandbox
	if sb == nil {
		sb = sandbox.None{}
	}
	env, release, err := sb.Prepare(ctx, wt.Path)
	if err != nil {
		return fail("sandbox", err)
	}
	defer release()

	r.logf("driver %s starting", drv.Name())
	out, execErr := drv.Execute(ctx, driver.Job{Task: t, Agent: man, Prompt: pack.Markdown(), Dir: env.Dir, Command: r.Command, Env: env.Env})
	res.Output = out.Text
	logPath := filepath.Join(r.Layout.Runs(), id, "agent.log")
	os.MkdirAll(filepath.Dir(logPath), 0o755)
	os.WriteFile(logPath, []byte(out.Text), 0o644)
	if execErr != nil {
		return fail("execution", execErr)
	}

	sha, err := wm.Commit(ctx, wt, fmt.Sprintf("%s: %s", id, t.Title))
	if err != nil {
		return fail("commit", err)
	}
	res.Commit = sha
	if sha != "" {
		r.logf("committed %s", sha[:12])
	} else {
		r.logf("nothing to commit")
	}

	if _, err := r.Store.Move(id, task.Verify); err != nil {
		return fail("verify", err)
	}
	v, err := task.RunChecks(ctx, r.Layout, t, env.Dir, r.By)
	if err != nil {
		return fail("verify", err)
	}
	res.Evidence = v.Evidence
	evals, err := r.recordEvals(v, id, env.Dir)
	if err != nil {
		return fail("verify", err)
	}
	res.Evidence = append(res.Evidence, evals...)
	// A metric that misses its threshold fails the verification even when
	// every command exited 0.
	res.Passed = v.Passed
	for _, e := range evals {
		if !e.Passed {
			res.Passed = false
		}
	}
	meta := map[string]string{"driver": drv.Name(), "branch": wt.Branch, "commit": sha, "elapsed": time.Since(start).Round(time.Millisecond).String()}
	if s := summarize(evals); s != "" {
		meta["metrics"] = s
	}
	for k, val := range out.Meta {
		meta[k] = val
	}
	if stat, err := wm.DiffStat(ctx, wt); err == nil && stat != "" {
		meta["diffstat"] = stat
	}
	run, _, err := task.Record(r.Layout, task.Evidence{Task: id, Kind: "run", By: r.By, Passed: res.Passed, Note: strings.TrimSpace(firstLines(out.Text, 20)), Files: []string{logPath}, Meta: meta})
	if err != nil {
		return fail("evidence", err)
	}
	res.Evidence = append(res.Evidence, run)

	to := task.Failed
	if res.Passed {
		to = task.Review
	}
	if _, err := r.Store.Move(id, to); err != nil {
		return fail("status", err)
	}
	res.Status = to
	res.Elapsed = time.Since(start)
	r.logf("%s is now %s (%d checks, %d metrics, passed=%v)", id, to, len(v.Evidence), len(evals), res.Passed)
	if !res.Passed {
		return res, errors.New("verification failed")
	}
	return res, nil
}

// Next runs the first executable task, or answers ErrNothing.
func (r *Runner) Next(ctx context.Context) (*Result, error) {
	g, err := r.Store.Graph()
	if err != nil {
		return nil, err
	}
	t := g.Next()
	if t == nil {
		return nil, ErrNothing
	}
	return r.Run(ctx, t.ID)
}

// ErrNothing is a graph with no executable task.
var ErrNothing = errors.New("runner: no task is ready with every dependency done")

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
