// Package queue is where the next task comes from. Local reads the graph in
// .trilha/ — the protocol is its own queue for one machine — and Remote asks
// trilha-cloud, which is how a fleet shares work. Both answer the same Item,
// so the runner does not know which one it is talking to.
package queue

import (
	"context"
	"errors"

	"github.com/emersonjoe/trilha-spec/task"
)

// Item is one unit of work handed to a runner.
type Item struct {
	// ID is the queue's own id; "" for the local queue.
	ID string `json:"id,omitempty"`
	// Project names the project on the remote side.
	Project string `json:"project,omitempty"`
	TaskID  string `json:"task_id"`
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

// Queue hands out work and takes back results.
type Queue interface {
	Next(ctx context.Context) (Item, error)
	Done(ctx context.Context, item Item, res Result) error
}

// Local is the dependency graph of the store: the first ready task with
// every dependency done.
type Local struct{ Store *task.Store }

// Next answers the first executable task.
func (l Local) Next(ctx context.Context) (Item, error) {
	g, err := l.Store.Graph()
	if err != nil {
		return Item{}, err
	}
	t := g.Next()
	if t == nil {
		return Item{}, ErrEmpty
	}
	return Item{TaskID: t.ID}, nil
}

// Done is a no-op: the runner already moved the task and wrote the evidence.
func (Local) Done(ctx context.Context, item Item, res Result) error { return nil }
