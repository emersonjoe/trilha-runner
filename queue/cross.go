package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/emersonjoe/trilha-spec/task"
)

// A cross-repository dependency is a task in one repository that waits for a
// task in another: a product task that cannot start until a framework task is
// done. The protocol spells it `<alias>:TASK-NNN` inside `depends_on`, keeps
// it out of this repository's topological order, and refuses to start a task
// whose alias nobody answered for:
//
//	---
//	id: TASK-005
//	depends_on:
//	  - TASK-004          # this repository
//	  - trilha:TASK-004   # another one
//	---
//
// What the protocol deliberately does not do is decide what an alias means —
// that is a machine's business, not a document's. This file is the two ways a
// runner answers: sibling checkouts on disk, and the control plane.

// ErrUnknownAlias is an alias nobody told the runner how to resolve.
var ErrUnknownAlias = errors.New("queue: unknown repository alias")

// Checkouts resolves aliases against sibling checkouts on this machine,
// through the protocol's own store: `--repo trilha=../trilha`.
//
// It answers false — not "done" — for anything it cannot read: an alias that
// was never declared, a directory that is not a Trilha project, a task that is
// not there. The protocol turns that into `waiting:<alias>:TASK-NNN`, because
// a dependency nobody can see is not a dependency anybody has met.
type Checkouts map[string]string

// Status implements task.Resolver.
func (c Checkouts) Status(alias, id string) (task.Status, bool) {
	dir, ok := c[alias]
	if !ok {
		return "", false
	}
	store, err := task.Open(dir)
	if err != nil {
		return "", false
	}
	t, err := store.Get(id)
	if err != nil {
		return "", false
	}
	return t.Status, true
}

// Aliases answers the aliases declared, sorted.
func (c Checkouts) Aliases() []string {
	out := make([]string, 0, len(c))
	for alias := range c {
		out = append(out, alias)
	}
	sort.Strings(out)
	return out
}

// ParseRepo reads one --repo alias=path.
func ParseRepo(value string) (string, string, error) {
	alias, dir, found := strings.Cut(value, "=")
	alias, dir = strings.TrimSpace(alias), strings.TrimSpace(dir)
	if !found || alias == "" || dir == "" {
		return "", "", fmt.Errorf("queue: --repo wants alias=path, got %q", value)
	}
	if strings.ContainsRune(alias, ':') {
		return "", "", fmt.Errorf("queue: alias %q must not contain a colon", alias)
	}
	return alias, dir, nil
}

// Status asks the control plane for a task in another project of the same
// account, so a worker resolves an alias without a checkout of it:
//
//	GET /api/projects/{alias}/tasks/{id}   → 200 {"status":"done"} | 404
//
// It implements task.Resolver, so anything it cannot answer — a 404, a
// transport error, a status the protocol does not know — leaves the
// dependency waiting rather than passing.
func (r Remote) Status(alias, id string) (task.Status, bool) {
	resp, err := r.do(context.Background(), http.MethodGet, "/api/projects/"+alias+"/tasks/"+id, nil)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return "", false
	}
	var answer struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return "", false
	}
	status := task.Status(strings.TrimSpace(answer.Status))
	if !status.Valid() {
		return "", false
	}
	return status, true
}
