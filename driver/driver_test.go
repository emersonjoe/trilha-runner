package driver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-spec/agent"
	"github.com/emersonjoe/trilha-spec/task"
)

func job(t *testing.T) Job {
	t.Helper()
	return Job{Task: &task.Task{ID: "TASK-001", Title: "x"}, Prompt: "# Task TASK-001 — x\n\nbody", Dir: t.TempDir()}
}

func TestClaudeCodeUsesFixedArgvAndRedactsCredential(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	root := t.TempDir()
	program := filepath.Join(root, "claude")
	if err := os.WriteFile(program, []byte("#!/bin/sh\necho args:$*\necho token:$ANTHROPIC_API_KEY\ncat >/dev/null\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	j := job(t)
	j.Command = "claude --unsafe ignored"
	j.AI = &AIConfig{Provider: "anthropic", Credential: "top-secret", Model: "claude-sonnet-5"}
	output, err := (ClaudeCode{}).Execute(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.Text, "args:-p - --permission-mode acceptEdits --model claude-sonnet-5") || strings.Contains(output.Text, "--unsafe") {
		t.Fatalf("preset argv was not fixed: %q", output.Text)
	}
	if strings.Contains(output.Text, "top-secret") || !strings.Contains(output.Text, "[REDACTED]") {
		t.Fatalf("credential leaked: %q", output.Text)
	}
}

func TestAIRejectsForbiddenHostBeforeCallingProvider(t *testing.T) {
	j := job(t)
	j.AI = &AIConfig{BaseURL: "https://api.example.us/v1", Credential: "secret", AllowedHosts: []string{"gateway.example.br"}}
	_, err := (AI{}).Execute(context.Background(), j)
	var policy *PolicyError
	if !errors.As(err, &policy) || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("error = %v", err)
	}
}

func TestEcho(t *testing.T) {
	j := job(t)
	d, err := New("echo")
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Execute(context.Background(), j)
	if err != nil || !strings.Contains(out.Text, "TASK-001") {
		t.Fatalf("%+v %v", out, err)
	}
	if b, _ := os.ReadFile(filepath.Join(j.Dir, "TRILHA_RUN.md")); string(b) != "# Task TASK-001 — x\n" {
		t.Fatalf("file = %q", b)
	}
	if _, err := New("nope"); err == nil {
		t.Fatal("unknown driver accepted")
	}
}

func TestExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	j := job(t)
	j.Command = `sh -c "cat > prompt.txt; echo done $TRILHA_TASK"`
	out, err := Exec{}.Execute(context.Background(), j)
	if err != nil || strings.TrimSpace(out.Text) != "done TASK-001" || out.Meta["exit_code"] != "0" {
		t.Fatalf("%+v %v", out, err)
	}
	if b, _ := os.ReadFile(filepath.Join(j.Dir, "prompt.txt")); string(b) != j.Prompt {
		t.Fatalf("prompt = %q", b)
	}
	j.Command = `sh -c "exit 4"`
	if out, err := (Exec{}).Execute(context.Background(), j); err == nil || out.Meta["exit_code"] != "4" {
		t.Fatalf("%+v %v", out, err)
	}
	j.Command = ""
	j.Agent = &agent.Agent{Name: "a", Role: "r", Command: "true"}
	if _, err := (Exec{}).Execute(context.Background(), j); err != nil {
		t.Fatal(err)
	}
}

