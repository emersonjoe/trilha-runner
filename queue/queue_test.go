package queue

import (
	"context"
	"encoding/json"
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
