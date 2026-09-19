package queue

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-spec/spec"
	"github.com/emersonjoe/trilha-spec/task"
)

func TestParseRepo(t *testing.T) {
	alias, dir, err := ParseRepo("trilha=../trilha")
	if err != nil || alias != "trilha" || dir != "../trilha" {
		t.Fatalf("%q %q %v", alias, dir, err)
	}
	for _, bad := range []string{"trilha", "=path", "alias=", "a:b=path"} {
		if _, _, err := ParseRepo(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// checkout writes a repository with the given tasks: id → front matter body.
func checkout(t *testing.T, tasks map[string]string) *task.Store {
	t.Helper()
	dir := t.TempDir()
	layout, _, err := spec.Init(dir, spec.InitOptions{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	for id, body := range tasks {
		if err := os.WriteFile(filepath.Join(layout.Tasks(), id+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &task.Store{Layout: layout}
}

// ready writes a ready task whose dependencies may name another repository,
// the way the protocol spells it: `depends_on: [trilha:TASK-004]`.
func ready(id, title string, deps ...string) string {
	body := "---\nid: " + id + "\ntitle: " + title + "\nstatus: ready\nacceptance:\n  - it works\n"
	if len(deps) > 0 {
		body += "depends_on:\n"
		for _, d := range deps {
			body += "  - " + d + "\n"
		}
	}
	return body + "---\n"
}

// finish walks a task to done through the protocol's transitions.
func finish(t *testing.T, store *task.Store, id string) {
	t.Helper()
	for _, to := range []task.Status{task.Running, task.Verify, task.Review, task.Done} {
		if _, err := store.Move(id, to); err != nil {
			t.Fatalf("move %s to %s: %v", id, to, err)
		}
	}
}

// A task file that names another repository parses and keeps the alias: the
// protocol accepts it, so the runner needs no field of its own.
func TestProtocolAcceptsAnAliasInDependsOn(t *testing.T) {
	tk, err := task.Parse([]byte(ready("TASK-005", "Product work", "trilha:TASK-004")))
	if err != nil {
		t.Fatalf("the protocol refused the alias: %v", err)
	}
	if len(tk.DependsOn) != 1 || tk.DependsOn[0] != "trilha:TASK-004" {
		t.Fatalf("depends_on = %v", tk.DependsOn)
	}
}

// A task waiting on another repository is not offered, and the reason names
// the dependency. Once it is done, the same queue offers the task.
func TestLocalHoldsACrossRepositoryDependency(t *testing.T) {
	framework := checkout(t, map[string]string{"TASK-004": ready("TASK-004", "Framework work")})
	product := checkout(t, map[string]string{"TASK-005": ready("TASK-005", "Product work", "trilha:TASK-004")})

	product.Remote = Checkouts{"trilha": framework.Layout.Root}
	q := Local{Store: product}
	item, waiting, err := q.NextWaiting(context.Background())
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("%+v %v", item, err)
	}
	if len(waiting) != 1 || waiting[0].Task != "TASK-005" || waiting[0].Reason != "trilha:TASK-004" {
		t.Fatalf("waiting = %+v", waiting)
	}
	// Nothing was written to the task: the protocol is the scheduler, and a
	// task that has not started needs no record saying so.
	if held, _ := product.Get("TASK-005"); held.Status != task.Ready {
		t.Fatalf("status = %s", held.Status)
	}
	if records, _ := task.ListEvidence(product.Layout, "TASK-005"); len(records) != 0 {
		t.Fatalf("%d evidence records for a task that never ran", len(records))
	}

	// The framework task finishes: the product task is offered.
	finish(t, framework, "TASK-004")
	item, waiting, err = q.NextWaiting(context.Background())
	if err != nil || item.TaskID != "TASK-005" || len(waiting) != 0 {
		t.Fatalf("%+v %+v %v", item, waiting, err)
	}
}

// Without a resolver, and with an alias nobody declared, the dependency is
// reported as `waiting:` — nobody could answer for it, which is not the same
// as it being open.
func TestLocalUnanswerableDependencyWaits(t *testing.T) {
	product := checkout(t, map[string]string{"TASK-005": ready("TASK-005", "Product work", "trilha:TASK-004")})

	_, waiting, err := Local{Store: product}.NextWaiting(context.Background())
	if !errors.Is(err, ErrEmpty) || len(waiting) != 1 || waiting[0].Reason != "waiting:trilha:TASK-004" {
		t.Fatalf("%+v %v", waiting, err)
	}

	other := checkout(t, map[string]string{})
	product.Remote = Checkouts{"outro": other.Layout.Root}
	_, waiting, err = Local{Store: product}.NextWaiting(context.Background())
	if !errors.Is(err, ErrEmpty) || waiting[0].Reason != "waiting:trilha:TASK-004" {
		t.Fatalf("%+v %v", waiting, err)
	}

	// A declared alias whose task is simply absent cannot be answered either.
	product.Remote = Checkouts{"trilha": other.Layout.Root}
	_, waiting, err = Local{Store: product}.NextWaiting(context.Background())
	if !errors.Is(err, ErrEmpty) || waiting[0].Reason != "waiting:trilha:TASK-004" {
		t.Fatalf("%+v %v", waiting, err)
	}
}

// A task that is waiting never hides one that can run.
func TestLocalStillOffersWhatCanRun(t *testing.T) {
	framework := checkout(t, map[string]string{"TASK-004": ready("TASK-004", "Framework work")})
	product := checkout(t, map[string]string{
		"TASK-001": ready("TASK-001", "Waits", "trilha:TASK-004"),
		"TASK-002": ready("TASK-002", "Runs now"),
	})
	product.Remote = Checkouts{"trilha": framework.Layout.Root}
	q := Local{Store: product}
	item, waiting, err := q.NextWaiting(context.Background())
	if err != nil || item.TaskID != "TASK-002" {
		t.Fatalf("%+v %v", item, err)
	}
	if len(waiting) != 1 || waiting[0].Task != "TASK-001" {
		t.Fatalf("waiting = %+v", waiting)
	}
}

// A local dependency still decides before any remote one is consulted.
func TestLocalDependenciesStillApply(t *testing.T) {
	framework := checkout(t, map[string]string{"TASK-004": ready("TASK-004", "Framework work")})
	product := checkout(t, map[string]string{
		"TASK-001": ready("TASK-001", "First"),
		"TASK-002": ready("TASK-002", "Second", "TASK-001", "trilha:TASK-004"),
	})
	product.Remote = Checkouts{"trilha": framework.Layout.Root}
	q := Local{Store: product}
	item, _, err := q.NextWaiting(context.Background())
	if err != nil || item.TaskID != "TASK-001" {
		t.Fatalf("%+v %v", item, err)
	}
	// TASK-002 waits on both, and the reason says so.
	waiting, err := q.Waiting(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range waiting {
		if w.Task != "TASK-002" {
			continue
		}
		if !strings.Contains(w.Reason, "TASK-001") || !strings.Contains(w.Reason, "trilha:TASK-004") {
			t.Fatalf("reason = %q", w.Reason)
		}
		return
	}
	t.Fatalf("TASK-002 is not reported as waiting: %+v", waiting)
}

// Checkouts answers only what it can read, and never guesses "done".
func TestCheckoutsResolver(t *testing.T) {
	framework := checkout(t, map[string]string{"TASK-004": ready("TASK-004", "Framework work")})
	c := Checkouts{"trilha": framework.Layout.Root, "vazio": t.TempDir()}

	if status, ok := c.Status("trilha", "TASK-004"); !ok || status != task.Ready {
		t.Fatalf("%q %v", status, ok)
	}
	for _, probe := range [][2]string{
		{"outro", "TASK-004"},  // alias never declared
		{"trilha", "TASK-009"}, // task not there
		{"vazio", "TASK-004"},  // not a Trilha project
	} {
		if status, ok := c.Status(probe[0], probe[1]); ok {
			t.Errorf("%v answered %q", probe, status)
		}
	}
	if got := c.Aliases(); len(got) != 2 || got[0] != "trilha" || got[1] != "vazio" {
		t.Fatalf("aliases = %v", got)
	}
}

// The control plane resolves an alias the same way, and anything it cannot
// answer leaves the dependency waiting rather than passing.
func TestRemoteResolvesACrossProjectDependency(t *testing.T) {
	var path string
	status := "done"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if status == "" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": status})
	}))
	defer srv.Close()
	q := Remote{BaseURL: srv.URL, Token: "tok", Worker: "w1", Project: "acervo"}

	got, ok := q.Status("trilha", "TASK-004")
	if !ok || got != task.Done {
		t.Fatalf("%q %v", got, ok)
	}
	if path != "/api/projects/trilha/tasks/TASK-004" {
		t.Fatalf("path = %q", path)
	}
	status = "running"
	if got, ok := q.Status("trilha", "TASK-004"); !ok || got != task.Running {
		t.Fatalf("%q %v", got, ok)
	}
	status = "shrug"
	if _, ok := q.Status("trilha", "TASK-004"); ok {
		t.Fatal("an invalid status was accepted")
	}
	status = ""
	if _, ok := q.Status("trilha", "TASK-004"); ok {
		t.Fatal("a 404 was read as an answer")
	}
	// A control plane that is not there leaves it waiting, it does not pass.
	if _, ok := (Remote{BaseURL: "http://127.0.0.1:1", Token: "t"}).Status("trilha", "TASK-004"); ok {
		t.Fatal("an unreachable control plane answered")
	}
}
