package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-runner/queue"
	"github.com/emersonjoe/trilha-spec/execution"
)

// The live cases run a real model and are opt-in: they need a local service
// (Ollama) or a logged-in agent (Claude Code, which costs money), so CI and
// `make ci` never run them. Small local models are not deterministic, so each
// case judges the protocol path — did the run end needs_input with a question,
// did the answer lead to passing checks — and tries up to three times,
// logging every attempt.

// liveTries answers how many times a live case may try: three, or
// TRILHA_E2E_TRIES (1 to 3) to spend less.
func liveTries() int {
	if n, err := strconv.Atoi(os.Getenv("TRILHA_E2E_TRIES")); err == nil && n >= 1 && n <= 3 {
		return n
	}
	return 3
}

// greetingRepo is a tiny synthetic project whose one task cannot be done
// without asking: the greeting is nowhere in it.
func greetingRepo(t *testing.T) string {
	t.Helper()
	dir := gitRepo(t)
	task := `---
id: TASK-001
title: Write the customer's preferred greeting into GREETING.txt
status: ready
acceptance:
  - GREETING.txt contains exactly the customer's preferred greeting
checks:
  - "grep -qi 'bom dia, cliente' GREETING.txt"
---

You do not know the customer's preferred greeting, and it is not anywhere in this repository.
Do not guess it. Ask for it (one question), then stop. When you are given the answer, write it,
exactly as given, into GREETING.txt and finish.
`
	os.WriteFile(filepath.Join(dir, ".trilha", "tasks", "TASK-001.md"), []byte(task), 0o644)
	sh(t, dir, "git", "add", "-A")
	sh(t, dir, "git", "commit", "-q", "-m", "greeting task")
	return dir
}

// liveFlow runs the question, then the answer, on a fresh repository, and
// answers whether each half followed the protocol.
func liveFlow(t *testing.T, try int, drv string, ai *queue.AI) (asked, continued bool) {
	dir := greetingRepo(t)
	cloud := &fakeCloud{items: []queue.Item{{ID: "run-000001", Project: "demo", TaskID: "TASK-001", AI: ai}}}
	defer assertKeyNeverLeaked(t, ai, dir, cloud)
	cloud.work(t, dir, drv, "")
	first := cloud.results["run-000001"]
	t.Logf("try %d, question run: status=%s passed=%v question=%+v session=%+v error=%q", try, first.Status, first.Passed, first.Question, first.Session, first.Error)
	if first.Status != string(execution.NeedsInput) || first.Question == nil || strings.TrimSpace(first.Question.Text) == "" {
		return false, false
	}
	cloud.items = []queue.Item{{ID: "run-000002", Project: "demo", TaskID: "TASK-001", AI: ai,
		Continuation: &execution.Continuation{Session: first.Session, Question: first.Question.Text, Answer: "Bom dia, cliente!", By: "operator@example.com"}}}
	cloud.work(t, dir, drv, "")
	second := cloud.results["run-000002"]
	greeting, _ := os.ReadFile(filepath.Join(dir, ".trilha", "runs", "TASK-001", "wt", "GREETING.txt"))
	t.Logf("try %d, answer run: status=%s passed=%v GREETING.txt=%q session=%+v error=%q", try, second.Status, second.Passed, greeting, second.Session, second.Error)
	return true, second.Status == "review" && second.Passed
}

// assertKeyNeverLeaked fails the case when the API key handed to the run
// shows up anywhere it could stay.
func assertKeyNeverLeaked(t *testing.T, ai *queue.AI, dir string, cloud *fakeCloud) {
	t.Helper()
	if ai == nil {
		return
	}
	for _, where := range keyLeaks(ai.Credential, dir, cloud) {
		t.Errorf("the API key leaked into %s", where)
	}
}

// keyLeaks answers every place the key appears: a file in the repository
// (worktree, agent log, outbox, evidence, checkpoint, settings) or what the
// control plane received (progress and results).
func keyLeaks(credential, dir string, cloud *fakeCloud) []string {
	if len(credential) < 16 {
		return nil
	}
	key := []byte(credential)
	var leaks []string
	filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if data, err := os.ReadFile(path); err == nil && bytes.Contains(data, key) {
			rel, _ := filepath.Rel(dir, path)
			leaks = append(leaks, rel)
		}
		return nil
	})
	cloud.mutex.Lock()
	sent, _ := json.Marshal(struct {
		Progress []queue.Progress
		Results  map[string]queue.Result
	}{cloud.progress, cloud.results})
	cloud.mutex.Unlock()
	if bytes.Contains(sent, key) {
		leaks = append(leaks, "what the control plane received")
	}
	return leaks
}

