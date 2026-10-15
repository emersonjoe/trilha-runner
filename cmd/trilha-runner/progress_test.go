package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersonjoe/trilha-runner/deployer"
	"github.com/emersonjoe/trilha-runner/queue"
)

// gitRepo builds a temp git checkout with one ready TASK-001 the echo driver
// can complete, mirroring runner_test.go's own repo helper (unexported
// there, so this package needs its own).
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	os.MkdirAll(filepath.Join(dir, ".trilha", "tasks"), 0o755)
	os.WriteFile(filepath.Join(dir, ".trilha", "project.md"), []byte("---\nname: demo\n---\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".trilha", "tasks", "TASK-001.md"), []byte("---\nid: TASK-001\ntitle: Echo\nstatus: ready\nacceptance:\n  - file exists\nchecks:\n  - \"sh -c \\\"test -f TRILHA_RUN.md\\\"\"\n---\n"), 0o644)
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	return dir
}

// TestWorkerReportsProgressBestEffort is SC-005 from trilha-cloud issue #22:
// processing an item sends POST /api/runs/{id}/progress with
// phase=consulting_ai, attempt=1, and — because progress is best-effort — a
// 500 from that route neither aborts the run nor stops POST …/result from
// still being sent.
func TestWorkerReportsProgressBestEffort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := gitRepo(t)
	base, err := newRunnerAt(dir, "echo", "", io.Discard)
	if err != nil {
		t.Fatal(err)
	}

	var mutex sync.Mutex
	var sawConsultingAIAttempt1 bool
	var resultPosted bool
	claimed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		switch {
		case r.URL.Path == "/api/runs/next":
			if claimed {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			claimed = true
			json.NewEncoder(w).Encode(queue.Item{ID: "run-1", Project: "demo", TaskID: "TASK-001"})
		case r.URL.Path == "/api/runs/run-1/bundle":
			http.NotFound(w, r) // no Cloud-managed bundle: fall back to the local task
		case r.URL.Path == "/api/runs/run-1/progress":
			var progress queue.Progress
			_ = json.NewDecoder(r.Body).Decode(&progress)
			if progress.Phase == "consulting_ai" && progress.Attempt == 1 {
				sawConsultingAIAttempt1 = true
			}
			// Best-effort: even a hard failure here must not affect the run.
			http.Error(w, "boom", http.StatusInternalServerError)
		case r.URL.Path == "/api/runs/run-1/result":
			resultPosted = true
			w.WriteHeader(http.StatusAccepted)
		case r.URL.Path == "/api/workers/heartbeat":
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	remote := queue.Remote{BaseURL: server.URL, Worker: "w1", Project: "demo", Capabilities: queue.Capabilities{Capacity: 1}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runRemoteWorker(ctx, nilWriter{}, remote, base, deployer.Config{}, false, workerOptions{Once: true, Every: time.Millisecond, Capacity: 1}); err != nil {
		t.Fatalf("runRemoteWorker: %v", err)
	}

	mutex.Lock()
	defer mutex.Unlock()
	if !sawConsultingAIAttempt1 {
		t.Fatal("no POST .../progress with phase=consulting_ai attempt=1 was observed")
	}
	if !resultPosted {
		t.Fatal("POST .../result was not sent despite the progress route failing")
	}
}

// TestWorkerReportsStartupFailureAsHeartbeat is SC-006 from trilha-cloud
// issue #25: cmdWorker run against a directory with no .trilha/ sends a
// failed heartbeat naming the error and still returns a non-nil error.
func TestWorkerReportsStartupFailureAsHeartbeat(t *testing.T) {
	var mutex sync.Mutex
	var gotStatus, gotError string
	var heartbeats int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		if r.URL.Path != "/api/workers/heartbeat" {
			http.NotFound(w, r)
			return
		}
		heartbeats++
		var body struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotStatus, gotError = body.Status, body.Error
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	empty := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(empty); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := []string{"--cloud", server.URL, "--token", "tok", "--project", "demo", "--once"}
	runErr := cmdWorker(ctx, args, nilWriter{})
	if runErr == nil {
		t.Fatal("cmdWorker: expected an error running outside a Trilha project")
	}

	mutex.Lock()
	defer mutex.Unlock()
	if heartbeats == 0 {
		t.Fatal("no heartbeat was sent")
	}
	if gotStatus != "failed" {
		t.Fatalf("status = %q, want failed", gotStatus)
	}
	if !strings.Contains(gotError, "no .trilha/") {
		t.Fatalf("error = %q, want it to mention no .trilha/", gotError)
	}
}
