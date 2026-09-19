// Package queue is where the next task comes from. Local reads the graph in
// .trilha/ — the protocol is its own queue for one machine — and Remote asks
// trilha-cloud, which is how a fleet shares work. Both answer the same Item,
// so the runner does not know which one it is talking to.
package queue

import (
	"context"
	"errors"

	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-spec/spec"
	"github.com/emersonjoe/trilha-spec/task"
)

// Item is one unit of work handed to a runner.
type Item struct {
	// ID is the queue's own id; "" for the local queue.
	ID string `json:"id,omitempty"`
	// Project names the project on the remote side.
	Project string `json:"project,omitempty"`
	TaskID  string `json:"task_id"`
	// Requires are the worker labels this run needs — `docker`, `region:br`.
	// The control plane filters on them; the worker checks them again,
	// because a claim it cannot honour must not become a silent failure.
	Requires []string `json:"requires,omitempty"`
	// AI is the project's model access, sealed by the control plane and
	// disclosed only on the claim, the way DeploymentWork.Secrets already
	// travels. The worker never persists it: it reaches the driver's process
	// environment and nowhere else, and it is redacted from captured output.
	AI *driver.Access `json:"ai,omitempty"`
}

// Capabilities is what a worker says it can do, how much it can take and
// where it is. A fleet with more than one kind of host — one without
// Docker, one with a GPU, one in a region a project's data may not leave —
// is only routable if each host declares itself, so the same payload rides
// the heartbeat and the claim.
type Capabilities struct {
	// Labels are free-form capabilities: `docker`, `gpu`, `region:br`.
	Labels []string `json:"labels,omitempty"`
	// Capacity is how many runs this worker executes at once (at least 1).
	Capacity int `json:"capacity"`
	// Running is how many it is executing right now.
	Running int `json:"running"`
	// Runner is the trilha-runner version; Drivers are the drivers it has.
	Runner  string   `json:"runner,omitempty"`
	Drivers []string `json:"drivers,omitempty"`
}

// Meets answers whether the worker's labels cover every label a run
// requires, and names the ones missing.
func (c Capabilities) Meets(requires []string) (bool, []string) {
	have := make(map[string]bool, len(c.Labels))
	for _, l := range c.Labels {
		have[l] = true
	}
	var missing []string
	for _, need := range requires {
		if !have[need] {
			missing = append(missing, need)
		}
	}
	return len(missing) == 0, missing
}

// Bundle is the versioned Trilha Spec context returned by the control plane.
// The runner materializes it in its local checkout; source code never travels
// in this payload.
type Bundle struct {
	Version       int           `json:"version"`
	RunID         string        `json:"run_id"`
	Project       string        `json:"project"`
	Repository    string        `json:"repository,omitempty"`
	DefaultBranch string        `json:"default_branch"`
	Specification Specification `json:"specification"`
	Round         Round         `json:"round"`
	Task          WorkTask      `json:"task"`
}

