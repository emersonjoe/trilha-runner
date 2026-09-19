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

func TestParseDependency(t *testing.T) {
	d, ok := ParseDependency(" trilha:TASK-004 ")
	if !ok || d.Alias != "trilha" || d.Task != "TASK-004" || d.String() != "trilha:TASK-004" {
		t.Fatalf("%+v %v", d, ok)
	}
	for _, bad := range []string{"TASK-004", "", ":TASK-004", "trilha:", "trilha:nope", "trilha:task-4"} {
		if _, ok := ParseDependency(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

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

func ready(id, title string, remote ...string) string {
	body := "---\nid: " + id + "\ntitle: " + title + "\nstatus: ready\nacceptance:\n  - it works\n"
	if len(remote) > 0 {
		body += RemoteField + ":\n"
		for _, d := range remote {
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

func done(id, title string) string {
	return "---\nid: " + id + "\ntitle: " + title + "\nstatus: done\n---\n"
}

// A task waiting on another repository is not offered, and the reason says
// which dependency and where it stands.
func TestLocalHoldsACrossRepositoryDependency(t *testing.T) {
	framework := checkout(t, map[string]string{"TASK-004": ready("TASK-004", "Framework work")})
	product := checkout(t, map[string]string{"TASK-005": ready("TASK-005", "Product work", "trilha:TASK-004")})

	q := Local{Store: product, Resolver: Checkouts{"trilha": framework.Layout.Root}}
	item, waiting, err := q.NextWaiting(context.Background())
	if !errors.Is(err, ErrEmpty) {
		t.Fatalf("%+v %v", item, err)
	}
	if len(waiting) != 1 || waiting[0].Task != "TASK-005" {
		t.Fatalf("waiting = %+v", waiting)
	}
	if !strings.HasPrefix(waiting[0].Reason, "waiting:trilha:TASK-004") || !strings.Contains(waiting[0].Reason, "ready") {
		t.Fatalf("reason = %q", waiting[0].Reason)
	}
	// It is reported as blocked, with the reason as evidence.
	held, _ := product.Get("TASK-005")
	if held.Status != task.Blocked {
		t.Fatalf("status = %s", held.Status)
	}
	records, _ := task.ListEvidence(product.Layout, "TASK-005")
	if len(records) != 1 || records[0].Meta["waiting"] != waiting[0].Reason {
		t.Fatalf("evidence = %+v", records)
	}
	// Polling again does not pile up identical notes.
	if _, _, err := q.NextWaiting(context.Background()); !errors.Is(err, ErrEmpty) {
		t.Fatal(err)
	}
	if records, _ := task.ListEvidence(product.Layout, "TASK-005"); len(records) != 1 {
		t.Fatalf("%d records after a second poll", len(records))
	}

	// The framework task finishes: the product task is released.
	finish(t, framework, "TASK-004")
	item, waiting, err = q.NextWaiting(context.Background())
	if err != nil || item.TaskID != "TASK-005" || len(waiting) != 0 {
		t.Fatalf("%+v %+v %v", item, waiting, err)
	}
	if got, _ := product.Get("TASK-005"); got.Status != task.Ready {
		t.Fatalf("status = %s", got.Status)
	}
}

// Without a resolver the runner does not guess: it holds the task and says
// which flag it needs.
func TestLocalWithoutAResolverHoldsAndSaysWhy(t *testing.T) {
	product := checkout(t, map[string]string{"TASK-005": ready("TASK-005", "Product work", "trilha:TASK-004")})
	_, waiting, err := Local{Store: product}.NextWaiting(context.Background())
	if !errors.Is(err, ErrEmpty) || len(waiting) != 1 {
		t.Fatalf("%+v %v", waiting, err)
	}
	if !strings.Contains(waiting[0].Reason, "--repo trilha=") {
		t.Fatalf("reason = %q", waiting[0].Reason)
	}
}

// An alias nobody declared, or a task that is not there, blocks — it is never
// read as done.
func TestLocalUnresolvableDependencyBlocks(t *testing.T) {
	other := checkout(t, map[string]string{})
	product := checkout(t, map[string]string{"TASK-005": ready("TASK-005", "Product work", "trilha:TASK-004")})

	_, waiting, err := Local{Store: product, Resolver: Checkouts{"outro": other.Layout.Root}}.NextWaiting(context.Background())
	if !errors.Is(err, ErrEmpty) || !strings.Contains(waiting[0].Reason, "unknown repository alias") {
		t.Fatalf("%+v %v", waiting, err)
	}

	product2 := checkout(t, map[string]string{"TASK-005": ready("TASK-005", "Product work", "trilha:TASK-004")})
	_, waiting, err = Local{Store: product2, Resolver: Checkouts{"trilha": other.Layout.Root}}.NextWaiting(context.Background())
	if !errors.Is(err, ErrEmpty) || !strings.Contains(waiting[0].Reason, "not found") {
		t.Fatalf("%+v %v", waiting, err)
	}
}

// A task with no cross-repository dependency is offered as before, and one
// that is waiting never hides a task that can run.
func TestLocalStillOffersWhatCanRun(t *testing.T) {
	framework := checkout(t, map[string]string{"TASK-004": ready("TASK-004", "Framework work")})
	product := checkout(t, map[string]string{
		"TASK-001": ready("TASK-001", "Waits", "trilha:TASK-004"),
		"TASK-002": ready("TASK-002", "Runs now"),
	})
	q := Local{Store: product, Resolver: Checkouts{"trilha": framework.Layout.Root}}
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
	framework := checkout(t, map[string]string{"TASK-004": done("TASK-004", "Framework work")})
	product := checkout(t, map[string]string{
		"TASK-001": ready("TASK-001", "First"),
		"TASK-002": "---\nid: TASK-002\ntitle: Second\nstatus: ready\nacceptance:\n  - ok\ndepends_on:\n  - TASK-001\n" + RemoteField + ":\n  - trilha:TASK-004\n---\n",
	})
	q := Local{Store: product, Resolver: Checkouts{"trilha": framework.Layout.Root}}
	item, _, err := q.NextWaiting(context.Background())
	if err != nil || item.TaskID != "TASK-001" {
		t.Fatalf("%+v %v", item, err)
	}
	// TASK-002 was never consulted, so it was not blocked either.
	if got, _ := product.Get("TASK-002"); got.Status != task.Ready {
		t.Fatalf("status = %s", got.Status)
	}
}

// The control plane resolves an alias the same way, and a 404 blocks.
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
	dep := RemoteDependency{Alias: "trilha", Task: "TASK-004"}

	got, err := q.Status(context.Background(), dep)
	if err != nil || got != task.Done {
		t.Fatalf("%q %v", got, err)
	}
	if path != "/api/projects/trilha/tasks/TASK-004" {
		t.Fatalf("path = %q", path)
	}
	status = "running"
	if got, _ := q.Status(context.Background(), dep); got != task.Running {
		t.Fatalf("status = %q", got)
	}
	status = "shrug"
	if _, err := q.Status(context.Background(), dep); err == nil {
		t.Fatal("an invalid status was accepted")
	}
	status = ""
	if _, err := q.Status(context.Background(), dep); !errors.Is(err, ErrUnknownAlias) {
		t.Fatalf("err = %v", err)
	}
}

// The protocol's own spelling is read too, once it accepts an alias inside
// depends_on.
func TestRemoteDependenciesReadBothSpellings(t *testing.T) {
	tk := &task.Task{ID: "TASK-005", DependsOn: []string{"TASK-001", "trilha:TASK-004"}}
	tk.Fields.SetList(RemoteField, []string{"trilha:TASK-004", "cloud:TASK-020"})
	got := RemoteDependencies(tk)
	if len(got) != 2 || got[0].String() != "cloud:TASK-020" || got[1].String() != "trilha:TASK-004" {
		t.Fatalf("got %+v", got)
	}
	if local := LocalDependencies(tk); len(local) != 1 || local[0] != "TASK-001" {
		t.Fatalf("local = %v", local)
	}
}
