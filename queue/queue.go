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