// The live cases' leak guard is itself tested, offline: a guard that never
// fires would prove nothing.
func TestKeyLeakGuardFindsTheKeyInFilesAndInWhatTheCloudReceived(t *testing.T) {
	const key = "sk-ant-api03-SYNTHETIC0000000000000000000000-FAKE"
	dir := t.TempDir()
	cloud := &fakeCloud{results: map[string]queue.Result{"run-000001": {Status: "review"}}}
	os.MkdirAll(filepath.Join(dir, ".trilha", "runs", "TASK-001"), 0o755)
	os.WriteFile(filepath.Join(dir, ".trilha", "runs", "TASK-001", "agent.log"), []byte("clean log"), 0o644)
	if leaks := keyLeaks(key, dir, cloud); len(leaks) != 0 {
		t.Fatalf("a clean run reported leaks: %v", leaks)
	}
	os.WriteFile(filepath.Join(dir, ".trilha", "runs", "TASK-001", "agent.log"), []byte("env: ANTHROPIC_API_KEY="+key), 0o644)
	cloud.progress = []queue.Progress{{Message: "using " + key}}
	leaks := keyLeaks(key, dir, cloud)
	if len(leaks) != 2 || leaks[0] != filepath.Join(".trilha", "runs", "TASK-001", "agent.log") || leaks[1] != "what the control plane received" {
		t.Fatalf("the guard missed a leak: %v", leaks)
	}
	if keyLeaks("", dir, cloud) != nil {
		t.Fatal("no credential, nothing to look for")
	}
}

func liveCase(t *testing.T, drv string, ai *queue.AI) {
	useBinary(t)
	tries := liveTries()
	for try := 1; try <= tries; try++ {
		asked, continued := liveFlow(t, try, drv, ai)
		if asked && continued {
			t.Logf("protocol path held on try %d of %d", try, tries)
			return
		}
	}
	t.Fatalf("the model did not follow ask → answer → checks in %d tries (see the log above)", tries)
}

// TRILHA_E2E_OLLAMA=1: the ai driver against local Ollama
// (http://localhost:11434/v1, TRILHA_E2E_OLLAMA_MODEL, default qwen2.5:7b).
func TestLiveOllamaAsksAndTheAnswerContinues(t *testing.T) {
	if os.Getenv("TRILHA_E2E_OLLAMA") != "1" {
		t.Skip("opt-in: set TRILHA_E2E_OLLAMA=1 with Ollama running on localhost:11434")
	}
	model := os.Getenv("TRILHA_E2E_OLLAMA_MODEL")
	if model == "" {
		model = "qwen2.5:7b"
	}
	// git and sh only: no trilha-spec MCP server, whose extra tools only
	// distract a small model from the ones this case is about.
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(git)+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	liveCase(t, "ai", &queue.AI{Provider: "openai", BaseURL: "http://localhost:11434/v1", Model: model, Credential: "ollama-local-FAKE"})
}

// TRILHA_E2E_CLAUDE=1: the claude-code driver (TRILHA_E2E_CLAUDE_MODEL,
// default haiku). It spends real money. With ANTHROPIC_API_KEY set, the key
// reaches the agent the way a control plane's credential does (the claim's
// AI config); without it, the local Claude Code login is used.
func TestLiveClaudeCodeAsksAndResumesTheSession(t *testing.T) {
	if os.Getenv("TRILHA_E2E_CLAUDE") != "1" {
		t.Skip("opt-in: set TRILHA_E2E_CLAUDE=1 with ANTHROPIC_API_KEY or Claude Code logged in (it costs money)")
	}
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	model := os.Getenv("TRILHA_E2E_CLAUDE_MODEL")
	if model == "" {
		model = "haiku"
	}
	liveCase(t, "", &queue.AI{Provider: "claude-code", Model: model, Credential: os.Getenv("ANTHROPIC_API_KEY")})
}

