package driver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/emersonjoe/trilha-runner/internal/agentlink"
	"github.com/emersonjoe/trilha-spec/execution"
)

// The claude-code preset always runs a session the runner can name: a new
// UUID, or the one a continuation resumes (only a UUID reaches argv). Its
// hooks live in a settings file in the run's directory, outside the worktree,
// and call `trilha-runner hook claude` for the four events the runner follows;
// it allows exactly the agent's runner commands through Bash, since print
// mode has nobody to approve them.
func TestClaudeCodeNamesItsSessionAndGetsHooksOutsideTheWorktree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	root := t.TempDir()
	program := filepath.Join(root, "claude")
	if err := os.WriteFile(program, []byte("#!/bin/sh\necho args:$*\ncat >/dev/null\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	runDir := filepath.Join(t.TempDir(), "runs", "TASK-001")
	self := filepath.Join(t.TempDir(), "it's here", "trilha-runner")

	j := job(t)
	j.RunDir, j.Self = runDir, self
	output, err := (ClaudeCode{}).Execute(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(runDir, ClaudeSettingsFile)
	if !ValidSessionID(output.Meta["session_id"]) || !strings.Contains(output.Text, "--session-id "+output.Meta["session_id"]) {
		t.Fatalf("no named session: %q %v", output.Text, output.Meta)
	}
	if !strings.Contains(output.Text, "--settings "+settings) || strings.HasPrefix(settings, j.Dir) {
		t.Fatalf("settings not passed from outside the worktree: %q", output.Text)
	}
	data, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Hooks map[string][]struct {
			Hooks []struct{ Type, Command string }
		}
		Permissions struct{ Allow []string }
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	want := `'` + strings.ReplaceAll(self, "'", `'\''`) + `' hook claude`
	for _, event := range []string{"PreToolUse", "PostToolUse", "Stop", "StopFailure"} {
		groups := file.Hooks[event]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 || groups[0].Hooks[0].Type != "command" || groups[0].Hooks[0].Command != want {
			t.Errorf("%s hooks = %+v, want %q", event, groups, want)
		}
	}
	if strings.Join(file.Permissions.Allow, ",") != "Bash(trilha-runner ask:*),Bash(trilha-runner note:*),Bash(trilha-runner overview:*),Bash(trilha-runner attention:*),Bash(trilha-runner finding:*),Bash(trilha-runner verdict:*),Bash(trilha-runner reply:*),Bash(trilha-runner artifact:*)" {
		t.Errorf("print mode cannot run the agent's own commands: %v", file.Permissions.Allow)
	}
	if entries, _ := os.ReadDir(j.Dir); len(entries) != 0 {
		t.Fatalf("the preset wrote into the worktree: %v", entries)
	}

	j.Resume = "3f2a9c1e-0000-4000-8000-000000000001"
	output, _ = (ClaudeCode{}).Execute(context.Background(), j)
	if !strings.Contains(output.Text, "--resume "+j.Resume) || strings.Contains(output.Text, "--session-id") || output.Meta["session_id"] != j.Resume {
		t.Fatalf("resume: %q %v", output.Text, output.Meta)
	}
	j.Resume = "--dangerously-skip-permissions"
	output, _ = (ClaudeCode{}).Execute(context.Background(), j)
	if strings.Contains(output.Text, "dangerously") || strings.Contains(output.Text, "--resume") {
		t.Fatalf("a session id that is not a UUID reached argv: %q", output.Text)
	}
}

// fakeModel answers chat completions from a script: each call pops the next
// assistant message.
func fakeModel(t *testing.T, replies ...map[string]any) (*httptest.Server, *int) {
	t.Helper()
	var mutex sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		reply := map[string]any{"role": "assistant", "content": "done"}
		if calls < len(replies) {
			reply = replies[calls]
		}
		calls++
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"index": 0, "message": reply, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func toolCall(id, name, arguments string) map[string]any {
	return map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": arguments}}}}
}

// The ai driver reports its own state — a tool starts and ends, the loop
// stops — and has an `ask` tool: the question reaches the runner, the model
// is told to stop, tools called after asking are refused, and the run is not
// an error.
func TestAIDriverReportsStateAndAsks(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no trilha-spec MCP server
	server, calls := fakeModel(t,
		toolCall("c1", "list_files", `{}`),
		toolCall("c2", "note", `{"text":"GREETING.txt does not exist yet"}`),
		toolCall("c3", "ask", `{"question":"What is the customer's preferred greeting?"}`),
		toolCall("c4", "write_file", `{"path":"GREETING.txt","content":"guess"}`),
		map[string]any{"role": "assistant", "content": "Asked for the greeting."},
	)
	var mutex sync.Mutex
	var got []agentlink.Message
	j := job(t)
	j.AI = &AIConfig{BaseURL: server.URL, Model: "fake-model"}
	j.Signal = func(m agentlink.Message) {
		mutex.Lock()
		got = append(got, m)
		mutex.Unlock()
	}
	output, err := (AI{}).Execute(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 5 || output.Text != "Asked for the greeting." {
		t.Fatalf("calls = %d, output = %q", *calls, output.Text)
	}
	if _, err := os.Stat(filepath.Join(j.Dir, "GREETING.txt")); err == nil {
		t.Fatal("a tool ran after the agent asked")
	}
	var sequence []string
	for _, m := range got {
		if m.Type == agentlink.TypeEvent {
			sequence = append(sequence, string(m.Event))
		} else {
			sequence = append(sequence, m.Type+":"+m.Text)
		}
	}
	want := []string{"tool_start", "tool_end", "tool_start", "note:GREETING.txt does not exist yet", "tool_end", "ask:What is the customer's preferred greeting?", string(execution.EventStop)}
	if strings.Join(sequence, "|") != strings.Join(want, "|") {
		t.Fatalf("signals:\n got %v\nwant %v", sequence, want)
	}
}

// DV-06..DV-08 for the ai driver: a read-only review offers no write tool and
// no ask; finding, verdict, reply and artifact reach the runner as the
// socket commands would; an invalid finding, a reply to an unknown comment
// and a binary artifact name are refused before anything is sent.
func TestAIDriverReportsFindingsVerdictRepliesAndArtifacts(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no trilha-spec MCP server
	server, _ := fakeModel(t,
		toolCall("c1", "write_file", `{"path":"x.txt","content":"no"}`),
		toolCall("c2", "finding", `{"severity":"high","path":"./calc.go","line_start":5,"title":"index out of range","why":"len(values)"}`),
		toolCall("c3", "finding", `{"severity":"low","path":"calc.go","line_start":5,"line_end":5,"title":"nit"}`),
		toolCall("c4", "verdict", `{"verdict":"changes"}`),
		toolCall("c5", "reply", `{"id":"c-9","reply":"done"}`),
		toolCall("c6", "reply", `{"id":"c-1","reply":"fixed the bound"}`),
		toolCall("c7", "artifact", `{"name":"chart.png","content":"x"}`),
		toolCall("c8", "artifact", `{"name":"review.md","content":"# Review\n"}`),
		map[string]any{"role": "assistant", "content": "Reviewed."},
	)
	var mutex sync.Mutex
	var got []agentlink.Message
	j := job(t)
	j.AI = &AIConfig{BaseURL: server.URL, Model: "fake-model"}
	j.Kind, j.ReadOnly, j.ReplyTo = execution.KindReview, true, []string{"c-1"}
	j.Signal = func(m agentlink.Message) {
		mutex.Lock()
		got = append(got, m)
		mutex.Unlock()
	}
	if _, err := (AI{}).Execute(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(j.Dir, "x.txt")); err == nil {
		t.Fatal("a read-only run wrote a file")
	}
	var reported []string
	for _, m := range got {
		switch m.Type {
		case agentlink.TypeFinding:
			reported = append(reported, "finding:"+m.Finding.Path+":"+m.Finding.Title)
			if m.Finding.LineEnd != 5 {
				t.Errorf("line_end = %d", m.Finding.LineEnd)
			}
		case agentlink.TypeVerdict, agentlink.TypeReply:
			reported = append(reported, m.Type+":"+m.ID+m.Text)
		case agentlink.TypeArtifact:
			reported = append(reported, "artifact:"+m.Name+":"+m.Content)
		case agentlink.TypeAsk:
			t.Fatal("a read-only run could ask")
		}
	}
	want := "finding:calc.go:index out of range|verdict:changes|reply:c-1fixed the bound|artifact:review.md:# Review\n"
	if strings.Join(reported, "|") != want {
		t.Fatalf("reported:\n got %q\nwant %q", strings.Join(reported, "|"), want)
	}
}
