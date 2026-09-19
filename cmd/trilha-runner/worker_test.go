package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersonjoe/trilha-runner/queue"
)

// project writes a checkout with n ready tasks the echo driver can run.
func project(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	sh(t, dir, "git", "init", "-q", "-b", "main")
	sh(t, dir, "git", "config", "user.email", "t@t")
	sh(t, dir, "git", "config", "user.name", "t")
	os.MkdirAll(filepath.Join(dir, ".trilha", "tasks"), 0o755)
	os.WriteFile(filepath.Join(dir, ".trilha", "project.md"), []byte("---\nname: demo\n---\n"), 0o644)
	for i := 1; i <= n; i++ {
		id := taskID(i)
		body := "---\nid: " + id + "\ntitle: Echo\nstatus: ready\nacceptance:\n  - file exists\nchecks:\n  - \"sh -c \\\"sleep 0.4; test -f TRILHA_RUN.md\\\"\"\n---\n"
		os.WriteFile(filepath.Join(dir, ".trilha", "tasks", id+".md"), []byte(body), 0o644)
	}
	sh(t, dir, "git", "add", "-A")
	sh(t, dir, "git", "commit", "-q", "-m", "init")
	return dir
}

func taskID(i int) string { return "TASK-00" + string(rune('0'+i)) }

// plane is a fake control plane that records what the worker sent and hands
// out a fixed set of runs.
type plane struct {
	mu sync.Mutex
	// pending are the task ids still to hand out.
	pending []string
	// requires is attached to every item handed out.
	requires []string
	claims   []queue.Capabilities
	beats    []map[string]any
	results  map[string]queue.Result
	// live is the highest number of runs claimed and not yet reported.
	open, live int
}

func (p *plane) server(t *testing.T) *httptest.Server {
	t.Helper()
	p.results = map[string]queue.Result{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		switch {
		case r.URL.Path == "/api/runs/next":
			var c struct {
				queue.Capabilities
				Worker, Project string
			}
			json.NewDecoder(r.Body).Decode(&c)
			p.claims = append(p.claims, c.Capabilities)
			if len(p.pending) == 0 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			id := p.pending[0]
			p.pending = p.pending[1:]
			p.open++
			if p.open > p.live {
				p.live = p.open
			}
			json.NewEncoder(w).Encode(queue.Item{ID: "run-" + id, Project: c.Project, TaskID: id, Requires: p.requires})
		case strings.HasSuffix(r.URL.Path, "/result"):
			var res queue.Result
			json.NewDecoder(r.Body).Decode(&res)
			p.results[strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/runs/"), "/result")] = res
			p.open--
			w.WriteHeader(http.StatusAccepted)
		case r.URL.Path == "/api/workers/heartbeat":
			var beat map[string]any
			json.NewDecoder(r.Body).Decode(&beat)
			p.beats = append(p.beats, beat)
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/api/deployments/next":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (p *plane) options(srv *httptest.Server, dir string, labels []string, capacity int) workerOptions {
	return workerOptions{
		Queue: queue.Remote{BaseURL: srv.URL, Token: "tok", Worker: "w1", Project: "demo",
			Capabilities: queue.Capabilities{Labels: labels, Capacity: capacity}},
		Name: "w1", Dir: dir, Driver: "echo", Capacity: capacity,
		Every: 10 * time.Millisecond, Out: io.Discard,
	}
}

// The heartbeat and the claim both carry what the worker can do.
func TestWorkerAnnouncesCapabilities(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := project(t, 1)
	p := &plane{pending: []string{"TASK-001"}}
	srv := p.server(t)
	o := p.options(srv, dir, []string{"docker", "region:br"}, 1)
	o.Once = true
	if err := runWorker(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.claims) == 0 {
		t.Fatal("no claim")
	}
	c := p.claims[0]
	if len(c.Labels) != 2 || c.Labels[0] != "docker" || c.Labels[1] != "region:br" {
		t.Fatalf("claim labels = %v", c.Labels)
	}
	if c.Capacity != 1 || c.Runner != version || len(c.Drivers) == 0 {
		t.Fatalf("claim capabilities = %+v", c)
	}
	first := p.beats[0]
	if first["status"] != "idle" || first["capacity"] != float64(1) || first["running"] != float64(0) {
		t.Fatalf("first heartbeat = %v", first)
	}
	labels, _ := first["labels"].([]any)
	if len(labels) != 2 {
		t.Fatalf("heartbeat labels = %v", first["labels"])
	}
	if first["runner"] != version {
		t.Fatalf("heartbeat runner = %v", first["runner"])
	}
	if res := p.results["run-TASK-001"]; !res.Passed || res.Status != "review" {
		t.Fatalf("result = %+v", res)
	}
}

// A run this host cannot honour is refused and reported, never executed.
func TestWorkerRefusesRunItCannotHonour(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := project(t, 1)
	p := &plane{pending: []string{"TASK-001"}, requires: []string{"docker", "gpu"}}
	srv := p.server(t)
	o := p.options(srv, dir, []string{"docker"}, 1)
	o.Once = true
	if err := runWorker(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	res := p.results["run-TASK-001"]
	if res.Status != "failed" || !strings.Contains(res.Error, "gpu") {
		t.Fatalf("result = %+v", res)
	}
	// The task was never started: it is still ready in the checkout.
	b, _ := os.ReadFile(filepath.Join(dir, ".trilha", "tasks", "TASK-001.md"))
	if !strings.Contains(string(b), "status: ready") {
		t.Fatalf("task was touched:\n%s", b)
	}
}

// Capacity 2 keeps two runs in flight and refuses a third until one finishes.
func TestWorkerCapacityRunsTwoAndHoldsTheThird(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := project(t, 3)
	p := &plane{pending: []string{"TASK-001", "TASK-002", "TASK-003"}}
	srv := p.server(t)
	o := p.options(srv, dir, nil, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runWorker(ctx, o) }()
	deadline := time.After(45 * time.Second)
	for {
		p.mu.Lock()
		finished := len(p.results)
		p.mu.Unlock()
		if finished == 3 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("worker stopped early: %v", err)
		case <-deadline:
			t.Fatalf("only %d of 3 runs finished", finished)
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	<-done
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.live != 2 {
		t.Fatalf("%d runs were in flight at once, want 2", p.live)
	}
	for _, id := range []string{"run-TASK-001", "run-TASK-002", "run-TASK-003"} {
		if res, ok := p.results[id]; !ok || !res.Passed {
			t.Fatalf("%s = %+v (ok=%v)", id, res, ok)
		}
	}
	// The claims report the real load, so the control plane sees it.
	var sawLoad bool
	for _, c := range p.claims {
		if c.Running > 0 {
			sawLoad = true
		}
		if c.Running > 2 {
			t.Fatalf("claim reported %d runs in flight", c.Running)
		}
	}
	if !sawLoad {
		t.Fatal("no claim reported a run in flight")
	}
}
