package main

import (
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

// TRILHA_E2E_CLAUDE=1: the claude-code driver with the local Claude Code
// login (TRILHA_E2E_CLAUDE_MODEL, default haiku). It spends real money.
func TestLiveClaudeCodeAsksAndResumesTheSession(t *testing.T) {
	if os.Getenv("TRILHA_E2E_CLAUDE") != "1" {
		t.Skip("opt-in: set TRILHA_E2E_CLAUDE=1 with Claude Code logged in (it costs money)")
	}
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	model := os.Getenv("TRILHA_E2E_CLAUDE_MODEL")
	if model == "" {
		model = "haiku"
	}
	liveCase(t, "", &queue.AI{Provider: "claude-code", Model: model})
}
