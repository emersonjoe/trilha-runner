package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersonjoe/trilha-runner/deployer"
	"github.com/emersonjoe/trilha-runner/queue"
)

// RM-013: a run of 100 s (simulated by a ticker the test drives) keeps the
// worker visibly alive: one heartbeat per tick saying `running 1/1`, on top
// of the transitions, and none once the run is over.
func TestWorkerHeartbeatsWhileARunIsActive(t *testing.T) {
	var mutex sync.Mutex
	var statuses []string
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
			var body struct {
				Status string `json:"status"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			statuses = append(statuses, body.Status)
		case "/api/runs/run-000001/result":
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	count := func(status string) int {
		mutex.Lock()
		defer mutex.Unlock()
		n := 0
		for _, s := range statuses {
			if s == status {
				n++
			}
		}
		return n
	}

	ticks := make(chan time.Time)
	release := make(chan struct{})
	started := make(chan struct{})
	options := workerOptions{Once: true, Every: time.Millisecond, Capacity: 1, WorkspaceRoot: t.TempDir(),
		heartbeatTicker: func(every time.Duration) (<-chan time.Time, func()) {
			if every != 30*time.Second {
				t.Errorf("heartbeat interval = %s, want 30s", every)
			}
			return ticks, func() {}
		},
		execute: func(ctx context.Context, remote queue.Remote, item queue.Item) workerOutcome {
			close(started)
			<-release
			return workerOutcome{item: item, report: queue.Result{Status: "review", Passed: true}}
		},
	}
	remote := queue.Remote{BaseURL: server.URL, Worker: "w1", Project: "demo", Capabilities: queue.Capabilities{Capacity: 1}}
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { done <- runRemoteWorker(ctx, nilWriter{}, remote, nil, deployer.Config{}, false, options) }()
	<-started
	transitions := count("running 1/1")
	for i := 0; i < 3; i++ { // 30 s, 60 s, 90 s into a 100 s run
		ticks <- time.Now()
	}
	deadline := time.Now().Add(5 * time.Second)
	for count("running 1/1") < transitions+3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := count("running 1/1"); got < transitions+3 {
		t.Fatalf("heartbeats while running = %d, want at least %d (%v)", got, transitions+3, statuses)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if last := statuses[len(statuses)-1]; !strings.HasPrefix(last, "idle") {
		t.Fatalf("last heartbeat = %q, want idle (%v)", last, statuses)
	}
}
