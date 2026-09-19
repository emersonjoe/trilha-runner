package queue

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/emersonjoe/trilha-spec/spec"
	"github.com/emersonjoe/trilha-spec/task"
)

func TestLocal(t *testing.T) {
	l, _, _ := spec.Init(t.TempDir(), spec.InitOptions{})
	st := &task.Store{Layout: l}
	q := Local{Store: st}
	if _, err := q.Next(context.Background()); err != ErrEmpty {
		t.Fatalf("err = %v", err)
	}
	st.Create("a", func(x *task.Task) { x.Status = task.Ready; x.Acceptance = []string{"ok"} })
	it, err := q.Next(context.Background())
	if err != nil || it.TaskID != "TASK-001" {
		t.Fatalf("%+v %v", it, err)
	}
}

func TestLocalResolvesCrossRepositoryDependency(t *testing.T) {
	primary, _, err := spec.Init(t.TempDir(), spec.InitOptions{Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	dependency, _, err := spec.Init(t.TempDir(), spec.InitOptions{Name: "trilha"})
	if err != nil {
		t.Fatal(err)
	}
	dependencyStore := &task.Store{Layout: dependency}
	if _, err := dependencyStore.Create("framework", func(item *task.Task) { item.Status = task.Done }); err != nil {
		t.Fatal(err)
	}
	content := "---\nid: TASK-001\ntitle: app\nstatus: ready\ndepends_on:\n  - \"trilha:TASK-001\"\nacceptance:\n  - done\nchecks:\n  - true\n---\n"
	if err := os.WriteFile(filepath.Join(primary.Tasks(), "TASK-001.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &task.Store{Layout: primary}
	if _, err := (Local{Store: store}).Next(context.Background()); err == nil || err.Error() != "TASK-001 blocked: waiting:trilha:TASK-001" {
		t.Fatalf("unresolved error = %v", err)
	}
	item, err := (Local{Store: store, Repositories: map[string]string{"trilha": dependency.Root}}).Next(context.Background())
	if err != nil || item.TaskID != "TASK-001" {
		t.Fatalf("item=%+v err=%v", item, err)
	}
}

func TestRemote(t *testing.T) {
	var gotAuth, gotResult string
	var gotClaim, gotHeartbeat Capabilities
	empty := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/api/runs/next":
			if r.Method != http.MethodPost {
				http.Error(w, "claim is a POST", http.StatusMethodNotAllowed)
				return
			}
			var claim struct {
				Worker  string
				Project string
				Capabilities
			}
			json.NewDecoder(r.Body).Decode(&claim)
			gotClaim = claim.Capabilities
			if empty {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			json.NewEncoder(w).Encode(Item{ID: "run-1", Project: claim.Project, TaskID: "TASK-007"})
		case "/api/runs/run-1/result":
			var res Result
			json.NewDecoder(r.Body).Decode(&res)
			gotResult = res.Status
			w.WriteHeader(http.StatusAccepted)
		case "/api/workers/heartbeat":
			var heartbeat struct {
				Capabilities
			}
			json.NewDecoder(r.Body).Decode(&heartbeat)
			gotHeartbeat = heartbeat.Capabilities
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	q := Remote{BaseURL: srv.URL, Token: "tok", Worker: "w1", Project: "demo", Capabilities: Capabilities{Labels: []string{"docker", "region:br"}, Capacity: 2, Running: 1, RunnerVersion: "0.3.0", DriverVersions: map[string]string{"ai": "0.3.0"}}}
	it, err := q.Next(context.Background())
	if err != nil || it.ID != "run-1" || it.TaskID != "TASK-007" || it.Project != "demo" || gotAuth != "Bearer tok" {
		t.Fatalf("%+v %v auth=%q", it, err, gotAuth)
	}
	if err := q.Done(context.Background(), it, Result{Status: "review", Passed: true}); err != nil || gotResult != "review" {
		t.Fatalf("done: %v %q", err, gotResult)
	}
	if err := q.Heartbeat(context.Background(), "idle"); err != nil {
		t.Fatal(err)
	}
	if gotClaim.Capacity != 2 || gotClaim.Running != 1 || len(gotClaim.Labels) != 2 || gotClaim.RunnerVersion != "0.3.0" {
		t.Fatalf("claim capabilities = %+v", gotClaim)
	}
	if gotHeartbeat.Capacity != 2 || gotHeartbeat.DriverVersions["ai"] != "0.3.0" {
		t.Fatalf("heartbeat capabilities = %+v", gotHeartbeat)
	}
	empty = true
	if _, err := q.Next(context.Background()); err != ErrEmpty {
		t.Fatalf("err = %v", err)
	}
}

func TestRemoteDependencyStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/runs/next":
			json.NewEncoder(w).Encode(Item{ID: "run-1", Project: "app", TaskID: "TASK-001", DependsOn: []string{"trilha:TASK-007"}})
		case "/api/projects/trilha/tasks/TASK-007":
			json.NewEncoder(w).Encode(map[string]string{"status": "ready"})
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	item, err := (Remote{BaseURL: server.URL, Worker: "w", Project: "app"}).Next(context.Background())
	if err != nil || item.Waiting != "waiting:trilha:TASK-007" {
		t.Fatalf("item=%+v err=%v", item, err)
	}
}