// buggyRepo is a tiny synthetic Go project whose task branch carries an
// obvious planted bug: Last indexes one past the end, and its own test
// already names the edge case that panics.
func buggyRepo(t *testing.T) (dir, tip string) {
	t.Helper()
	dir = gitRepo(t)
	os.WriteFile(filepath.Join(dir, ".trilha", "tasks", "TASK-001.md"), []byte("---\nid: TASK-001\ntitle: Add Last to the calc package\nstatus: review\nacceptance:\n  - Last answers the last element of a non-empty slice\nchecks:\n  - \"sh -c \\\"test -f calc.go\\\"\"\n---\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.local/calc\n\ngo 1.22\n"), 0o644)
	sh(t, dir, "git", "add", "-A")
	sh(t, dir, "git", "commit", "-q", "-m", "calc module")
	sh(t, dir, "git", "switch", "-q", "-c", "trilha/task-001")
	os.WriteFile(filepath.Join(dir, "calc.go"), []byte(`package calc

// Last answers the last element of values.
func Last(values []int) int {
	return values[len(values)]
}
`), 0o644)
	os.WriteFile(filepath.Join(dir, "calc_test.go"), []byte(`package calc

import "testing"

func TestLastOfOne(tt *testing.T) {
	if got := Last([]int{7}); got != 7 {
		tt.Fatalf("Last([7]) = %d, want 7", got)
	}
}
`), 0o644)
	sh(t, dir, "git", "add", "-A")
	sh(t, dir, "git", "commit", "-q", "-m", "TASK-001: add Last")
	tip = strings.TrimSpace(sh(t, dir, "git", "rev-parse", "HEAD"))
	sh(t, dir, "git", "switch", "-q", "main")
	return dir, tip
}

// TRILHA_E2E_OLLAMA=1: a read-only hunt by the ai driver against local
// Ollama finds the planted off-by-one: the run stays read-only (done, the
// branch at the same commit) and reports at least one finding whose path is
// a file of the branch. Up to three tries (two retries), each logged.
func TestLiveOllamaHuntFindsThePlantedBug(t *testing.T) {
	if os.Getenv("TRILHA_E2E_OLLAMA") != "1" {
		t.Skip("opt-in: set TRILHA_E2E_OLLAMA=1 with Ollama running on localhost:11434")
	}
	model := os.Getenv("TRILHA_E2E_OLLAMA_MODEL")
	if model == "" {
		model = "qwen2.5:7b"
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(git)+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	useBinary(t)
	ai := &queue.AI{Provider: "openai", BaseURL: "http://localhost:11434/v1", Model: model, Credential: "ollama-local-FAKE"}
	tries := liveTries()
	for try := 1; try <= tries; try++ {
		dir, tip := buggyRepo(t)
		cloud := &fakeCloud{items: []queue.Item{{ID: "run-000001", Project: "demo", TaskID: "TASK-001", AI: ai, Kind: execution.KindHunt, HuntSeed: &execution.HuntSeed{Seed: 1}}}}
		cloud.work(t, dir, "ai", "")
		result := cloud.results["run-000001"]
		branch := strings.TrimSpace(sh(t, dir, "git", "rev-parse", "trilha/task-001"))
		t.Logf("try %d: status=%s passed=%v failure=%q error=%q commit=%s findings=%+v", try, result.Status, result.Passed, result.FailureClass, result.Error, result.Commit, result.Findings)
		if branch != tip {
			t.Fatalf("try %d: the hunt moved the branch", try)
		}
		if result.FailureClass == "policy" {
			t.Fatalf("try %d: the hunt changed the repository: %s", try, result.Error)
		}
		valid := 0
		for _, finding := range result.Findings {
			if finding.Validate() == nil && exec.Command("git", "-C", dir, "cat-file", "-e", tip+":"+finding.Path).Run() == nil {
				valid++
			}
		}
		if result.Status == "done" && result.Passed && result.Commit == tip && valid > 0 {
			t.Logf("hunt held on try %d of %d with %d valid findings", try, tries, valid)
			return
		}
	}
	t.Fatalf("the model did not report a valid finding on a read-only hunt in %d tries (see the log above)", tries)
}
