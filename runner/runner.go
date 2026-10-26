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
	"path/filepath"
	"strings"
	"time"

	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-runner/internal/taskcompat"
	"github.com/emersonjoe/trilha-runner/queue"
	"github.com/emersonjoe/trilha-runner/sandbox"
	"github.com/emersonjoe/trilha-runner/worktree"
	"github.com/emersonjoe/trilha-spec/agent"
	"github.com/emersonjoe/trilha-spec/execution"
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
	// Progress receives structured phase changes; nil discards them.
	Progress func(Event)
	// MaxAttempts includes the first attempt. Default: 3.
	MaxAttempts int
	// RetryBase is the first exponential-backoff delay. Default: 1 second.
	RetryBase time.Duration
	// Self is the trilha-runner binary. When set, the agent finds it on its
	// PATH (ask, note, overview, attention) and claude-code's hooks call it.
	Self string
	// RunID is the control plane's id for this run, exported to the agent as
	// TRILHA_RUN_ID; the task id when empty.
	RunID string
	// Continuation is an operator's answer to the question an earlier run
	// asked: the same claude-code session resumes with it, or a fresh session
	// reads it, quoted, in the context pack.
	Continuation *execution.Continuation
	// TaskContext is the task's memory (overview, recent notes), quoted into
	// the context pack.
	TaskContext *execution.TaskContext
	// AgentStateInterval bounds agent-state reports. Default: 2 seconds.
	AgentStateInterval time.Duration
	// Work is what the control plane asked this run for beyond the task: a
	// review, a hunt, a variant, review comments, foreign code.
	Work Work
}

// Event is a structured progress notification about a run's phase, emitted
// alongside (never parsed from) Log lines so a caller — a worker reporting
// to trilha-cloud — can follow a run without scraping text.
type Event struct {
	Phase       string
	Message     string
	Attempt     int
	MaxAttempts int
	Branch      string
	Model       string
	// AttemptStatus, when set, closes out an attempt ("passed" or "failed").
	AttemptStatus string
	FailureClass  string
	ErrorCode     string
	// AgentState, when set, is what the agent is doing now; Attention raises
	// or clears the badge that says a person is needed.
	AgentState execution.AgentState
	Attention  *execution.Attention
}

// Attempt is one provider or corrective execution inside a logical run.
type Attempt struct {
	Number        int     `json:"number"`
	Status        string  `json:"status"`
	FailureClass  string  `json:"failure_class,omitempty"`
	ErrorCode     string  `json:"error_code,omitempty"`
	RepairReason  string  `json:"repair_reason,omitempty"`
	Model         string  `json:"model,omitempty"`
	TotalTokens   int     `json:"total_tokens,omitempty"`
	EstimatedCost float64 `json:"estimated_cost,omitempty"`
	// The cost of the attempt under the names a `run` evidence record uses
	// (protocol §5 and §10).
	Provider  string    `json:"provider,omitempty"`
	TokensIn  int       `json:"tokens_in,omitempty"`
	TokensOut int       `json:"tokens_out,omitempty"`
	Cost      float64   `json:"cost,omitempty"`
	Currency  string    `json:"currency,omitempty"`
	Started   time.Time `json:"started"`
	Finished  time.Time `json:"finished"`
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
	// ErrorCode is the machine-readable detail next to FailureClass, for
	// example environment_missing_tool.
	ErrorCode    string   `json:"error_code,omitempty"`
	FilesChanged []string `json:"files_changed,omitempty"`
	DiffSummary  string   `json:"diff_summary,omitempty"`
	AgentReport  string   `json:"agent_report,omitempty"`
	// Question is set when the agent stopped to ask: the run needs input, the
	// task is blocked locally, and nothing failed.
	Question  *execution.Question  `json:"question,omitempty"`
	Session   *execution.Session   `json:"session,omitempty"`
	Overview  string               `json:"overview,omitempty"`
	Notes     []execution.Note     `json:"notes,omitempty"`
	DiffStats *execution.DiffStats `json:"diff_stats,omitempty"`
	// Review is a review run's verdict and findings; Findings a hunt's;
	// ReviewReplies the answers to a repair run's review comments;
	// Artifacts the reports the agent published, copied under the run's
	// directory for the worker to upload.
	Review        *execution.ReviewVerdict `json:"review,omitempty"`
	Findings      []execution.Finding      `json:"findings,omitempty"`
	ReviewReplies []execution.ReviewReply  `json:"review_replies,omitempty"`
	Artifacts     []Artifact               `json:"artifacts,omitempty"`
	// ReadOnly is a hunt or a report-mode review: Branch is the branch it
	// read, Commit the commit it read, and there is nothing to push.
	ReadOnly bool `json:"read_only,omitempty"`
}

