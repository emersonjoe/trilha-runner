package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-runner/worktree"
	"github.com/emersonjoe/trilha-spec/spec"
)

// DV-13a end to end: `doctor --worktrees` reports and deletes nothing;
// `--prune-orphans` skips an orphan with unmerged work and exits 12 (with
// --json on stdout); `--force-unmerged` removes it and exits 0.
func TestDoctorReportsAndExitsTwelveWhenItSkips(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := gitRepo(t)
	m := worktree.Manager{Layout: spec.Layout{Root: dir}}
	w, err := m.Create(context.Background(), "TASK-009")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(w.Path, "work.txt"), []byte("unmerged\n"), 0o644)
	sh(t, w.Path, "git", "add", "-A")
	sh(t, w.Path, "git", "commit", "-q", "-m", "unmerged")
	doctor := func(args ...string) (string, int) {
		cmd := exec.Command(bin, append([]string{"doctor", "--worktrees", "--default-branch", "main"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.Output()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return string(out), exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), 0
	}
	out, code := doctor()
	if code != 0 || !strings.Contains(out, "TASK-009") || !strings.Contains(out, "orphaned") || !strings.Contains(out, "removed 0, skipped 0") {
		t.Fatalf("report (exit %d):\n%s", code, out)
	}
	if _, err := os.Stat(w.Path); err != nil {
		t.Fatal("a report deleted the worktree")
	}
	out, code = doctor("--prune-orphans", "--json")
	var report worktree.DoctorReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if code != 12 || report.Skipped != 1 || report.Entries[0].Action != "skipped" {
		t.Fatalf("prune (exit %d): %+v", code, report)
	}
	if out, code = doctor("--prune-orphans", "--force-unmerged"); code != 0 || !strings.Contains(out, "removed 1") {
		t.Fatalf("forced (exit %d):\n%s", code, out)
	}
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Fatal("the forced prune kept the worktree")
	}
	if out, code = doctor("--prune-older-than", "soon"); code != 1 {
		t.Fatalf("a bad duration was accepted (exit %d): %s", code, out)
	}
}
