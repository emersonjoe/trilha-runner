package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
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
	"github.com/emersonjoe/trilha-spec/execution"
)

// A hook must never hold up or fail the agent: with no socket variable, a
// socket nobody listens on, a listener that never answers, or a stdin that
// never closes, `trilha-runner hook claude` exits 0 in under a second.
func TestHookAlwaysExitsZeroWithinASecond(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets")
	}
	dir, err := os.MkdirTemp("", "hk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	silent := filepath.Join(dir, "silent.sock")
	listener, err := net.Listen("unix", silent)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer conn.Close() // accepts, never reads, never answers
		}
	}()
	payload := `{"hook_event_name":"PreToolUse","session_id":"s1"}`
	// The first exec of a freshly built binary pays the operating system's
	// one-time scan of it (seconds, on macOS); an installed runner has paid it
	// long before any hook fires. Pay it here, outside the clock.
	if out, err := exec.Command(bin, "version").CombinedOutput(); err != nil {
		t.Fatalf("warm-up: %v %s", err, out)
	}
	cases := []struct {
		name, socket string
		openStdin    bool
	}{
		{"no socket variable", "", false},
		{"socket file missing", filepath.Join(dir, "gone.sock"), false},
		{"listener never answers", silent, false},
		{"stdin never closes", filepath.Join(dir, "gone.sock"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.Command(bin, "hook", "claude")
			cmd.Env = append(os.Environ(), "TRILHA_RUN_SOCKET="+c.socket)
			if c.openStdin {
				// A real pipe whose write end stays open: the hook cannot
				// wait for end of input.
				reader, writer, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				defer writer.Close()
				cmd.Stdin = reader
			} else {
				cmd.Stdin = strings.NewReader(payload)
			}
			start := time.Now()
			out, err := cmd.CombinedOutput()
			if elapsed := time.Since(start); elapsed > time.Second {
				t.Fatalf("hook took %s", elapsed)
			}
			if err != nil {
				t.Fatalf("hook exited non-zero: %v %s", err, out)
			}
		})
	}
	if out, err := exec.Command(bin, "ask", "anything").CombinedOutput(); err != nil || !strings.Contains(string(out), "not inside a trilha run") {
		t.Fatalf("ask outside a run: %v %s", err, out)
	}
}

// useBinary makes the in-process worker hand the agent the binary TestMain
// built, the way a real worker hands its own.
func useBinary(t *testing.T) {
	t.Helper()
	previous := selfPath
	selfPath = bin
	t.Cleanup(func() { selfPath = previous })
}

// fakeCloud is a control plane for one or more runs: it hands out the items
// in order, keeps every progress report and every result.
type fakeCloud struct {
	mutex    sync.Mutex
	items    []queue.Item
	progress []queue.Progress
	results  map[string]queue.Result
}

