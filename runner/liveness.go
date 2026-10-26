package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/emersonjoe/trilha-runner/internal/taskcompat"
	"github.com/emersonjoe/trilha-spec/task"
)

// A run marks its task with the runner's process id while it executes, in
// .trilha/runs/<task>/runner.pid. A task found `running` whose mark is gone,
// or names a process that no longer exists, was left behind by a runner that
// died (RM-012): it can be run again.

func (r *Runner) pidFile(id string) string {
	return filepath.Join(r.Layout.Runs(), id, "runner.pid")
}

func (r *Runner) markRunning(id string) func() {
	path := r.pidFile(id)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
	return func() { _ = os.Remove(path) }
}

// liveRunner answers the pid of a process still running the task, or 0.
func (r *Runner) liveRunner(id string) int {
	data, err := os.ReadFile(r.pidFile(id))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	if pid == os.Getpid() {
		return pid
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return 0
	}
	if err := process.Signal(syscall.Signal(0)); err != nil && !errors.Is(err, syscall.EPERM) {
		return 0
	}
	return pid
}

// resumeOrphan moves a task left `running` by a dead runner back to ready,
// with a note saying so. It answers whether it did.
func (r *Runner) resumeOrphan(t *task.Task) bool {
	if t.Status != task.Running || r.liveRunner(t.ID) != 0 {
		return false
	}
	if err := taskcompat.Move(r.Store, t, task.Ready); err != nil {
		return false
	}
	t.Status = task.Ready
	note := fmt.Sprintf("resumed: %s was left running with no live runner process; moved back to ready", t.ID)
	r.logf("%s", note)
	_, _, _ = task.Record(r.Layout, task.Evidence{Task: t.ID, Kind: "note", By: r.By, Note: note})
	return true
}
