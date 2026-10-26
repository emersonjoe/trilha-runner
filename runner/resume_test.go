package runner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-spec/task"
)

// RM-012: a worker that died mid-run leaves its task `running` in the
// checkout. With no live runner process behind it, the next run moves it back
// to ready with a note and runs it, instead of refusing it forever. A task
// whose runner is still alive is left alone.
func TestRunResumesATaskLeftRunningWithNoLiveProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := repo(t)
	r, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	r.Driver = driver.Echo{}
	tk, err := r.Store.Create("Write the run file", func(x *task.Task) {
		x.Status = task.Ready
		x.Acceptance = []string{"TRILHA_RUN.md exists"}
		x.Checks = []string{`sh -c "test -f TRILHA_RUN.md"`}
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Store.Move(tk.ID, task.Running); err != nil {
		t.Fatal(err)
	}

	// A live runner (this process) still owns it: refused, untouched.
	pidFile := filepath.Join(r.Layout.Runs(), tk.ID, "runner.pid")
	os.MkdirAll(filepath.Dir(pidFile), 0o755)
	os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o644)
	if _, err := r.Run(context.Background(), tk.ID); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("a task with a live runner was run: %v", err)
	}

	// The runner died: no pid file left behind.
	os.Remove(pidFile)
	res, err := r.Run(context.Background(), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != task.Review || !res.Passed {
		t.Fatalf("res = %+v", res)
	}
	evidence, _ := task.ListEvidence(r.Layout, tk.ID)
	if len(evidence) == 0 || evidence[0].Kind != "note" || !strings.Contains(evidence[0].Note, "no live runner") {
		t.Fatalf("no resume note: %+v", evidence)
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("the finished run left its pid file: %v", err)
	}
}
