package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/emersonjoe/trilha-runner/deployer"
	"github.com/emersonjoe/trilha-runner/queue"
)

func TestWorkerCapacityClaimsThirdOnlyAfterOneFinishes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var mutex sync.Mutex
	claims := 0
	results := 0
	var runningAtClaim []int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		switch {
		case request.URL.Path == "/api/runs/next":
			var input struct {
				Running int `json:"running"`
			}
			_ = json.NewDecoder(request.Body).Decode(&input)
			runningAtClaim = append(runningAtClaim, input.Running)
			if claims >= 3 {
				response.WriteHeader(http.StatusNoContent)
				return
			}
			if claims == 2 && results == 0 {
				t.Errorf("third claim happened before capacity was released")
			}
			claims++
			_ = json.NewEncoder(response).Encode(queue.Item{ID: "run-" + string(rune('0'+claims)), Project: "app", TaskID: "TASK-001", Waiting: "waiting:dep:TASK-001"})
		case request.URL.Path == "/api/workers/heartbeat":
			response.WriteHeader(http.StatusOK)
		case len(request.URL.Path) > len("/api/runs//result") && request.Method == http.MethodPost:
			results++
			response.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	remote := queue.Remote{BaseURL: server.URL, Worker: "worker", Project: "app", Capabilities: queue.Capabilities{Labels: []string{"docker"}, Capacity: 2}}
	if err := runRemoteWorker(ctx, nilWriter{}, remote, nil, deployer.Config{}, false, workerOptions{Every: time.Millisecond, Capacity: 2}); err != nil {
		t.Fatal(err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if results != 3 || len(runningAtClaim) < 3 || runningAtClaim[0] != 0 || runningAtClaim[1] != 1 {
		t.Fatalf("claims=%d results=%d running=%v", claims, results, runningAtClaim)
	}
}

type nilWriter struct{}

func (nilWriter) Write(value []byte) (int, error) { return len(value), nil }
