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
// done. The protocol spells it `<alias>:TASK-NNN`, and the alias is resolved
// by the runner, never guessed — it is the operator who says which checkout
// an alias means (`--repo trilha=../trilha`) or the control plane that knows.
//
// Until the protocol accepts an alias inside `depends_on` (trilha-spec #11),
// a task declares them under `depends_on_remote`, which the protocol keeps as
// an unknown field and round-trips untouched:
//
//	---
//	id: TASK-005
//	depends_on_remote:
//	  - trilha:TASK-004
//	---
//
// Both forms are read, so a checkout works before and after that change.

// RemoteDependency is one dependency in another repository.
type RemoteDependency struct {
	Alias string
	Task  string
}

// String is the protocol's spelling: alias:TASK-NNN.
func (d RemoteDependency) String() string { return d.Alias + ":" + d.Task }

// RemoteField is the transitional front matter key, read alongside the
// protocol's own `depends_on`.
const RemoteField = "depends_on_remote"

// ParseDependency reads `alias:TASK-NNN`. A plain task id is local and
// answers false.
func ParseDependency(value string) (RemoteDependency, bool) {
	alias, id, found := strings.Cut(strings.TrimSpace(value), ":")
	if !found {
		return RemoteDependency{}, false
	}
	alias, id = strings.TrimSpace(alias), strings.TrimSpace(id)
	if alias == "" || !task.ValidID(id) {
		return RemoteDependency{}, false
	}
	return RemoteDependency{Alias: alias, Task: id}, true
}

// RemoteDependencies answers a task's cross-repository dependencies, from
// either spelling, without duplicates and in a stable order.
func RemoteDependencies(t *task.Task) []RemoteDependency {
	seen := map[string]bool{}
	var out []RemoteDependency
	add := func(value string) {
		if d, ok := ParseDependency(value); ok && !seen[d.String()] {
			seen[d.String()] = true
			out = append(out, d)
		}
	}
	for _, value := range t.DependsOn {
		add(value)
	}
	for _, value := range t.Fields.GetList(RemoteField) {
		add(value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// LocalDependencies answers the dependencies inside this repository — what
// the protocol's own graph can reason about.
func LocalDependencies(t *task.Task) []string {
	var out []string
	for _, value := range t.DependsOn {
		if _, remote := ParseDependency(value); !remote {
			out = append(out, value)
		}
	}
	return out
}

// ErrUnknownAlias is an alias nobody told the runner how to resolve.
var ErrUnknownAlias = errors.New("queue: unknown repository alias")

// Resolver answers the status of a task in another repository.
type Resolver interface {
	// Status answers the dependency's status, or an error when it cannot be
	// resolved. An unresolvable dependency blocks: the runner never assumes
	// a task it cannot see is done.
	Status(ctx context.Context, dep RemoteDependency) (task.Status, error)
}

// Checkouts resolves aliases against sibling checkouts on this machine,
// through the protocol's own store: `--repo trilha=../trilha`.
type Checkouts map[string]string

// Status opens the sibling checkout and reads the task.
func (c Checkouts) Status(ctx context.Context, dep RemoteDependency) (task.Status, error) {
	dir, ok := c[dep.Alias]
	if !ok {
		return "", fmt.Errorf("%w: %q (have %s)", ErrUnknownAlias, dep.Alias, strings.Join(c.aliases(), ", "))
	}
	store, err := task.Open(dir)
	if err != nil {
		return "", fmt.Errorf("queue: %s: %w", dep.Alias, err)
	}
	t, err := store.Get(dep.Task)
	if err != nil {
		return "", fmt.Errorf("queue: %s: %w", dep.Alias, err)
	}
	return t.Status, nil
}

func (c Checkouts) aliases() []string {
	out := make([]string, 0, len(c))
	for alias := range c {
		out = append(out, alias)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return []string{"none: pass --repo alias=path"}
	}
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
// account:
//
//	GET /api/projects/{alias}/tasks/{id}   → 200 {"status":"done"} | 404
//
// A 404 is unresolvable, not done: the runner blocks rather than guess.
func (r Remote) Status(ctx context.Context, dep RemoteDependency) (task.Status, error) {
	resp, err := r.do(ctx, http.MethodGet, "/api/projects/"+dep.Alias+"/tasks/"+dep.Task, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("%w: %s", ErrUnknownAlias, dep)
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("queue: %s: %s: %s", dep, resp.Status, strings.TrimSpace(string(body)))
	}
	var answer struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return "", err
	}
	status := task.Status(strings.TrimSpace(answer.Status))
	if !status.Valid() {
		return "", fmt.Errorf("queue: %s: %q is not a status", dep, answer.Status)
	}
	return status, nil
}
