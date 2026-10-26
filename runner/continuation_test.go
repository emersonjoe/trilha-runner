package runner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-spec/execution"
	"github.com/emersonjoe/trilha-spec/task"
)

// fakeClaude puts a `claude` on PATH that records its argv and its stdin
// (the prompt) and writes GREETING.txt; with failResume it refuses --resume,
// the way Claude Code does for a session it does not have.
func fakeClaude(t *testing.T, failResume bool) (argv, stdin string) {
	t.Helper()
	bin := t.TempDir()
	argv, stdin = filepath.Join(bin, "argv.txt"), filepath.Join(bin, "stdin.txt")
	script := "#!/bin/sh\necho \"$*\" >> " + argv + "\ncat >> " + stdin + "\necho '---' >> " + stdin + "\n"
	if failResume {
		script += "case \"$*\" in *--resume*) echo 'No conversation found'; exit 1;; esac\n"
	}
	script += "echo hello > GREETING.txt\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argv, stdin
}

func blockedTask(t *testing.T) (*Runner, *task.Task) {
	t.Helper()
	r, err := New(repo(t))
	if err != nil {
		t.Fatal(err)
	}
	r.Driver = driver.ClaudeCode{}
	tk := agentTask(t, r)
	r.Store.Move(tk.ID, task.Running)
	r.Store.Move(tk.ID, task.Blocked) // where a needs_input run left it
	return r, tk
}

// DV-03: the answer to a claude-code question resumes that same session
// (--resume with the id the run reported), with the answer alone, quoted, as
// the prompt; the task blocked by the question runs again; the result names
// the session.
func TestContinuationResumesTheSameClaudeSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	argv, stdin := fakeClaude(t, false)
	r, tk := blockedTask(t)
	session := "3f2a9c1e-0000-4000-8000-000000000001"
	r.Continuation = &execution.Continuation{Session: &execution.Session{Driver: "claude-code", SessionID: session}, Question: "Which greeting?", Answer: "Say hello."}
	res, err := r.Run(context.Background(), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argv)
	prompt, _ := os.ReadFile(stdin)
	if !strings.Contains(string(args), "--resume "+session) || strings.Contains(string(args), "--session-id") {
		t.Fatalf("argv = %s", args)
	}
	if !strings.HasPrefix(string(prompt), "Operator answer to your question") || !strings.Contains(string(prompt), "> Say hello.") || strings.Contains(string(prompt), "# Task") {
		t.Fatalf("prompt = %s", prompt)
	}
	if res.Status != task.Review || res.Session == nil || res.Session.SessionID != session || res.Session.Driver != "claude-code" {
		t.Fatalf("res = %+v session %+v", res, res.Session)
	}
}

// A session of another driver cannot be resumed: a fresh session starts and
// reads the question and the answer, quoted, in its context pack. A resume
// Claude refuses (the session lives on another machine) falls back to the
// same fresh session within the same attempt.
func TestContinuationWithoutAResumableSessionStartsFresh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	for name, c := range map[string]struct {
		session    *execution.Session
		failResume bool
	}{
		"another driver":    {&execution.Session{Driver: "ai", SessionID: "3f2a9c1e-0000-4000-8000-000000000002"}, false},
		"resume refused":    {&execution.Session{Driver: "claude-code", SessionID: "3f2a9c1e-0000-4000-8000-000000000003"}, true},
		"no session at all": {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			argv, stdin := fakeClaude(t, c.failResume)
			r, tk := blockedTask(t)
			r.Continuation = &execution.Continuation{Session: c.session, Question: "Which greeting?", Answer: "Say hello."}
			res, err := r.Run(context.Background(), tk.ID)
			if err != nil {
				t.Fatal(err)
			}
			args, _ := os.ReadFile(argv)
			calls := strings.Split(strings.TrimSpace(string(args)), "\n")
			last := calls[len(calls)-1]
			if !strings.Contains(last, "--session-id ") || strings.Contains(last, "--resume") {
				t.Fatalf("argv = %s", args)
			}
			if c.failResume != (len(calls) == 2) {
				t.Fatalf("calls = %v", calls)
			}
			prompts := strings.Split(string(mustRead(t, stdin)), "---\n")
			prompt := prompts[len(prompts)-2]
			if !strings.Contains(prompt, "# Task") || !strings.Contains(prompt, "## Your earlier question and its answer") || !strings.Contains(prompt, "> Which greeting?") || !strings.Contains(prompt, "> Say hello.") {
				t.Fatalf("prompt = %s", prompt)
			}
			if res.Status != task.Review || res.Attempt != 1 || res.Session == nil || !driver.ValidSessionID(res.Session.SessionID) {
				t.Fatalf("res = %+v", res)
			}
		})
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
