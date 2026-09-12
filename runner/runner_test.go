package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-spec/spec"
	"github.com/emersonjoe/trilha-spec/task"
)

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if _, _, err := spec.Init(dir, spec.InitOptions{Name: "demo"}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("demo\n"), 0o644)
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	return dir
}

func TestRunPipeline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := repo(t)
	r, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	r.Driver = driver.Echo{}
	var log []string
	r.Log = func(s string) { log = append(log, s) }
	tk, _ := r.Store.Create("Write the run file", func(x *task.Task) {
		x.Status = task.Ready
		x.Acceptance = []string{"TRILHA_RUN.md exists"}
		x.Checks = []string{`sh -c "test -f TRILHA_RUN.md"`, `sh -c "grep -q TASK-001 TRILHA_RUN.md"`}
	})
	res, err := r.Run(context.Background(), tk.ID)
	if err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(log, "\n"))
	}
	if res.Status != task.Review || !res.Passed || res.Commit == "" || res.Branch != "trilha/task-001" {
		t.Fatalf("res = %+v", res)
	}
	// The maintainer's checkout is untouched; the branch has the work.
	if _, err := os.Stat(filepath.Join(dir, "TRILHA_RUN.md")); err == nil {
		t.Fatal("agent wrote into the main checkout")
	}
	if _, err := os.Stat(filepath.Join(res.Worktree, "TRILHA_RUN.md")); err != nil {
		t.Fatal("no file in the worktree")
	}
	ev, _ := task.ListEvidence(r.Layout, tk.ID)
	if len(ev) != 3 || ev[2].Kind != "run" || ev[2].Meta["commit"] != res.Commit || ev[2].Meta["driver"] != "echo" {
		t.Fatalf("evidence = %+v", ev)
	}
	got, _ := r.Store.Get(tk.ID)
	if got.Status != task.Review {
		t.Fatalf("status = %s", got.Status)
	}
	// Nothing else is ready now.
	if _, err := r.Next(context.Background()); err != ErrNothing {
		t.Fatalf("next = %v", err)
	}
	// Running it again is refused: it is not ready.
	if _, err := r.Run(context.Background(), tk.ID); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("rerun: %v", err)
	}
}

func TestRunFailsChecks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := repo(t)
	r, _ := New(dir)
	r.Driver = driver.Echo{}
	tk, _ := r.Store.Create("Impossible", func(x *task.Task) {
		x.Status = task.Ready
		x.Acceptance = []string{"never"}
		x.Checks = []string{`sh -c "exit 1"`}
	})
	res, err := r.Run(context.Background(), tk.ID)
	if err == nil || res.Status != task.Failed || res.Passed {
		t.Fatalf("%+v %v", res, err)
	}
	// Back to ready and run again: the worktree and branch are reused.
	r.Store.Move(tk.ID, task.Ready)
	if res2, _ := r.Run(context.Background(), tk.ID); res2.Worktree != res.Worktree {
		t.Fatalf("worktree changed: %s vs %s", res2.Worktree, res.Worktree)
	}
}

func TestExecDriverFailureIsRecorded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := repo(t)
	r, _ := New(dir)
	r.Command = `sh -c "echo boom; exit 9"`
	tk, _ := r.Store.Create("Crash", func(x *task.Task) { x.Status = task.Ready; x.Acceptance = []string{"x"}; x.Checks = []string{"true"} })
	res, err := r.Run(context.Background(), tk.ID)
	if err == nil || res.Status != task.Failed {
		t.Fatalf("%+v %v", res, err)
	}
	ev, _ := task.ListEvidence(r.Layout, tk.ID)
	if len(ev) != 1 || ev[0].Kind != "run" || !strings.Contains(ev[0].Note, "execution") || ev[0].Meta["stage"] != "execution" {
		t.Fatalf("evidence = %+v", ev)
	}
}
