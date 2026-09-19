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
	// SandboxSpec is what the sandbox should build, from the agent manifest.
	SandboxSpec *sandbox.Spec
	// Access is the per-project model access the control plane delivered with
	// this run, if any. The runner passes it to the driver and keeps it
	// nowhere else.
	Access *driver.Access
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
	fail := func(stage string, cause error, extra ...map[string]string) (*Result, error) {
		r.logf("%s: %v", stage, cause)
		meta := map[string]string{"driver": drv.Name(), "stage": stage}
		for _, m := range extra {
			for k, v := range m {
				meta[k] = v
			}
		}
		meta["stage"] = stage
		task.Record(r.Layout, task.Evidence{Task: id, Kind: "run", By: r.By, Note: stage + ": " + cause.Error(), Meta: meta})
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
	env, release, err := sb.Prepare(ctx, sandbox.Request{Task: id, Dir: wt.Path, Env: r.access(), Spec: r.SandboxSpec})
	if err != nil {
		return fail("sandbox", err)
	}
	defer release()
	if env.Inside() {
		r.logf("sandbox %s: commands run in %s", sb.Name(), env.WorkDir)
	}

	r.logf("driver %s starting", drv.Name())
	out, execErr := drv.Execute(ctx, driver.Job{Task: t, Agent: man, Prompt: pack.Markdown(), Dir: env.Dir,
		Command: r.Command, Env: env.Env, Access: r.Access, Prefix: env.Prefix, WorkDir: env.WorkDir})
	res.Output = out.Text
	logPath := filepath.Join(r.Layout.Runs(), id, "agent.log")
	os.MkdirAll(filepath.Dir(logPath), 0o755)
	os.WriteFile(logPath, []byte(out.Text), 0o644)
	if execErr != nil {
		// A refusal by policy is not a failure to execute: the run never
		// started, and the evidence says which rule stopped it.
		if errors.Is(execErr, driver.ErrPolicy) {
			return fail("policy", execErr, out.Meta)
		}
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
	v, err := r.verify(ctx, t, env)
	if err != nil {
		return fail("verify", err)
	}
	res.Evidence = v.Evidence
	// v.Passed already accounts for the metrics the checks printed: the
	// protocol records an `eval` per metric line and a missed gate fails the
	// verification even when every command exited 0.
	res.Passed = v.Passed
	meta := map[string]string{"driver": drv.Name(), "branch": wt.Branch, "commit": sha, "elapsed": time.Since(start).Round(time.Millisecond).String()}
	if s := summarize(v.Evidence); s != "" {
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
	r.logf("%s is now %s (%d records, passed=%v)", id, to, len(v.Evidence), res.Passed)
	if !res.Passed {
		return res, errors.New("verification failed")
	}
	return res, nil
}

// access answers the environment the run's model access adds, so a sandbox
// can put it inside the container instead of on a command line. A refusal by
// policy is left to the driver, which is where the run stops.
func (r *Runner) access() []string {
	env, err := r.Access.Env()
	if err != nil {
		return nil
	}
	return env
}

// verify runs the task's checks — then the project's — where the sandbox says.
//
// Without a sandbox that moves the work this is the protocol's own RunChecks,
// which already records one `check` per command and one `eval` per metric line
// the command printed. With one, the loop is here because each command has to
// be wrapped to run inside, and the evidence keeps the wrapped line: what ran
// is what the record says. Every decision inside it is still the protocol's —
// RunCheck, ParseMetricLine, Eval, Record — so the two paths cannot drift on
// anything but the wrapping.
func (r *Runner) verify(ctx context.Context, t *task.Task, env sandbox.Env) (*task.Verification, error) {
	if !env.Inside() {
		return task.RunChecks(ctx, r.Layout, t, env.Dir, r.By)
	}
	proj, err := r.Layout.LoadProject()
	if err != nil {
		return nil, err
	}
	commands := append(append([]string(nil), t.Checks...), proj.Verify...)
	v := &task.Verification{Task: t.ID, Passed: true}
	keep := func(e task.Evidence, p string) {
		v.Evidence, v.Paths = append(v.Evidence, e), append(v.Paths, p)
		if !e.Passed {
			v.Passed = false
		}
	}
	if len(commands) == 0 {
		v.Passed = false
		e, p, err := task.Record(r.Layout, task.Evidence{Task: t.ID, Kind: "note", By: r.By, Dir: env.WorkDir,
			Note: "no checks to run: add `checks:` to the task or `verify:` to project.md"})
		if err != nil {
			return nil, err
		}
		v.Evidence, v.Paths = append(v.Evidence, e), append(v.Paths, p)
		return v, nil
	}
	for _, c := range commands {
		check := task.RunCheck(ctx, t.ID, env.Wrap(c), env.Dir, r.By)
		output := check.Output
		e, p, err := task.Record(r.Layout, check)
		if err != nil {
			return nil, err
		}
		keep(e, p)
		for _, line := range strings.Split(output, "\n") {
			m, why, ok := task.ParseMetricLine(line)
			if !ok {
				continue
			}
			ev := task.Eval(t.ID, r.By, m)
			ev.Command, ev.Dir = env.Wrap(c), env.Dir
			if why != "" {
				ev.Note, ev.Passed = why, false
			}
			ev, p, err := task.Record(r.Layout, ev)
			if err != nil {
				return nil, err
			}
			keep(ev, p)
			r.logf("eval %s (passed=%v)", m, ev.Passed)
		}
	}
	return v, nil
}

// summarize renders the metrics for the run record's meta, so the summary a
// reviewer reads first already carries the numbers.
func summarize(records []task.Evidence) string {
	metrics := task.Metrics(records)
	parts := make([]string, 0, len(metrics))
	for _, m := range metrics {
		mark := "fail"
		if m.Passed {
			mark = "pass"
		}
		parts = append(parts, m.String()+" "+mark)
	}
	return strings.Join(parts, "; ")
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
