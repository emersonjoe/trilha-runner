package driver

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-spec/task"
)

const sandboxSecret = "sk-ant-oat-SECRET-0123456789"

// Inside a sandbox, no command may carry the project's credential: the prefix
// runs on the host, so its arguments are visible in `ps` to every user on the
// machine. The credential is in the container already, put there when the
// sandbox was prepared.
func TestSandboxedCommandCarriesNoCredential(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := t.TempDir()
	// A stand-in for the docker client that records the argv it was given.
	fake := filepath.Join(dir, "docker")
	logPath := filepath.Join(dir, "argv.log")
	os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> "+logPath+"\n"), 0o700)

	job := Job{
		Task:   &task.Task{ID: "TASK-001", Title: "x"},
		Prompt: "prompt",
		Dir:    dir,
		// What a prepared Docker sandbox hands the driver.
		Prefix: []string{fake, "exec", "-i", "--workdir", "/workspace", "trilha-1-agent"},
		AI:     &AIConfig{Provider: "anthropic-oauth", Credential: sandboxSecret},
	}
	if _, err := (ClaudeCode{}).Execute(context.Background(), job); err != nil {
		t.Fatalf("execute: %v", err)
	}
	argv, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(argv), sandboxSecret) {
		t.Fatalf("the credential is on the host's command line, so `ps` shows it:\n%s", argv)
	}
	// Nor does anything else ride the argv as KEY=VALUE.
	for _, line := range strings.Split(strings.TrimSpace(string(argv)), "\n") {
		if strings.Contains(line, "=") && !strings.HasPrefix(line, "--") {
			t.Errorf("environment on the argv: %q", line)
		}
	}
	// The command itself still gets there.
	if !strings.Contains(string(argv), "claude") {
		t.Fatalf("the agent's command did not reach the sandbox:\n%s", argv)
	}
}

// CredentialEnv is the one place that names the variable, so the runner can put
// it in the sandbox before any command runs.
func TestCredentialEnv(t *testing.T) {
	if got := CredentialEnv(nil); got != nil {
		t.Fatalf("nil config = %v", got)
	}
	if got := CredentialEnv(&AIConfig{}); got != nil {
		t.Fatalf("no credential = %v", got)
	}
	got := CredentialEnv(&AIConfig{Provider: "anthropic", Credential: sandboxSecret})
	if len(got) != 1 || got[0] != "ANTHROPIC_API_KEY="+sandboxSecret {
		t.Fatalf("api key = %v", got)
	}
	got = CredentialEnv(&AIConfig{Provider: "anthropic-oauth", Credential: sandboxSecret})
	if len(got) != 1 || got[0] != "CLAUDE_CODE_OAUTH_TOKEN="+sandboxSecret {
		t.Fatalf("oauth token = %v", got)
	}
}