type Specification struct {
	ID          string `json:"id"`
	ProtocolID  string `json:"protocol_id,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Version     int    `json:"version"`
}

type Round struct {
	ID     string  `json:"id"`
	Number int     `json:"number"`
	Goal   string  `json:"goal,omitempty"`
	Stages []Stage `json:"stages"`
}

type Stage struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Position int        `json:"position"`
	Tasks    []WorkTask `json:"tasks"`
}

type WorkTask struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	Agent      string   `json:"agent,omitempty"`
	DependsOn  []string `json:"depends_on,omitempty"`
	Acceptance []string `json:"acceptance,omitempty"`
	Checks     []string `json:"checks,omitempty"`
}

// Result is what a runner reports back.
type Result struct {
	Passed   bool            `json:"passed"`
	Status   string          `json:"status"`
	Branch   string          `json:"branch,omitempty"`
	Commit   string          `json:"commit,omitempty"`
	Evidence []task.Evidence `json:"evidence,omitempty"`
	Log      string          `json:"log,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// DeploymentWork is a claimed operation. Secrets are disclosed only on the
// claim response to a project-bound runner and must never be logged.
type DeploymentWork struct {
	Deployment Deployment        `json:"deployment"`
	Secrets    map[string]string `json:"secrets,omitempty"`
}

type Deployment struct {
	ID          string `json:"id"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Profile     string `json:"profile"`
	Action      string `json:"action"`
	Revision    string `json:"revision"`
}

type DeploymentResult struct {
	Worker  string `json:"worker"`
	Project string `json:"project"`
	Passed  bool   `json:"passed"`
	Log     string `json:"log,omitempty"`
	Health  string `json:"health,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ErrEmpty is a queue with nothing to hand out right now.
var ErrEmpty = errors.New("queue: nothing to run")

// ErrUnmet is a run handed to a worker that does not have what it requires.
// The run was claimed, so it is reported back rather than dropped.
var ErrUnmet = errors.New("queue: run requires labels this worker does not have")

// Queue hands out work and takes back results.
type Queue interface {
	Next(ctx context.Context) (Item, error)
	Done(ctx context.Context, item Item, res Result) error
}

// Local is the dependency graph of the store: the first ready task with
// every dependency done. When a Resolver is set, a dependency in another
// repository counts too — a task that waits for `trilha:TASK-004` is not
// offered until that task is done, and the reason is visible rather than a
// silent skip.
type Local struct {
	Store *task.Store
	// Resolver answers the status of a cross-repository dependency. Without
	// one, a task that has any is left alone: the runner does not assume a
	// dependency it cannot see is done.
	Resolver Resolver
	// By is recorded as the author of the evidence a block leaves.
	By string
}

// Waiting is one task the queue did not offer, and why.
type Waiting struct {
	Task string `json:"task"`
	// Reason is `waiting:<alias>:TASK-NNN` for a dependency that is not done,
	// with what went wrong appended when it could not be resolved at all.
	Reason string `json:"reason"`
}

func (w Waiting) String() string { return w.Task + ": " + w.Reason }

// Next answers the first executable task.
func (l Local) Next(ctx context.Context) (Item, error) {
	item, _, err := l.NextWaiting(ctx)
	return item, err
}

// NextWaiting is Next with the tasks it passed over and why, so a caller can
// say what the agenda is waiting for instead of "nothing to run".
func (l Local) NextWaiting(ctx context.Context) (Item, []Waiting, error) {
	tasks, err := l.Store.List()
	if err != nil {
		return Item{}, nil, err
	}
	// The protocol's graph reasons about this repository; a dependency in
	// another one is resolved beside it.
	g, err := task.NewGraph(localized(tasks))
	if err != nil {
		return Item{}, nil, err
	}
	var waiting []Waiting
	for _, t := range candidates(g, tasks) {
		reason, err := l.blockedBy(ctx, t)
		if err != nil {
			return Item{}, waiting, err
		}
		if reason != "" {
			waiting = append(waiting, Waiting{Task: t.ID, Reason: reason})
			if err := l.block(t, reason); err != nil {
				return Item{}, waiting, err
			}
			continue
		}
		if t.Status == task.Blocked {
			// Everything it waited for is done: the runner unblocks what the
			// runner blocked, so the agenda never strands on its own.
			if _, err := l.Store.Move(t.ID, task.Ready); err != nil {
				return Item{}, waiting, err
			}
			l.note(t.ID, "unblocked: every cross-repository dependency is done")
		}
		return Item{TaskID: t.ID}, waiting, nil
	}
	return Item{}, waiting, ErrEmpty
}

// Waiting answers every task held back by a cross-repository dependency.
func (l Local) Waiting(ctx context.Context) ([]Waiting, error) {
	_, waiting, err := l.NextWaiting(ctx)
	if err != nil && !errors.Is(err, ErrEmpty) {
		return waiting, err
	}
	return waiting, nil
}

// candidates are the tasks the local graph says could run now — ready with
// every local dependency done — plus the ones this queue blocked earlier and
// may now be able to release.
func candidates(g *task.Graph, tasks []*task.Task) []*task.Task {
	ready := g.Ready()
	byID := make(map[string]bool, len(ready))
	out := make([]*task.Task, 0, len(ready))
	for _, t := range ready {
		byID[t.ID] = true
		out = append(out, t)
	}
	for _, t := range tasks {
		if t.Status != task.Blocked || byID[t.ID] || len(RemoteDependencies(t)) == 0 {
			continue
		}
		if len(g.Blockers(t.ID)) == 0 {
			out = append(out, t)
		}
	}
	return out
}

// blockedBy answers why a task cannot run, or "" when nothing holds it.
func (l Local) blockedBy(ctx context.Context, t *task.Task) (string, error) {
	deps := RemoteDependencies(t)
	if len(deps) == 0 {
		return "", nil
	}
	if l.Resolver == nil {
		return "waiting:" + deps[0].String() + " (no resolver: pass --repo " + deps[0].Alias + "=<path>)", nil
	}
	for _, dep := range deps {
		status, err := l.Resolver.Status(ctx, dep)
		if err != nil {
			return "waiting:" + dep.String() + " (" + err.Error() + ")", nil
		}
		if status != task.Done {
			return "waiting:" + dep.String() + " (" + string(status) + ")", nil
		}
	}
	return "", nil
}

// block moves a ready task out of the way and records why. A task already
// blocked stays put; the note is only written when the reason changes, so a
// poll every ten seconds does not fill the evidence directory.
func (l Local) block(t *task.Task, reason string) error {
	if t.Status == task.Blocked {
		if last, _ := lastWaiting(l.Store.Layout, t.ID); last == reason {
			return nil
		}
		l.note(t.ID, reason)
		return nil
	}
	if _, err := l.Store.Move(t.ID, task.Blocked); err != nil {
		return err
	}
	l.note(t.ID, reason)
	return nil
}

func (l Local) note(id, reason string) {
	by := l.By
	if by == "" {
		by = "trilha-runner queue"
	}
	task.Record(l.Store.Layout, task.Evidence{
		Task: id, Kind: "note", By: by, Note: reason,
		Meta: map[string]string{"queue": "cross-repository", "waiting": reason},
	})
}

// lastWaiting answers the reason the queue recorded last for a task.
func lastWaiting(layout spec.Layout, id string) (string, bool) {
	records, err := task.ListEvidence(layout, id)
	if err != nil {
		return "", false
	}
	for i := len(records) - 1; i >= 0; i-- {
		if r := records[i]; r.Meta["queue"] == "cross-repository" {
			return r.Meta["waiting"], true
		}
	}
	return "", false
}

// localized copies the tasks with only the dependencies this repository can
// resolve, so the protocol's graph is not asked about another repository.
func localized(tasks []*task.Task) []*task.Task {
	out := make([]*task.Task, 0, len(tasks))
	for _, t := range tasks {
		copied := *t
		copied.DependsOn = LocalDependencies(t)
		out = append(out, &copied)
	}
	return out
}

// Done is a no-op: the runner already moved the task and wrote the evidence.
func (Local) Done(ctx context.Context, item Item, res Result) error { return nil }
