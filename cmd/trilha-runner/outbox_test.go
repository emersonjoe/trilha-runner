package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/emersonjoe/trilha-runner/deployer"
	"github.com/emersonjoe/trilha-runner/queue"
)

// RM-012: the result is on disk before the cloud is asked to take it, a 500
// is retried with backoff instead of killing the worker and losing the run,
// and the cloud ends up with exactly one accepted result.
func TestWorkerResultSurvivesTwoFailedDeliveries(t *testing.T) {
	root := gitRepo(t)
	outbox := filepath.Join(root, ".trilha", "runs", "run-000001", "result.json")
	var mutex sync.Mutex
	posts, accepted := 0, 0
	var onDiskAtFirstPost bool
	claimed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		switch r.URL.Path {
		case "/api/runs/next":
			if claimed {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			claimed = true
			json.NewEncoder(w).Encode(queue.Item{ID: "run-000001", Project: "demo", TaskID: "TASK-001"})
		case "/api/workers/heartbeat":
		case "/api/runs/run-000001/result":
			posts++
			if posts == 1 {
				_, err := os.Stat(outbox)
				onDiskAtFirstPost = err == nil
			}
			var result queue.Result
			_ = json.NewDecoder(r.Body).Decode(&result)
			if posts <= 2 {
				http.Error(w, "database is down", http.StatusInternalServerError)
				return
			}
			if result.Status == "review" && result.Attempt == 1 {
				accepted++
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	base, err := newRunnerAt(root, "echo", "", nilWriter{})
	if err != nil {
		t.Fatal(err)
	}
	options := workerOptions{Once: true, Every: time.Millisecond, Capacity: 1, resultRetryBase: time.Millisecond,
		execute: func(ctx context.Context, remote queue.Remote, item queue.Item) workerOutcome {
			return workerOutcome{item: item, root: root, report: queue.Result{Status: "review", Passed: true, Attempt: 1}}
		},
	}
	remote := queue.Remote{BaseURL: server.URL, Worker: "w1", Project: "demo", Capabilities: queue.Capabilities{Capacity: 1}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runRemoteWorker(ctx, nilWriter{}, remote, base, deployer.Config{}, false, options); err != nil {
		t.Fatalf("a failed delivery stopped the worker: %v", err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if !onDiskAtFirstPost {
		t.Fatal("the result was not in the outbox before the first POST")
	}
	if posts != 3 || accepted != 1 {
		t.Fatalf("posts = %d, accepted = %d; want 3 and 1", posts, accepted)
	}
	if _, err := os.Stat(outbox); !os.IsNotExist(err) {
		t.Fatalf("the delivered result is still in the outbox: %v", err)
	}
}

// A worker that died with a result in its outbox delivers it when it starts
// again, before claiming anything; a result the cloud already has (409) is
// cleared, and one it refuses for good is set aside, not retried forever.
func TestWorkerResendsPendingResultsAtStart(t *testing.T) {
	root := gitRepo(t)
	write := func(runID, status string) string {
		path := filepath.Join(root, ".trilha", "runs", runID, "result.json")
		os.MkdirAll(filepath.Dir(path), 0o755)
		data, _ := json.Marshal(outboxEntry{RunID: runID, TaskID: "TASK-001", Result: queue.Result{Status: status, Attempt: 1}})
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	pending := write("run-000002", "review")
	duplicate := write("run-000003", "failed")
	refused := write("run-000004", "failed")
	var mutex sync.Mutex
	var order []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		order = append(order, r.URL.Path)
		switch r.URL.Path {
		case "/api/runs/next":
			w.WriteHeader(http.StatusNoContent)
		case "/api/workers/heartbeat", "/api/runs/run-000002/result":
		case "/api/runs/run-000003/result":
			http.Error(w, "run already has a result", http.StatusConflict)
		case "/api/runs/run-000004/result":
			http.Error(w, "unknown run", http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	base, err := newRunnerAt(root, "echo", "", nilWriter{})
	if err != nil {
		t.Fatal(err)
	}
	remote := queue.Remote{BaseURL: server.URL, Worker: "w1", Project: "demo", Capabilities: queue.Capabilities{Capacity: 1}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runRemoteWorker(ctx, nilWriter{}, remote, base, deployer.Config{}, false, workerOptions{Once: true, Every: time.Millisecond, Capacity: 1, resultRetryBase: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	claim := -1
	sent := map[string]int{}
	for i, path := range order {
		if path == "/api/runs/next" && claim < 0 {
			claim = i
		}
		sent[path] = i
	}
	for _, path := range []string{"/api/runs/run-000002/result", "/api/runs/run-000003/result", "/api/runs/run-000004/result"} {
		if i, ok := sent[path]; !ok || i > claim {
			t.Fatalf("%s not sent before the first claim: %v", path, order)
		}
	}
	for _, path := range []string{pending, duplicate, refused} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s is still pending", path)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(refused), "result.rejected.json")); err != nil {
		t.Errorf("the refused result was not set aside: %v", err)
	}
}
