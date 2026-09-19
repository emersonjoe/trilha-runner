package queue

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestRemote(t *testing.T) {
	var gotAuth, gotResult string
	empty := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/api/runs/next":
			if r.Method != http.MethodPost {
				http.Error(w, "claim is a POST", http.StatusMethodNotAllowed)
				return
			}
			var claim struct{ Worker, Project string }
			json.NewDecoder(r.Body).Decode(&claim)
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
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	q := Remote{BaseURL: srv.URL, Token: "tok", Worker: "w1", Project: "demo"}
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
	empty = true
	if _, err := q.Next(context.Background()); err != ErrEmpty {
		t.Fatalf("err = %v", err)
	}
}

func TestCapabilitiesMeets(t *testing.T) {
	c := Capabilities{Labels: []string{"docker", "region:br"}}
	if ok, missing := c.Meets(nil); !ok || missing != nil {
		t.Fatalf("no requirement: %v %v", ok, missing)
	}
	if ok, _ := c.Meets([]string{"docker"}); !ok {
		t.Fatal("docker is declared")
	}
	ok, missing := c.Meets([]string{"docker", "gpu", "region:us"})
	if ok || len(missing) != 2 || missing[0] != "gpu" || missing[1] != "region:us" {
		t.Fatalf("%v %v", ok, missing)
	}
}

// The claim carries the capabilities, and a run this worker cannot honour
// comes back as ErrUnmet with the item, so the caller can report it.
func TestRemoteCapabilitiesAndUnmetRequirements(t *testing.T) {
	var got struct {
		Capabilities
		Worker, Project string
	}
	requires := []string{"docker"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/runs/next":
			json.NewDecoder(r.Body).Decode(&got)
			json.NewEncoder(w).Encode(Item{ID: "run-1", Project: "demo", TaskID: "TASK-007", Requires: requires})
		case "/api/workers/heartbeat":
			json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	q := Remote{BaseURL: srv.URL, Token: "tok", Worker: "w1", Project: "demo",
		Capabilities: Capabilities{Labels: []string{"region:br"}, Capacity: 2, Runner: "test"}}
	it, err := q.NextRunning(context.Background(), 1)
	if !errors.Is(err, ErrUnmet) || it.ID != "run-1" {
		t.Fatalf("%+v %v", it, err)
	}
	if got.Capacity != 2 || got.Running != 1 || got.Runner != "test" || len(got.Labels) != 1 {
		t.Fatalf("claim = %+v", got)
	}

	// With the label, the same run is accepted.
	q.Capabilities.Labels = []string{"region:br", "docker"}
	if it, err := q.Next(context.Background()); err != nil || it.TaskID != "TASK-007" {
		t.Fatalf("%+v %v", it, err)
	}

	// Capacity is never below one, whatever was configured.
	q.Capabilities.Capacity = 0
	if err := q.HeartbeatRunning(context.Background(), "idle", 0); err != nil {
		t.Fatal(err)
	}
	if got.Capacity != 1 {
		t.Fatalf("heartbeat capacity = %d", got.Capacity)
	}
}
