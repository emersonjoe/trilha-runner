package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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
	r.RetryBase = time.Millisecond // three attempts, not three real backoffs
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

func TestPolicyFailureIsRecordedWithoutRetry(t *testing.T) {
	dir := repo(t)
	runner, _ := New(dir)
	runner.Driver = driver.AI{}
	runner.AI = &driver.AIConfig{BaseURL: "https://outside.example/v1", Credential: "secret", AllowedHosts: []string{"gateway.example.br"}}
	item, _ := runner.Store.Create("Residency", func(current *task.Task) {
		current.Status = task.Ready
		current.Acceptance = []string{"provider allowed"}
		current.Checks = []string{"true"}
	})
	result, err := runner.Run(context.Background(), item.ID)
	if err == nil || result.Status != task.Failed || result.Attempt != 1 || result.FailureClass != "policy" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	evidence, _ := task.ListEvidence(runner.Layout, item.ID)
	if len(evidence) != 1 || evidence[0].Meta["stage"] != "policy" || strings.Contains(evidence[0].Note, "secret") {
		t.Fatalf("evidence=%+v", evidence)
	}
}

func TestExecWithoutCommandIsRecorded(t *testing.T) {
	dir := repo(t)
	r, _ := New(dir)
	tk, _ := r.Store.Create("No command", func(x *task.Task) { x.Status = task.Ready; x.Acceptance = []string{"x"}; x.Checks = []string{"true"} })
	res, err := r.Run(context.Background(), tk.ID)
	if err == nil || !strings.Contains(err.Error(), "no command") || res.Status != task.Failed || res.Attempt != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	ev, _ := task.ListEvidence(r.Layout, tk.ID)
	if len(ev) != 1 || ev[0].Meta["stage"] != "execution" || ev[0].Meta["driver"] != "exec" {
		t.Fatalf("evidence = %+v", ev)
	}
}

// A run that cannot even start still ends with evidence and a failed task
// (principle III): the maintainer finds out why in .trilha/evidence, not
// only in a terminal that may be gone.
func TestNotAGitRepositoryIsRecorded(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := spec.Init(dir, spec.InitOptions{Name: "demo"}); err != nil {
		t.Fatal(err)
	}
	r, _ := New(dir)
	r.Driver = driver.Echo{}
	tk, _ := r.Store.Create("No git", func(x *task.Task) { x.Status = task.Ready; x.Acceptance = []string{"x"}; x.Checks = []string{"true"} })
	res, err := r.Run(context.Background(), tk.ID)
	if err == nil || !strings.Contains(err.Error(), "git repository") || res == nil || res.Status != task.Failed || res.Driver != "echo" {
		t.Fatalf("%+v %v", res, err)
	}
	ev, _ := task.ListEvidence(r.Layout, tk.ID)
	if len(ev) != 1 || ev[0].Kind != "run" || ev[0].Meta["stage"] != "worktree" || ev[0].Meta["driver"] != "echo" || !strings.HasPrefix(ev[0].Note, "worktree: the project must be a git repository") {
		t.Fatalf("evidence = %+v", ev)
	}
	if got, _ := r.Store.Get(tk.ID); got.Status != task.Failed {
		t.Fatalf("status = %s", got.Status)
	}
	if _, err := os.Stat(filepath.Join(r.Layout.Runs(), tk.ID, "wt")); !os.IsNotExist(err) {
		t.Fatalf("a worktree was attempted: %v", err)
	}
}

func TestUnknownDriverIsRecorded(t *testing.T) {
	dir := repo(t)
	r, _ := New(dir)
	os.MkdirAll(r.Layout.Agents(), 0o755)
	os.WriteFile(r.Layout.AgentFile("dev"), []byte("---\nname: dev\nrole: developer\ndriver: nope\n---\n"), 0o644)
	tk, _ := r.Store.Create("Unknown driver", func(x *task.Task) {
		x.Status = task.Ready
		x.Agent = "dev"
		x.Acceptance = []string{"x"}
		x.Checks = []string{"true"}
	})
	res, err := r.Run(context.Background(), tk.ID)
	if err == nil || !strings.Contains(err.Error(), "nope") || res == nil || res.Status != task.Failed || res.Driver != "nope" {
		t.Fatalf("%+v %v", res, err)
	}
	ev, _ := task.ListEvidence(r.Layout, tk.ID)
	if len(ev) != 1 || ev[0].Meta["stage"] != "driver" || ev[0].Meta["driver"] != "nope" || strings.HasPrefix(ev[0].Note, "driver: driver:") {
		t.Fatalf("evidence = %+v", ev)
	}
}

// A task blocked by a dependency did not fail: it is not run, and it stays
// ready with no evidence.
func TestBlockedTaskIsNotFailed(t *testing.T) {
	dir := repo(t)
	r, _ := New(dir)
	r.Driver = driver.Echo{}
	first, _ := r.Store.Create("First", func(x *task.Task) { x.Status = task.Ready; x.Acceptance = []string{"x"} })
	second, _ := r.Store.Create("Second", func(x *task.Task) {
		x.Status = task.Ready
		x.Acceptance = []string{"x"}
		x.DependsOn = []string{first.ID}
	})
	if res, err := r.Run(context.Background(), second.ID); err == nil || res != nil {
		t.Fatalf("%+v %v", res, err)
	}
	if ev, _ := task.ListEvidence(r.Layout, second.ID); len(ev) != 0 {
		t.Fatalf("evidence = %+v", ev)
	}
	if got, _ := r.Store.Get(second.ID); got.Status != task.Ready {
		t.Fatalf("status = %s", got.Status)
	}
}
