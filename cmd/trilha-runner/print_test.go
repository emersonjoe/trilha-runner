package main

import (
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-spec/task"

	"github.com/emersonjoe/trilha-runner/runner"
)

// A run that failed before its worktree existed has no branch to show.
func TestPrintResultWithoutWorktree(t *testing.T) {
	var out strings.Builder
	printResult(&out, &runner.Result{Task: "TASK-001", Status: task.Failed, Driver: "echo"})
	if got := out.String(); strings.Contains(got, "branch") || !strings.HasPrefix(got, "TASK-001 → failed (driver echo") {
		t.Fatalf("output:\n%s", got)
	}

	out.Reset()
	printResult(&out, &runner.Result{Task: "TASK-001", Status: task.Review, Driver: "echo", Branch: "trilha/task-001", Worktree: "/p/.trilha/runs/TASK-001/wt"})
	if got := out.String(); !strings.Contains(got, "branch trilha/task-001 in /p/.trilha/runs/TASK-001/wt\n") {
		t.Fatalf("output:\n%s", got)
	}
}