func (c *fakeCloud) serve(t *testing.T) *httptest.Server {
	c.results = map[string]queue.Result{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mutex.Lock()
		defer c.mutex.Unlock()
		switch {
		case r.URL.Path == "/api/runs/next":
			if len(c.items) == 0 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			json.NewEncoder(w).Encode(c.items[0])
			c.items = c.items[1:]
		case strings.HasSuffix(r.URL.Path, "/bundle"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/progress"):
			var p queue.Progress
			_ = json.NewDecoder(r.Body).Decode(&p)
			c.progress = append(c.progress, p)
		case strings.HasSuffix(r.URL.Path, "/result"):
			var result queue.Result
			_ = json.NewDecoder(r.Body).Decode(&result)
			c.results[strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/runs/"), "/result")] = result
			w.WriteHeader(http.StatusAccepted)
		case r.URL.Path == "/api/workers/heartbeat":
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func (c *fakeCloud) work(t *testing.T, dir, drv, command string) {
	t.Helper()
	server := c.serve(t)
	base, err := newRunnerAt(dir, drv, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	base.Log = func(string) {}
	remote := queue.Remote{BaseURL: server.URL, Worker: "w1", Project: "demo", Capabilities: queue.Capabilities{Capacity: 1}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for range c.items {
		if err := runRemoteWorker(ctx, nilWriter{}, remote, base, deployer.Config{}, false, workerOptions{Once: true, Every: time.Millisecond, Capacity: 1}); err != nil {
			t.Fatal(err)
		}
	}
}

// DV-01 end to end: a claude-code run gets a settings file outside its
// worktree whose hooks call `trilha-runner hook claude`; the hooks fire, and
// the cloud sees the agent go working → idle; the result names the session.
func TestClaudeHooksReachTheCloudAsAgentState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	useBinary(t)
	fake := t.TempDir()
	script := `#!/bin/sh
settings=""; sid=""
while [ $# -gt 0 ]; do
  case "$1" in --settings) settings="$2"; shift;; --session-id) sid="$2"; shift;; esac
  shift
done
cat > /dev/null
hook=$(grep -o '"command": *"[^"]*"' "$settings" | head -1 | sed 's/^"command": *"//; s/"$//')
echo "{\"hook_event_name\":\"PreToolUse\",\"session_id\":\"$sid\",\"tool_name\":\"Write\"}" | sh -c "$hook"
echo hello > GREETING.txt
echo "{\"hook_event_name\":\"PostToolUse\",\"session_id\":\"$sid\",\"tool_name\":\"Write\"}" | sh -c "$hook"
echo "{\"hook_event_name\":\"Stop\",\"session_id\":\"$sid\"}" | sh -c "$hook"
echo "$sid" > "` + fake + `/sid"
`
	if err := os.WriteFile(filepath.Join(fake, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir := gitRepo(t)
	os.WriteFile(filepath.Join(dir, ".trilha", "tasks", "TASK-001.md"), []byte("---\nid: TASK-001\ntitle: Greeting\nstatus: ready\nacceptance:\n  - greeting\nchecks:\n  - \"sh -c \\\"test -f GREETING.txt\\\"\"\n---\n"), 0o644)
	cloud := &fakeCloud{items: []queue.Item{{ID: "run-000001", Project: "demo", TaskID: "TASK-001", AI: &queue.AI{Provider: "claude-code"}}}}
	cloud.work(t, dir, "", "")

	cloud.mutex.Lock()
	defer cloud.mutex.Unlock()
	var states []string
	for _, p := range cloud.progress {
		if p.AgentState != "" {
			states = append(states, string(p.AgentState))
			if p.Message == "" {
				t.Errorf("an agent state travelled without a message: %+v", p)
			}
		}
	}
	if strings.Join(states, ",") != "working,idle" {
		t.Fatalf("agent states = %v", states)
	}
	result := cloud.results["run-000001"]
	sid, _ := os.ReadFile(filepath.Join(fake, "sid"))
	if result.Status != "review" || result.Session == nil || result.Session.Driver != "claude-code" || result.Session.SessionID != strings.TrimSpace(string(sid)) {
		t.Fatalf("result = %+v session %+v", result, result.Session)
	}
	worktree := filepath.Join(dir, ".trilha", "runs", "TASK-001", "wt")
	if status := sh(t, worktree, "git", "status", "--porcelain", "--ignored"); strings.Contains(status, "claude-settings") {
		t.Fatalf("settings file in the worktree: %s", status)
	}
	if files := sh(t, worktree, "git", "ls-files"); strings.Contains(files, "claude-settings") {
		t.Fatalf("settings file committed: %s", files)
	}
	if _, err := os.Stat(filepath.Join(dir, ".trilha", "runs", "TASK-001", "claude-settings.json")); err != nil {
		t.Fatalf("settings file not beside the worktree: %v", err)
	}
}

// DV-02/DV-04 end to end: an agent runs `trilha-runner ask`, `note`,
// `overview` and `attention` from its PATH; the cloud gets the attention
// badge while the run is going and a needs_input result — not passed, not
// failed — with the question, the note, the overview and the diff counts.
// Then the answer comes in a continuation, and the run reaches review.
func TestAgentCommandsReachTheCloudAndTheAnswerContinues(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	useBinary(t)
	dir := gitRepo(t)
	os.WriteFile(filepath.Join(dir, ".trilha", "tasks", "TASK-001.md"), []byte("---\nid: TASK-001\ntitle: Greeting\nstatus: ready\nacceptance:\n  - greeting\nchecks:\n  - \"sh -c \\\"grep -q Hello GREETING.txt\\\"\"\n---\n"), 0o644)
	sh(t, dir, "git", "add", "-A")
	sh(t, dir, "git", "commit", "-q", "-m", "greeting task")
	agent := filepath.Join(t.TempDir(), "agent.sh")
	os.WriteFile(agent, []byte(`#!/bin/sh
prompt=$(cat)
case "$prompt" in
  *"> Hello, customer"*) echo "Hello, customer" > GREETING.txt; exit 0;;
esac
echo draft > DRAFT.txt
trilha-runner note "The greeting is not in the repository"
trilha-runner overview "Waiting to know the greeting"
trilha-runner attention "needs the customer's greeting"
trilha-runner ask "Which greeting does the customer prefer?"
`), 0o700)
	cloud := &fakeCloud{items: []queue.Item{{ID: "run-000001", Project: "demo", TaskID: "TASK-001", Kind: "implement"}}}
	cloud.work(t, dir, "exec", "sh "+agent)

	cloud.mutex.Lock()
	result := cloud.results["run-000001"]
	var attention string
	for _, p := range cloud.progress {
		if p.Attention != nil {
			attention = p.Attention.Reason
		}
	}
	cloud.mutex.Unlock()
	if result.Status != "needs_input" || result.Passed || result.Error != "" || result.Question == nil || result.Question.Text != "Which greeting does the customer prefer?" {
		t.Fatalf("result = %+v question %+v", result, result.Question)
	}
	for _, attempt := range result.Attempts {
		if attempt.Status == "failed" {
			t.Fatalf("a question counted as a failed attempt: %+v", result.Attempts)
		}
	}
	if len(result.Notes) != 1 || result.Notes[0].Source != "ai" || result.Overview != "Waiting to know the greeting" {
		t.Fatalf("memory = %+v %q", result.Notes, result.Overview)
	}
	if result.DiffStats == nil || result.DiffStats.Files != 1 || attention != "needs the customer's greeting" {
		t.Fatalf("diff %+v attention %q", result.DiffStats, attention)
	}

	cloud.items = []queue.Item{{ID: "run-000002", Project: "demo", TaskID: "TASK-001",
		Continuation: &execution.Continuation{Question: result.Question.Text, Answer: "Hello, customer", By: "operator@example.com"}}}
	cloud.work(t, dir, "exec", "sh "+agent)
	cloud.mutex.Lock()
	defer cloud.mutex.Unlock()
	if next := cloud.results["run-000002"]; next.Status != "review" || !next.Passed {
		t.Fatalf("continuation = %+v", next)
	}
}