// NeedsInput answers whether the run ended with a question for a person.
func (r *Result) NeedsInput() bool { return r != nil && r.Question != nil }

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

func (r *Runner) progress(e Event) {
	if r.Progress != nil {
		r.Progress(e)
	}
}

// Run executes one task. The task must be ready with every dependency
// done; the runner moves it to running, and at the end to review (checks
// passed) or failed. A run that cannot even start — an unknown driver, a
// project that is not a git repository — still ends failed, with a run
// record naming the stage. A task that is not ready, or is blocked by a
// dependency, is not run and is left where it was.
func (r *Runner) Run(ctx context.Context, id string) (*Result, error) {
	start := time.Now()
	t, err := taskcompat.Get(r.Store, id)
	if err != nil {
		return nil, err
	}
	work := r.Work
	readOnly := work.readOnly()
	if !work.Kind.Valid() {
		return nil, fmt.Errorf("run kind %q is not implement, review or hunt", work.Kind)
	}
	// A read-only run reads the task and its branch and moves nothing: the
	// task stays where it is, whatever its status.
	if !readOnly {
		r.resumeOrphan(t)
		if t.Status == task.Blocked && r.Continuation != nil {
			// The task stopped here to ask; the answer has come.
			if err := taskcompat.Move(r.Store, t, task.Ready); err == nil {
				t.Status = task.Ready
			}
		}
		if t.Status == task.Review && (work.review() || len(work.ReviewComments) > 0) {
			// A fix-mode review or a repair run starts from review.
			if err := taskcompat.Move(r.Store, t, task.Ready); err == nil {
				t.Status = task.Ready
			}
		}
		if t.Status != task.Ready {
			return nil, fmt.Errorf("task %s is %s, not ready", id, t.Status)
		}
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
	pack, err := buildContextPack(r.Layout, t, proj, man)
	if err != nil {
		return nil, err
	}
	memory := r.TaskContext
	if work.VariantIndex > 0 {
		// A variant reads nothing a sibling left: no earlier evidence of the
		// task, no note or overview an agent wrote.
		pack.Evidence = nil
		memory = variantMemory(memory)
	}
	if !readOnly {
		if err := r.moveRunning(t); err != nil {
			return nil, err
		}
	}
	defer r.markRunning(id)()
	res := &Result{ProtocolVersion: "trilha.execution/v1", Task: id, ReadOnly: readOnly}
	fail := func(stage string, cause error) (*Result, error) {
		note := cause.Error()
		if !strings.HasPrefix(note, stage+": ") {
			note = stage + ": " + note
		}
		r.logf("%s", note)
		meta := map[string]string{"driver": res.Driver, "stage": stage}
		if stage == failurePolicy {
			res.FailureClass = failurePolicy
			meta["failure_class"] = failurePolicy
			if res.ErrorCode != "" {
				meta["error_code"] = res.ErrorCode
			}
		}
		task.Record(r.Layout, task.Evidence{Task: id, Kind: "run", By: r.By, Note: note, Meta: meta})
		if readOnly {
			res.Status = task.Failed
		} else if err := taskcompat.Move(r.Store, t, task.Failed); err == nil {
			res.Status = task.Failed
		}
		res.Elapsed = time.Since(start)
		return res, cause
	}

	drv := r.Driver
	if drv == nil {
		name := "exec"
		if man != nil && man.Driver != "" {
			name = man.Driver
		}
		res.Driver = name
		if drv, err = driver.New(name); err != nil {
			return fail("driver", err)
		}
	}
	res.Driver = drv.Name()
	wm := worktree.Manager{Layout: r.Layout}
	if err := wm.IsRepo(ctx); err != nil {
		return fail("worktree", err)
	}
	checks := append(append([]string(nil), t.Checks...), proj.Verify...)
	if work.ForeignCode {
		// The fork's branch never chooses what the worker runs.
		verify, err := baseVerify(ctx, r.Layout, wm, work.BaseBranch)
		if err != nil {
			res.ErrorCode = errorSandboxRequired
			return fail(failurePolicy, err)
		}
		checks = append(append([]string(nil), t.Checks...), verify...)
	}

	target := r.target(id)
	wt, err := wm.Open(ctx, target)
	if err != nil {
		return fail("worktree", err)
	}
	res.Branch, res.Worktree = wt.Branch, wt.Path
	r.logf("worktree %s on %s", wt.Path, wt.Branch)
	r.progress(Event{Phase: "creating_worktree", Branch: wt.Branch})

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
	if docker, ok := sb.(sandbox.Docker); ok && readOnly {
		// A read-only run cannot write the worktree even if it tries.
		docker.ReadOnly = true
		sb = docker
	}
	if work.ForeignCode && sb.Name() != "docker" {
		res.ErrorCode = errorSandboxRequired
		return fail(failurePolicy, errors.New("foreign code (a branch from a fork) runs only in the Docker sandbox: declare `sandbox: {type: docker, image: …}` in the agent manifest"))
	}
	// The run's environment is handed to the sandbox, not to each command: a
	// sandbox that moves execution elsewhere has to carry the credential there
	// without ever making it an argument of a host process. On foreign code
	// the credential goes only to a driver that needs it in its environment
	// to call the model; the ai driver holds it in process.
	runEnv := []string{"TRILHA_TASK=" + id}
	if !work.ForeignCode || drv.Name() != "ai" {
		runEnv = append(runEnv, driver.CredentialEnv(r.AI)...)
	}
	env, release, err := sb.Prepare(ctx, wt.Path, runEnv)
	if err != nil {
		if work.ForeignCode {
			res.ErrorCode = errorSandboxRequired
			return fail(failurePolicy, fmt.Errorf("foreign code needs the Docker sandbox, which is unavailable: %w", err))
		}
		return fail("sandbox", err)
	}
	defer release()

	runDir := wm.RunDir(target.Key)
	session, closeLink := r.openAgentLink(id, runDir, &env.Env, len(env.Prefix) == 0)
	session.report.artifactDir = filepath.Join(runDir, "artifacts")
	session.report.sources = []string{wt.Path, filepath.Join(runDir, "scratch")}
	for _, comment := range work.ReviewComments {
		session.report.comments = append(session.report.comments, comment.ID)
	}
	var fresh string
	switch {
	case readOnly && work.Kind == execution.KindHunt:
		seed := execution.HuntSeed{}
		if work.HuntSeed != nil {
			seed = *work.HuntSeed
		}
		fresh = huntPrompt(scopeOf(ctx, wm, wt, work.BaseBranch), seed) + "\n---\n\n" + pack.Markdown()
	case work.review():
		fresh = reviewPrompt(scopeOf(ctx, wm, wt, work.BaseBranch), work.ReviewOf, work.ReviewMode) + "\n---\n\n" + pack.Markdown()
		if !readOnly {
			fresh += agentSections(memory, nil)
		}
	default:
		fresh = pack.Markdown() + reviewsBlock(wt.Path, work.ReviewComments) + smallChangeParagraph + agentSections(memory, r.Continuation)
	}
	// The same claude-code conversation resumes with the answer alone; any
	// other session — another driver, a corrective attempt, a resume that
	// fails — starts fresh with the question and answer quoted in the pack.
	resume := ""
	if c := r.Continuation; c != nil && !readOnly && c.Session != nil && c.Session.Driver == drv.Name() && drv.Name() == "claude-code" && driver.ValidSessionID(c.Session.SessionID) {
		resume = c.Session.SessionID
	}
	self := ""
	if len(env.Prefix) == 0 {
		self = r.Self
	}

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
		dir: env.Dir, env: env.Env, prefix: env.Prefix, prompt: fresh, checks: checks, ai: r.AI,
		maxAttempts: maxAttempts, result: res, started: start, fail: fail,
		agent: session, runDir: runDir, self: self, resume: resume, resumePrompt: resumePrompt(r.Continuation, resume),
		readOnly: readOnly,
	}
	result, runErr := loop.run()
	closeLink()
	r.finishAgent(ctx, result, session, wm, wt, drv.Name())
	r.attachReport(result, session)
	return result, runErr
}

func resumePrompt(continuation *execution.Continuation, resume string) string {
	if resume == "" || continuation == nil {
		return ""
	}
	return continuationPrompt(continuation)
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
