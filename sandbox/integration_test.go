package sandbox_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-runner/runner"
	"github.com/emersonjoe/trilha-runner/sandbox"
	"github.com/emersonjoe/trilha-spec/spec"
	"github.com/emersonjoe/trilha-spec/task"
)

// requireDocker skips unless this machine can actually run the sandbox. The
// eoslab runner is deliberately without Docker, so these tests must not fail
// there; on a host labelled `docker` they are the ones that matter.
func requireDocker(t *testing.T) sandbox.Docker {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	if os.Getenv("TRILHA_SKIP_DOCKER_TESTS") != "" {
		t.Skip("TRILHA_SKIP_DOCKER_TESTS is set")
	}
	box := sandbox.Docker{}
	if err := box.Available(context.Background()); err != nil {
		t.Skipf("no Docker on this machine: %v", err)
	}
	return box
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	if _, _, err := spec.Init(dir, spec.InitOptions{Name: "demo"}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("demo\n"), 0o644)
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	return dir
}

// A check that needs Postgres passes inside the sandbox and fails with None:
// that difference is the whole point of the sandbox existing.
func TestCheckNeedingPostgresPassesInsideAndFailsWithNone(t *testing.T) {
	box := requireDocker(t)
	// The check reaches the service by name on the sandbox's own network.
	const check = `sh -c "PGPASSWORD=trilha psql -h postgres -U postgres -d acervo -c \"select 1\""`
	sb := &sandbox.Spec{
		Image: "postgres:16-alpine", // carries psql, so the check has a client
		Services: []sandbox.Service{{
			Name:                "postgres",
			Image:               "postgres:16-alpine",
			Env:                 map[string]string{"POSTGRES_PASSWORD": "trilha", "POSTGRES_DB": "acervo"},
			Ready:               []string{"pg_isready", "-U", "postgres"},
			ReadyTimeoutSeconds: 120,
		}},
	}

	inside := gitRepo(t)
	r, err := runner.New(inside)
	if err != nil {
		t.Fatal(err)
	}
	r.Driver, r.Sandbox, r.SandboxSpec = driver.Echo{}, box, sb
	r.Log = func(s string) { t.Log(s) }
	tk, _ := r.Store.Create("Needs Postgres", func(x *task.Task) {
		x.Status = task.Ready
		x.Acceptance = []string{"the database answers"}
		x.Checks = []string{check}
	})
	res, err := r.Run(context.Background(), tk.ID)
	if err != nil || !res.Passed || res.Status != task.Review {
		t.Fatalf("inside the sandbox: %+v %v", res, err)
	}
	// The evidence records what actually ran, sandbox and all.
	var ran string
	for _, e := range res.Evidence {
		if e.Kind == "check" {
			ran = e.Command
		}
	}
	if !strings.Contains(ran, "docker exec --workdir /workspace") {
		t.Fatalf("the evidence does not say where it ran: %q", ran)
	}

	// The same check, the same task, without the sandbox.
	outside := gitRepo(t)
	r2, err := runner.New(outside)
	if err != nil {
		t.Fatal(err)
	}
	r2.Driver = driver.Echo{}
	tk2, _ := r2.Store.Create("Needs Postgres", func(x *task.Task) {
		x.Status = task.Ready
		x.Acceptance = []string{"the database answers"}
		x.Checks = []string{check}
	})
	if res, err := r2.Run(context.Background(), tk2.ID); err == nil || res.Passed {
		t.Fatalf("with None the check should fail: %+v %v", res, err)
	}
}

// Teardown leaves no container and no network behind, whatever the run did.
func TestTeardownLeavesNothingBehind(t *testing.T) {
	box := requireDocker(t)
	// --all, so a container that was merely stopped rather than removed is
	// caught as a leftover too.
	names := func(kind string, extra ...string) string {
		args := append(append([]string{kind, "ls"}, extra...), "--format", "{{.Name}}")
		out, err := exec.Command("docker", args...).Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	// A service has to be an image that stays up on its own; a base image with
	// no long-running entrypoint exits at once, which the sandbox now reports
	// rather than waiting out.
	sb := &sandbox.Spec{
		Image: "postgres:16-alpine",
		Services: []sandbox.Service{{
			Name:                "postgres",
			Image:               "postgres:16-alpine",
			Env:                 map[string]string{"POSTGRES_PASSWORD": "trilha"},
			Ready:               []string{"pg_isready", "-U", "postgres"},
			ReadyTimeoutSeconds: 120,
		}},
	}
	env, release, err := box.Prepare(context.Background(), sandbox.Request{
		Task: "TASK-009", Dir: t.TempDir(), Spec: sb,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(names("container"), "trilha-task-009") {
		t.Fatal("the sandbox did not start")
	}
	if !env.Inside() {
		t.Fatalf("env = %+v", env)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(names("container", "--all"), "trilha-task-009") {
		t.Fatal("a container outlived the run")
	}
	if strings.Contains(names("network"), "trilha-task-009") {
		t.Fatal("a network outlived the run")
	}
}

// The worktree is the only writable path that survives the run.
//
// The image is deliberately busybox-based: it is the case `sleep infinity`
// broke, so this is the regression guard for KeepAlive as much as for the
// mount.
func TestOnlyTheWorktreeIsWritable(t *testing.T) {
	box := requireDocker(t)
	dir := t.TempDir()
	env, release, err := box.Prepare(context.Background(), sandbox.Request{
		Task: "TASK-010", Dir: dir, Spec: &sandbox.Spec{Image: "alpine:3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	inside := func(command string) *exec.Cmd {
		argv := append(append([]string(nil), env.Prefix...), "sh", "-c", command)
		return exec.Command(argv[0], argv[1:]...)
	}
	run := func(command string) error { return inside(command).Run() }
	if err := run("touch /workspace/written"); err != nil {
		t.Fatalf("the worktree is not writable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "written")); err != nil {
		t.Fatalf("the worktree is not the host's: %v", err)
	}
	if err := run("touch /usr/local/escaped"); err == nil {
		t.Fatal("the root filesystem is writable")
	}
	// The environment reached the container without ever being on a command line.
	out, err := inside("echo $TRILHA_WORKTREE").Output()
	if err != nil || strings.TrimSpace(string(out)) != "/workspace" {
		t.Fatalf("TRILHA_WORKTREE = %q (%v)", out, err)
	}
}
