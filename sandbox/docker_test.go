package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersonjoe/trilha-spec/agent"
	"github.com/emersonjoe/trilha-spec/spec"
)

// fakeDocker writes a script that stands in for the client: it appends every
// invocation to a log and exits 0, unless the command matches failOn. An
// `inspect` answers that the container is running, which is what the real
// client says for a container that stayed up.
func fakeDocker(t *testing.T, failOn string) (binary, logPath string) {
	t.Helper()
	return fakeDockerRunning(t, failOn, "true")
}

// fakeDockerRunning is fakeDocker with what `inspect` should answer, so a
// container that did not stay up can be exercised too.
func fakeDockerRunning(t *testing.T, failOn, running string) (binary, logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := t.TempDir()
	binary = filepath.Join(dir, "docker")
	logPath = filepath.Join(dir, "calls.log")
	body := "#!/bin/sh\necho \"$@\" >> " + logPath + "\n"
	if failOn != "" {
		body += "case \"$*\" in\n  " + failOn + ") exit 1 ;;\nesac\n"
	}
	body += "case \"$1\" in\n" +
		"  inspect) echo " + running + "; exit 0 ;;\n" +
		"  logs) echo 'sleep: invalid number'; exit 0 ;;\n" +
		"esac\necho fake-id\n"
	if err := os.WriteFile(binary, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return binary, logPath
}

func calls(t *testing.T, logPath string) []string {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func called(t *testing.T, logPath, want string) bool {
	t.Helper()
	for _, line := range calls(t, logPath) {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}

func demoSpec() *Spec {
	return &Spec{
		Image: "golang:1.22",
		Env:   map[string]string{"CGO_ENABLED": "0"},
		Services: []Service{{
			Name:  "postgres",
			Image: "pgvector/pgvector:pg16",
			Env:   map[string]string{"POSTGRES_PASSWORD": "trilha", "POSTGRES_DB": "acervo"},
			Ready: []string{"pg_isready", "-U", "postgres"},
		}},
	}
}

func TestDockerPreparesServicesThenTheAgentContainer(t *testing.T) {
	binary, logPath := fakeDocker(t, "")
	worktree := t.TempDir()
	d := Docker{Binary: binary}
	env, release, err := d.Prepare(context.Background(), Request{
		Task: "TASK-001", Dir: worktree, Env: []string{"ANTHROPIC_API_KEY=sk-secret-0123456789"}, Spec: demoSpec(),
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(calls(t, logPath), "\n"))
	}
	defer release()

	lines := calls(t, logPath)
	if len(lines) < 4 {
		t.Fatalf("calls:\n%s", strings.Join(lines, "\n"))
	}
	if lines[0] != "network create trilha-task-001" {
		t.Fatalf("first call = %q", lines[0])
	}
	// The service comes up on the private network, reachable by its name.
	service := lines[1]
	for _, want := range []string{"--name trilha-task-001-postgres", "--network trilha-task-001",
		"--network-alias postgres", "--env POSTGRES_DB=acervo", "--env POSTGRES_PASSWORD=trilha",
		"pgvector/pgvector:pg16"} {
		if !strings.Contains(service, want) {
			t.Errorf("service call misses %q:\n%s", want, service)
		}
	}
	// It is asked whether it is ready before the agent starts.
	readyAt, agentAt := -1, -1
	for i, line := range lines {
		if line == "exec trilha-task-001-postgres pg_isready -U postgres" && readyAt < 0 {
			readyAt = i
		}
		if strings.HasPrefix(line, "run --detach --name trilha-task-001 ") {
			agentAt = i
		}
	}
	if readyAt < 0 || agentAt < 0 || readyAt > agentAt {
		t.Fatalf("the agent must start after the service is ready:\n%s", strings.Join(lines, "\n"))
	}
	agentCall := lines[agentAt]
	for _, want := range []string{
		"--name trilha-task-001", "--network trilha-task-001",
		"--volume " + worktree + ":/workspace", "--workdir /workspace",
		"--read-only", "--tmpfs /tmp:rw,size=256m",
		"--security-opt no-new-privileges", "--cap-drop ALL",
		"--pids-limit 512", "--memory 4g", "--cpus 2",
		"--entrypoint sleep golang:1.22 2147483647",
	} {
		if !strings.Contains(agentCall, want) {
			t.Errorf("agent call misses %q:\n%s", want, agentCall)
		}
	}
	// No socket reaches the sandbox, and no credential reaches a command line.
	for _, line := range lines {
		if strings.Contains(line, "docker.sock") {
			t.Fatalf("the Docker socket was mounted: %s", line)
		}
		if strings.Contains(line, "sk-secret-0123456789") {
			t.Fatalf("a credential is on a command line: %s", line)
		}
	}

	// The environment travels in a file only this user can read.
	var envFile string
	for _, field := range strings.Fields(agentCall) {
		if strings.HasSuffix(field, ".env") {
			envFile = field
		}
	}
	if envFile == "" {
		t.Fatalf("no --env-file:\n%s", agentCall)
	}
	info, err := os.Stat(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("env file mode = %v", info.Mode().Perm())
	}
	body, _ := os.ReadFile(envFile)
	for _, want := range []string{"TRILHA_TASK=TASK-001", "TRILHA_WORKTREE=/workspace",
		"ANTHROPIC_API_KEY=sk-secret-0123456789", "CGO_ENABLED=0"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("env file misses %q:\n%s", want, body)
		}
	}

	// Commands run inside, and a check reads as exactly what ran.
	if !env.Inside() || env.WorkDir != "/workspace" {
		t.Fatalf("env = %+v", env)
	}
	wrapped := env.Wrap(`sh -c "go test ./..."`)
	if wrapped != binary+` exec --workdir /workspace trilha-task-001 sh -c "go test ./..."` {
		t.Fatalf("wrapped = %q", wrapped)
	}

	// Teardown removes the containers and the network, and is idempotent.
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if !called(t, logPath, "rm --force --volumes trilha-task-001") ||
		!called(t, logPath, "rm --force --volumes trilha-task-001-postgres") ||
		!called(t, logPath, "network rm trilha-task-001") {
		t.Fatalf("teardown incomplete:\n%s", strings.Join(calls(t, logPath), "\n"))
	}
	if _, err := os.Stat(envFile); !os.IsNotExist(err) {
		t.Fatal("the env file outlived the sandbox")
	}
	before := len(calls(t, logPath))
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if len(calls(t, logPath)) != before {
		t.Fatal("a second release tore down again")
	}
}

// A service that never becomes ready fails the run, and nothing is left over.
func TestDockerTearsDownWhenAServiceNeverBecomesReady(t *testing.T) {
	readyInterval = 10 * time.Millisecond
	defer func() { readyInterval = time.Second }()
	binary, logPath := fakeDocker(t, "exec*pg_isready*")
	spec := demoSpec()
	spec.Services[0].ReadyTimeoutSeconds = 1
	_, release, err := Docker{Binary: binary}.Prepare(context.Background(), Request{
		Task: "TASK-002", Dir: t.TempDir(), Spec: spec,
	})
	if err == nil || !strings.Contains(err.Error(), "was not ready") {
		t.Fatalf("err = %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	// The service that was started is removed; the agent container never was.
	if !called(t, logPath, "rm --force --volumes trilha-task-002-postgres") ||
		!called(t, logPath, "network rm trilha-task-002") {
		t.Fatalf("teardown incomplete:\n%s", strings.Join(calls(t, logPath), "\n"))
	}
	for _, line := range calls(t, logPath) {
		if strings.HasPrefix(line, "run --detach --name trilha-task-002 ") {
			t.Fatal("the agent started although a service was not ready")
		}
	}
}

// A failure creating the network leaves nothing behind either.
func TestDockerReleasesAfterAnEarlyFailure(t *testing.T) {
	binary, logPath := fakeDocker(t, "network create*")
	_, release, err := Docker{Binary: binary}.Prepare(context.Background(), Request{
		Task: "TASK-003", Dir: t.TempDir(), Spec: demoSpec(),
	})
	if err == nil {
		t.Fatal("the failure was not reported")
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if called(t, logPath, "network rm") {
		t.Fatal("removed a network that was never created")
	}
}

func TestDockerRefusesWhatItCannotRun(t *testing.T) {
	binary, _ := fakeDocker(t, "")
	d := Docker{Binary: binary}
	if _, _, err := d.Prepare(context.Background(), Request{Task: "TASK-001", Dir: t.TempDir()}); !errors.Is(err, ErrNoSpec) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := d.Prepare(context.Background(), Request{Task: "nope", Dir: t.TempDir(), Spec: demoSpec()}); err == nil {
		t.Fatal("a bad task id was accepted")
	}
	if _, _, err := d.Prepare(context.Background(), Request{Task: "TASK-001", Dir: t.TempDir(), Spec: &Spec{}}); err == nil {
		t.Fatal("a spec with no image was accepted")
	}
	// An environment value with a newline would smuggle a second variable.
	_, release, err := d.Prepare(context.Background(), Request{Task: "TASK-001", Dir: t.TempDir(),
		Env: []string{"A=one\nB=two"}, Spec: demoSpec()})
	if err == nil || !strings.Contains(err.Error(), "newline") {
		t.Fatalf("err = %v", err)
	}
	release()
}

// None is unchanged: the worktree is the sandbox and nothing wraps.
func TestNoneRunsInPlace(t *testing.T) {
	dir := t.TempDir()
	env, release, err := None{}.Prepare(context.Background(), Request{Task: "TASK-001", Dir: dir, Env: []string{"A=1"}})
	if err != nil || env.Dir != dir || env.WorkDir != dir || env.Inside() {
		t.Fatalf("%+v %v", env, err)
	}
	if env.Wrap("go test ./...") != "go test ./..." {
		t.Fatalf("wrapped = %q", env.Wrap("go test ./..."))
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestFromAgent(t *testing.T) {
	root := t.TempDir()
	layout, _, err := spec.Init(root, spec.InitOptions{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	_ = layout

	// Inline JSON in the manifest.
	man, err := agent.Parse([]byte("---\nname: coder\nrole: Implements\ndriver: exec\n" +
		`sandbox: {"image":"golang:1.22","services":[{"name":"postgres","image":"pg:16","ready":["pg_isready"]}]}` + "\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := FromAgent(man, root)
	if err != nil || got.Image != "golang:1.22" || len(got.Services) != 1 || got.Services[0].Name != "postgres" {
		t.Fatalf("%+v %v", got, err)
	}

	// A file the manifest names, relative to the repository root.
	os.MkdirAll(filepath.Join(root, ".trilha", "sandboxes"), 0o755)
	os.WriteFile(filepath.Join(root, ".trilha", "sandboxes", "api.json"),
		[]byte(`{"image":"python:3.12","services":[{"name":"postgres","image":"pg:16"}]}`), 0o644)
	man.Fields.Set(SpecField, ".trilha/sandboxes/api.json")
	got, err = FromAgent(man, root)
	if err != nil || got.Image != "python:3.12" {
		t.Fatalf("%+v %v", got, err)
	}

	// A manifest that declares nothing, and one that declares nonsense.
	bare, _ := agent.Parse([]byte("---\nname: coder\nrole: Implements\n---\n"))
	if _, err := FromAgent(bare, root); !errors.Is(err, ErrNoSpec) {
		t.Fatalf("err = %v", err)
	}
	if _, err := FromAgent(nil, root); !errors.Is(err, ErrNoSpec) {
		t.Fatalf("err = %v", err)
	}
	for _, bad := range []string{
		`{"image":"x","services":[{"name":"Postgres","image":"pg"}]}`,
		`{"image":"x","services":[{"name":"a","image":"pg"},{"name":"a","image":"pg"}]}`,
		`{"image":"x","services":[{"name":"a"}]}`,
		`{"image":"x","privileged":true}`,
		`{"services":[]}`,
	} {
		man.Fields.Set(SpecField, bad)
		if _, err := FromAgent(man, root); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

// The limits are the runner's: a manifest cannot raise its own ceiling.
func TestLimitsAreTheRunners(t *testing.T) {
	binary, logPath := fakeDocker(t, "")
	d := Docker{Binary: binary, Limits: Limits{CPUs: "1", Memory: "512m", PIDs: 64}}
	_, release, err := d.Prepare(context.Background(), Request{Task: "TASK-001", Dir: t.TempDir(), Spec: demoSpec()})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if !called(t, logPath, "--cpus 1") || !called(t, logPath, "--memory 512m") || !called(t, logPath, "--pids-limit 64") {
		t.Fatalf("calls:\n%s", strings.Join(calls(t, logPath), "\n"))
	}
}

// The keep-alive must be a plain number of seconds. `sleep infinity` is a GNU
// coreutils spelling and busybox — Alpine, and most small base images —
// refuses it, so the container would exit before the first command.
func TestKeepAliveIsPortable(t *testing.T) {
	if len(KeepAlive) != 2 || KeepAlive[0] != "sleep" {
		t.Fatalf("KeepAlive = %v", KeepAlive)
	}
	if _, err := strconv.Atoi(KeepAlive[1]); err != nil {
		t.Fatalf("KeepAlive argument %q is not a number of seconds: busybox sleep needs one", KeepAlive[1])
	}
}

// A container that did not stay up is reported at once, with its own output,
// rather than through a later `docker exec` that says nothing useful.
func TestDockerReportsAContainerThatDidNotStayUp(t *testing.T) {
	binary, logPath := fakeDockerRunning(t, "", "false")
	_, release, err := Docker{Binary: binary}.Prepare(context.Background(), Request{
		Task: "TASK-004", Dir: t.TempDir(), Spec: &Spec{Image: "alpine:3"},
	})
	if err == nil {
		t.Fatal("a container that exited was accepted")
	}
	if !strings.Contains(err.Error(), "exited instead of staying up") {
		t.Fatalf("err = %v", err)
	}
	// The container's own output is what says why.
	if !strings.Contains(err.Error(), "sleep: invalid number") {
		t.Fatalf("err does not carry the container's output: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if !called(t, logPath, "rm --force --volumes trilha-task-004") {
		t.Fatalf("teardown incomplete:\n%s", strings.Join(calls(t, logPath), "\n"))
	}
}

// A service that exits fails immediately instead of after the readiness
// timeout: waiting a minute for a container that is already gone tells the
// operator nothing.
func TestDockerFailsFastWhenAServiceExits(t *testing.T) {
	readyInterval = time.Hour // any poll-then-retry would hang the test
	defer func() { readyInterval = time.Second }()
	binary, _ := fakeDockerRunning(t, "exec*pg_isready*", "false")
	spec := demoSpec()
	spec.Services[0].ReadyTimeoutSeconds = 3600
	start := time.Now()
	_, release, err := Docker{Binary: binary}.Prepare(context.Background(), Request{
		Task: "TASK-005", Dir: t.TempDir(), Spec: spec,
	})
	if err == nil || !strings.Contains(err.Error(), "exited instead of staying up") {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("took %s: it waited for a container that was already gone", elapsed)
	}
	release()
}
