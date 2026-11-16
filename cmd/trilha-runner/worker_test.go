package main

import (
	"context"
	"encoding/json"
	"errors"
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

// roundTrip answers every request of a queue.Remote without a network, so a
// test decides exactly when the worker's context is cancelled.
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func answer(status int) *http.Response {
	return &http.Response{StatusCode: status, Body: http.NoBody, Header: http.Header{}}
}

// A shutdown (SIGTERM, Ctrl-C) is not a failure, wherever it finds the
// worker: in the middle of a claim or waiting on an empty queue. A claim
// that fails while the worker is not stopping is an error. Each moment is
// forced, not left to the scheduler, so the suite covers every branch on
// every run.
func TestWorkerShutdownWhileClaimingOrIdleIsNotAFailure(t *testing.T) {
	cases := []struct {
		name string
		// next answers the claim; cancel stops the worker.
		next    func(cancel context.CancelFunc) (*http.Response, error)
		wantErr bool
	}{
		{"shutdown during the claim", func(cancel context.CancelFunc) (*http.Response, error) {
			cancel()
			return nil, context.Canceled
		}, false},
		{"shutdown while idle", func(cancel context.CancelFunc) (*http.Response, error) {
			// The queue is empty; the worker then waits an hour unless stopped.
			cancel()
			return answer(http.StatusNoContent), nil
		}, false},
		{"claim fails while running", func(context.CancelFunc) (*http.Response, error) {
			return nil, errors.New("connection refused")
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == "/api/runs/next" {
					return c.next(cancel)
				}
				return answer(http.StatusOK), nil
			})}
			remote := queue.Remote{BaseURL: "http://cloud.example", Worker: "worker", Project: "app", HTTPClient: client}
			done := make(chan error, 1)
			go func() {
				done <- runRemoteWorker(ctx, nilWriter{}, remote, nil, deployer.Config{}, false, workerOptions{Every: time.Hour, WorkspaceRoot: t.TempDir()})
			}()
			select {
			case err := <-done:
				if (err != nil) != c.wantErr {
					t.Fatalf("err = %v, want error: %v", err, c.wantErr)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the worker did not return")
			}
		})
	}
}
