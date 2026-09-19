package driver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha/ai"
)

const secret = "sk-test-0123456789abcdef"

func TestAccessEnvByProvider(t *testing.T) {
	openai := &Access{Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1", Model: "m", Credential: secret}
	env, err := openai.Env()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"OPENAI_API_KEY=" + secret, "OPENAI_BASE_URL=https://openrouter.ai/api/v1", "OPENAI_MODEL=m", "TRILHA_AI_MODEL=m"}
	if strings.Join(env, " ") != strings.Join(want, " ") {
		t.Fatalf("env = %v", env)
	}

	anthropic := &Access{Provider: "anthropic", BaseURL: "https://api.anthropic.com", Model: "claude", Credential: secret}
	env, _ = anthropic.Env()
	if env[0] != "ANTHROPIC_API_KEY="+secret || !strings.Contains(strings.Join(env, " "), "ANTHROPIC_BASE_URL=") {
		t.Fatalf("env = %v", env)
	}

	// claude-code takes an OAuth token under its own name.
	code := &Access{Provider: "claude-code", Credential: secret}
	env, _ = code.Env()
	if len(env) != 1 || env[0] != "CLAUDE_CODE_OAUTH_TOKEN="+secret {
		t.Fatalf("env = %v", env)
	}

	// A nil access adds nothing and refuses nothing.
	if env, err := (*Access)(nil).Env(); env != nil || err != nil {
		t.Fatalf("%v %v", env, err)
	}

	// The variable name is checked like a deployment secret's.
	bad := &Access{CredentialEnv: "not a name", Credential: secret}
	if _, err := bad.Env(); !errors.Is(err, ErrPolicy) {
		t.Fatalf("err = %v", err)
	}
}

func TestAccessResidency(t *testing.T) {
	a := &Access{AllowedHosts: []string{"gateway.example.br", "api.interno.br"}}
	if err := a.Allows("https://gateway.example.br/v1"); err != nil {
		t.Fatal(err)
	}
	// Case and port do not smuggle a host past the list.
	if err := a.Allows("https://GATEWAY.example.br:8443/v1"); err != nil {
		t.Fatal(err)
	}
	err := a.Allows("https://api.openai.com/v1")
	if !errors.Is(err, ErrPolicy) || !strings.Contains(err.Error(), "api.openai.com") {
		t.Fatalf("err = %v", err)
	}
	// No list means the worker's own configuration decides, as before.
	if err := (&Access{}).Allows("https://api.openai.com/v1"); err != nil {
		t.Fatal(err)
	}
	// An access whose own base URL is forbidden refuses before any run.
	if _, err := (&Access{AllowedHosts: []string{"gateway.example.br"}, BaseURL: "https://api.openai.com/v1", Credential: secret}).Env(); !errors.Is(err, ErrPolicy) {
		t.Fatalf("err = %v", err)
	}
}

// The credential reaches the agent's process and never the captured output.
func TestExecAccessReachesTheProcessAndNotTheLog(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	j := job(t)
	j.Access = &Access{Provider: "claude-code", Credential: secret}
	// A script, because a command line is argv here: no shell, no quoting.
	os.WriteFile(filepath.Join(j.Dir, "agent.sh"), []byte(
		"printf %s \"$CLAUDE_CODE_OAUTH_TOKEN\" > token.txt\necho \"the key is $CLAUDE_CODE_OAUTH_TOKEN\"\n"), 0o755)
	j.Command = "sh agent.sh"
	out, err := Exec{}.Execute(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(j.Dir, "token.txt")); string(b) != secret {
		t.Fatalf("the agent did not receive the credential: %q", b)
	}
	if strings.Contains(out.Text, secret) {
		t.Fatalf("the credential is in the captured output: %q", out.Text)
	}
	if !strings.Contains(out.Text, "[REDACTED]") {
		t.Fatalf("output = %q", out.Text)
	}
}

// A forbidden endpoint stops the exec driver before the agent starts.
func TestExecRefusesForbiddenHost(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	j := job(t)
	j.Access = &Access{BaseURL: "https://api.openai.com/v1", Credential: secret, AllowedHosts: []string{"gateway.example.br"}}
	j.Command = `sh -c "touch ran.txt"`
	out, err := Exec{}.Execute(context.Background(), j)
	if !errors.Is(err, ErrPolicy) {
		t.Fatalf("err = %v", err)
	}
	if out.Meta["stage"] != "policy" || out.Meta["base_url"] != "https://api.openai.com/v1" {
		t.Fatalf("meta = %v", out.Meta)
	}
	if strings.Contains(out.Meta["allowed_hosts"]+out.Meta["base_url"], secret) {
		t.Fatal("the credential leaked into the evidence")
	}
	if _, err := os.Stat(filepath.Join(j.Dir, "ran.txt")); err == nil {
		t.Fatal("the agent ran despite the refusal")
	}
}

// The ai driver refuses the same way, before the first request.
func TestAIRefusesForbiddenHost(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	j := job(t)
	j.Access = &Access{BaseURL: srv.URL + "/v1", Credential: secret, AllowedHosts: []string{"gateway.example.br"}}
	out, err := AI{}.Execute(context.Background(), j)
	if !errors.Is(err, ErrPolicy) || out.Meta["stage"] != "policy" {
		t.Fatalf("%+v %v", out, err)
	}
	if called {
		t.Fatal("the model was called despite the refusal")
	}
}

// A run's access wins over the worker's environment: one worker serving two
// projects does not spend one project's key on the other's endpoint.
func TestAIUsesTheRunsAccess(t *testing.T) {
	var gotAuth, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req struct{ Model string }
		json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer srv.Close()
	t.Setenv("OPENAI_BASE_URL", "https://api.openai.com/v1")
	t.Setenv("OPENAI_API_KEY", "the-worker-key")
	j := job(t)
	j.Access = &Access{BaseURL: srv.URL + "/v1", Credential: secret, Model: "gateway-model",
		AllowedHosts: []string{"127.0.0.1", "localhost"}}
	out, err := AI{MaxTurns: 1}.Execute(context.Background(), j)
	if err != nil {
		t.Fatalf("%+v %v", out, err)
	}
	if gotAuth != "Bearer "+secret {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotModel != "gateway-model" {
		t.Fatalf("model = %q", gotModel)
	}
}

// The preset's argv is the runner's, not the manifest's.
func TestClaudeCodePresetArgvIsFixed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	d, err := New("claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name() != "claude-code" {
		t.Fatalf("name = %q", d.Name())
	}
	if strings.Join(ClaudeCodeArgv, " ") != "claude -p - --permission-mode acceptEdits" {
		t.Fatalf("argv = %v", ClaudeCodeArgv)
	}
	// A manifest command is ignored: the preset is the argv.
	j := job(t)
	j.Command = `sh -c "touch manifest-ran.txt"`
	out, _ := d.Execute(context.Background(), j)
	if _, err := os.Stat(filepath.Join(j.Dir, "manifest-ran.txt")); err == nil {
		t.Fatal("the preset ran the manifest's command")
	}
	if out.Meta["command"] != strings.Join(ClaudeCodeArgv, " ") || out.Meta["driver"] != "claude-code" {
		t.Fatalf("meta = %v", out.Meta)
	}
}

func TestRedactKeepsShortValues(t *testing.T) {
	if got := Redact("token abc", []string{"abc"}); got != "token abc" {
		t.Fatalf("a short value must not be redacted away: %q", got)
	}
	if got := Redact("token "+secret, []string{secret}); got != "token [REDACTED]" {
		t.Fatalf("got %q", got)
	}
}

var _ = ai.DefaultBaseURL