func TestToolsStayInside(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("hi"), 0o644)
	ctx := context.Background()
	if out, err := readFile(root).Func(ctx, []byte(`{"path":"a.txt"}`)); err != nil || out != "hi" {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := readFile(root).Func(ctx, []byte(`{"path":"../etc/passwd"}`)); err == nil {
		t.Fatal("escaped the worktree")
	}
	if _, err := writeFile(root).Func(ctx, []byte(`{"path":"sub/b.txt","content":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if out, _ := listFiles(root).Func(ctx, []byte(`{}`)); !strings.Contains(out, "sub/b.txt") {
		t.Fatalf("list = %q", out)
	}
	if runtime.GOOS != "windows" {
		if out, _ := runCommand(root, []string{"go version"}, nil).Func(ctx, []byte(`{"command":"go version"}`)); !strings.Contains(out, `"exit_code":0`) {
			t.Fatalf("run = %q", out)
		}
		if _, err := runCommand(root, []string{"go version"}, nil).Func(ctx, []byte(`{"command":"sh -c \"exit 2\""}`)); err == nil {
			t.Fatal("non-allow-listed shell command was accepted")
		}
	}
}

func TestClaudeCodeOAuthProviderSelectsTokenAndDropsBadModel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	root := t.TempDir()
	program := filepath.Join(root, "claude")
	if err := os.WriteFile(program, []byte("#!/bin/sh\necho args:$*\necho oauth:$CLAUDE_CODE_OAUTH_TOKEN key:$ANTHROPIC_API_KEY\ncat >/dev/null\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	j := job(t)
	j.AI = &AIConfig{Provider: "claude-code-oauth", Credential: "top-secret", Model: "--dangerously-skip-permissions"}
	output, err := (ClaudeCode{}).Execute(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.Text, "--dangerously") || !strings.Contains(output.Text, "args:-p - --permission-mode acceptEdits --output-format json --session-id ") {
		t.Fatalf("a model that is not a model reached argv: %q", output.Text)
	}
	if !strings.Contains(output.Text, "oauth:[REDACTED] key:\n") {
		t.Fatalf("oauth credential not routed to CLAUDE_CODE_OAUTH_TOKEN: %q", output.Text)
	}
	if !IsClaudeCode("claude-code-oauth") || !IsClaudeCode("claude-code") || IsClaudeCode("anthropic") {
		t.Fatal("IsClaudeCode")
	}
}

// Claude Code reports what a run used in its JSON result: the model it
// resolved, the tokens (cache writes and reads are input too) and the cost
// in dollars. Before, the preset asked for plain text and every claude-code
// run reached the control plane with no model, tokens or cost.
func TestClaudeCodeReportsModelTokensAndCostFromItsJSONResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	root := t.TempDir()
	result := `{"type":"result","subtype":"success","is_error":false,"result":"Wrote GREETING.txt with top-secret","num_turns":3,"total_cost_usd":0.03309375,"usage":{"input_tokens":10,"output_tokens":40,"cache_creation_input_tokens":26307,"cache_read_input_tokens":5},"modelUsage":{"claude-haiku-4-5-20251001":{"inputTokens":10,"outputTokens":40,"costUSD":0.03309375}}}`
	script := "#!/bin/sh\ncat >/dev/null\necho \"args:$*\" >&2\necho 'a warning on stderr' >&2\nprintf '%s\\n' '" + result + "'\n"
	if err := os.WriteFile(filepath.Join(root, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	j := job(t)
	j.AI = &AIConfig{Provider: "claude-code", Credential: "top-secret", Model: "haiku"}
	output, err := (ClaudeCode{}).Execute(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	if output.Text != "Wrote GREETING.txt with [REDACTED]" {
		t.Fatalf("the agent's final message is the JSON result's text, redacted: %q", output.Text)
	}
	want := map[string]string{"model": "claude-haiku-4-5-20251001", "provider": "anthropic", "input_tokens": "26322", "output_tokens": "40", "tokens": "26362", "cost_usd": "0.03309375", "turns": "3"}
	for key, value := range want {
		if output.Meta[key] != value {
			t.Errorf("Meta[%q] = %q, want %q", key, output.Meta[key], value)
		}
	}
}

// An older Claude Code that ignores --output-format still works: its text is
// the report, and nothing is invented about what it used.
func TestClaudeCodeWithoutAJSONResultKeepsTheText(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "claude"), []byte("#!/bin/sh\ncat >/dev/null\necho 'plain answer'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := (ClaudeCode{}).Execute(context.Background(), job(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.Text) != "plain answer" || output.Meta["cost_usd"] != "" || output.Meta["model"] != "" {
		t.Fatalf("text = %q, meta = %v", output.Text, output.Meta)
	}
}
