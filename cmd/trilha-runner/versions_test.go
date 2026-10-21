package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The fleet panel routes on what each worker can run. A fake `claude` on
// PATH answers its version; a `codex` that never answers is "unknown" within
// the timeout; `echo` and `ai` are the runner itself.
func TestDriverVersionsComeFromTheBinariesOnPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	bin := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write("claude", "echo '1.2.3 (Claude Code)'\necho 'a second line nobody needs'\n")
	write("codex", "sleep 5\necho codex-cli 9.9.9\n")
	// Only the fake binaries and the shell: the real PATH may well have a
	// claude of its own, and the test must not depend on it.
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/bin"+string(os.PathListSeparator)+"/usr/bin")

	start := time.Now()
	got := driverVersions(context.Background(), []string{"ai", "claude-code", "echo", "exec"}, 200*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("a binary that does not answer held the worker for %s", elapsed)
	}
	want := map[string]string{
		"ai":          version,
		"echo":        version,
		"exec":        version,
		"claude-code": "1.2.3 (Claude Code)",
		"claude":      "1.2.3 (Claude Code)",
		"codex":       "unknown",
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("driver_versions[%q] = %q, want %q", key, got[key], value)
		}
	}
	if len(got) != len(want) {
		t.Errorf("driver_versions = %v", got)
	}

	// Nothing on PATH at all: every agent is unknown, the runner still knows itself.
	t.Setenv("PATH", t.TempDir())
	got = driverVersions(context.Background(), []string{"echo", "claude-code"}, 200*time.Millisecond)
	if got["echo"] != version || got["claude-code"] != "unknown" || got["claude"] != "unknown" || got["codex"] != "unknown" {
		t.Fatalf("driver_versions = %v", got)
	}
}
