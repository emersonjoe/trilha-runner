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
	"time"

	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-runner/internal/taskcompat"
	"github.com/emersonjoe/trilha-runner/queue"
	"github.com/emersonjoe/trilha-runner/sandbox"
	"github.com/emersonjoe/trilha-runner/worktree"
	"github.com/emersonjoe/trilha-spec/agent"
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
	// AI is a transient, project-scoped provider policy supplied by the queue.
	AI *driver.AIConfig
	// Repositories maps cross-repository dependency aliases to sibling checkouts.
	Repositories map[string]string
	Sandbox      sandbox.Sandbox
	// By is recorded as the author of the evidence.
	By string
	// Log receives progress lines; nil discards them.
	Log func(string)
	// MaxAttempts includes the first attempt. Default: 3.
	MaxAttempts int
	// RetryBase is the first exponential-backoff delay. Default: 1 second.
	RetryBase time.Duration
}

// Attempt is one provider or corrective execution inside a logical run.
type Attempt struct {
	Number        int       `json:"number"`
	Status        string    `json:"status"`
	FailureClass  string    `json:"failure_class,omitempty"`
	RepairReason  string    `json:"repair_reason,omitempty"`
	Model         string    `json:"model,omitempty"`
	TotalTokens   int       `json:"total_tokens,omitempty"`
	EstimatedCost float64   `json:"estimated_cost,omitempty"`
	Started       time.Time `json:"started"`
	Finished      time.Time `json:"finished"`
}

// Result is what a run leaves behind.
type Result struct {
	ProtocolVersion string          `json:"protocol_version"`
	Task            string          `json:"task"`
	Status          task.Status     `json:"status"`
	Passed          bool            `json:"passed"`
	Branch          string          `json:"branch"`
	Worktree        string          `json:"worktree"`
	Commit          string          `json:"commit,omitempty"`
	Driver          string          `json:"driver"`
	Output          string          `json:"output,omitempty"`
	Evidence        []task.Evidence `json:"evidence"`
	Elapsed         time.Duration   `json:"elapsed"`
	Attempt         int             `json:"attempt,omitempty"`
	Attempts        []Attempt       `json:"attempts,omitempty"`
	Model           string          `json:"model,omitempty"`
	InputTokens     int             `json:"input_tokens,omitempty"`
	OutputTokens    int             `json:"output_tokens,omitempty"`
	TotalTokens     int             `json:"total_tokens,omitempty"`
	EstimatedCost   float64         `json:"estimated_cost,omitempty"`
	FailureClass    string          `json:"failure_class,omitempty"`
	FilesChanged    []string        `json:"files_changed,omitempty"`
	DiffSummary     string          `json:"diff_summary,omitempty"`
	AgentReport     string          `json:"agent_report,omitempty"`
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
	t, err := taskcompat.Get(r.Store, id)
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
	pack, err := buildContextPack(r.Layout, t, proj, man)
	if err != nil {
		return nil, err
	}
	if err := r.moveRunning(t); err != nil {
		return nil, err
	}
	res := &Result{ProtocolVersion: "trilha.execution/v1", Task: id, Driver: drv.Name()}
	fail := func(stage string, cause error) (*Result, error) {
		r.logf("%s: %v", stage, cause)
		task.Record(r.Layout, task.Evidence{Task: id, Kind: "run", By: r.By, Note: stage + ": " + cause.Error(), Meta: map[string]string{"driver": drv.Name(), "stage": stage}})
		if err := taskcompat.Move(r.Store, t, task.Failed); err == nil {
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
	if _, isNone := sb.(sandbox.None); isNone {
		if configured, ok, configErr := sandbox.FromAgent(man); configErr != nil {
			return fail("sandbox", configErr)
		} else if ok {
			sb = configured
		}
	}
	// The run's environment is handed to the sandbox, not to each command: a
	// sandbox that moves execution elsewhere has to carry the credential there
	// without ever making it an argument of a host process.
	env, release, err := sb.Prepare(ctx, wt.Path, append([]string{"TRILHA_TASK=" + id}, driver.CredentialEnv(r.AI)...))
	if err != nil {
		return fail("sandbox", err)
	}
	defer release()

	maxAttempts := r.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = fieldInt(t.Fields.Get("max_attempts"), 0)
	}
	if maxAttempts <= 0 && man != nil {
		maxAttempts = fieldInt(man.Fields.Get("max_attempts"), 0)
	}
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	loop := attemptLoop{
		runner: r, ctx: ctx, item: t, manifest: man, driver: drv, manager: wm, tree: wt,
		dir: env.Dir, env: env.Env, prefix: env.Prefix, prompt: pack.Markdown(), checks: append(append([]string(nil), t.Checks...), proj.Verify...), ai: r.AI,
		maxAttempts: maxAttempts, result: res, started: start, fail: fail,
	}
	return loop.run()
}

func (r *Runner) moveRunning(item *task.Task) error {
	blocked, err := (queue.Local{Store: r.Store, Repositories: r.Repositories}).Blocker(item)
	if err != nil {
		return err
	}
	if blocked != "" {
		return (&queue.BlockedError{Task: item.ID, Reason: blocked})
	}
	return taskcompat.Move(r.Store, item, task.Running)
}

// Next runs the first executable task, or answers ErrNothing.
func (r *Runner) Next(ctx context.Context) (*Result, error) {
	item, err := (queue.Local{Store: r.Store, Repositories: r.Repositories}).Next(ctx)
	if errors.Is(err, queue.ErrEmpty) {
		return nil, ErrNothing
	}
	if err != nil {
		return nil, err
	}
	return r.Run(ctx, item.TaskID)
}

// ErrNothing is a graph with no executable task.
var ErrNothing = errors.New("runner: no task is ready with every dependency done")

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
