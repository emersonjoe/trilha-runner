// Package queue is where the next task comes from. Local reads the graph in
// .trilha/ — the protocol is its own queue for one machine — and Remote asks
// trilha-cloud, which is how a fleet shares work. Both answer the same Item,
// so the runner does not know which one it is talking to.
package queue

import (
	"context"
	"errors"
	"strings"

	"github.com/emersonjoe/trilha-runner/driver"
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

// Local is the dependency graph of the store: the first ready task with every
// dependency done. Dependencies in other repositories count too — a task that
// waits for `trilha:TASK-004` is not offered until that task is done — and who
// answers for an alias is the store's `Remote`, so `next` and the run that
// follows it cannot disagree about whether a task may start.
type Local struct{ Store *task.Store }

// Waiting is one task the queue did not offer, and why.
type Waiting struct {
	Task string `json:"task"`
	// Reason names each dependency that is not done, as the protocol writes
	// it: `trilha:TASK-004` for one that is open, `waiting:trilha:TASK-004`
	// for one nobody could answer for.
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
	g, err := l.Store.Graph()
	if err != nil {
		return Item{}, nil, err
	}
	if t := g.Next(); t != nil {
		return Item{TaskID: t.ID}, waitingOn(g, tasks), nil
	}
	return Item{}, waitingOn(g, tasks), ErrEmpty
}

// Waiting answers every ready task held back by a dependency.
func (l Local) Waiting(ctx context.Context) ([]Waiting, error) {
	tasks, err := l.Store.List()
	if err != nil {
		return nil, err
	}
	g, err := l.Store.Graph()
	if err != nil {
		return nil, err
	}
	return waitingOn(g, tasks), nil
}

// waitingOn reports the ready tasks the graph would not offer. Only ready
// ones: a task that is still an idea is not waiting on anything, it is
// unwritten.
func waitingOn(g *task.Graph, tasks []*task.Task) []Waiting {
	var out []Waiting
	for _, t := range tasks {
		if t.Status != task.Ready {
			continue
		}
		if open := g.Blockers(t.ID); len(open) > 0 {
			out = append(out, Waiting{Task: t.ID, Reason: strings.Join(open, ", ")})
		}
	}
	return out
}

// Done is a no-op: the runner already moved the task and wrote the evidence.
func (Local) Done(ctx context.Context, item Item, res Result) error { return nil }
