package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const probeSecret = "sk-ant-oat-SECRET-0123456789"

// fakeDocker stands in for the client so the argv can be inspected without a
// daemon: it logs every invocation, and answers `inspect` with running.
func fakeDocker(t *testing.T, running string) (binary, logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := t.TempDir()
	binary = filepath.Join(dir, "docker")
	logPath = filepath.Join(dir, "calls.log")
	body := "#!/bin/sh\necho \"$@\" >> " + logPath + "\n" +
		"case \"$1\" in\n" +
		"  inspect) [ \"$2\" = \"--format\" ] && { echo " + running + "; exit 0; }; exit 0 ;;\n" +
		"  logs) echo 'exec: \"tail\": executable file not found'; exit 0 ;;\n" +
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

// agentRun answers the `docker run` line that started the agent container.
func agentRun(t *testing.T, logPath string) string {
	t.Helper()
	for _, line := range calls(t, logPath) {
		if strings.HasPrefix(line, "run -d ") && strings.Contains(line, "-agent ") {
			return line
		}
	}
	t.Fatalf("no agent container was started:\n%s", strings.Join(calls(t, logPath), "\n"))
	return ""
}

// No command may carry a secret: an argument of `docker exec` is an argument
// of a host process, and `ps` shows it to every user on the machine.
func TestSecretNeverReachesACommandLine(t *testing.T) {
	binary, logPath := fakeDocker(t, "true")
	docker := Docker{Image: "alpine:3", Binary: binary,
		Services: []Service{{Name: "db", Image: "alpine:3"}}}
	environment, release, err := docker.Prepare(context.Background(), t.TempDir(),
		[]string{"TRILHA_TASK=TASK-001", "CLAUDE_CODE_OAUTH_TOKEN=" + probeSecret})
	if err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(calls(t, logPath), "\n"))
	}

	for _, line := range calls(t, logPath) {
		if strings.Contains(line, probeSecret) {
			t.Fatalf("the credential is on a command line: %s", line)
		}
	}
	// The prefix a driver will use carries nothing either — not the secret,
	// and no `env KEY=VALUE` that a later command would inherit on its argv.
	joined := strings.Join(environment.Prefix, " ")
	if strings.Contains(joined, probeSecret) || strings.Contains(joined, "=") {
		t.Fatalf("prefix carries environment: %q", joined)
	}

	// It travels in a file only this user can read.
	var envFile string
	fields := strings.Fields(agentRun(t, logPath))
	for i, f := range fields {
		if f == "--env-file" && i+1 < len(fields) {
			envFile = fields[i+1]
		}
	}
	if envFile == "" {
		t.Fatalf("no --env-file:\n%s", agentRun(t, logPath))
	}
	info, err := os.Stat(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("env file mode = %v, want 0600", info.Mode().Perm())
	}
	body, _ := os.ReadFile(envFile)
	for _, want := range []string{"CLAUDE_CODE_OAUTH_TOKEN=" + probeSecret, "TRILHA_TASK=TASK-001",
		"TRILHA_WORKTREE=/workspace", "HOME=/tmp"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("env file misses %q:\n%s", want, body)
		}
	}

	// And it does not outlive the sandbox.
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(envFile); !os.IsNotExist(err) {
		t.Fatal("the env file holding the credential outlived the sandbox")
	}
}

// The agent drops every capability and runs as the worktree's owner. The two
// belong together: dropping capabilities alone removes CAP_DAC_OVERRIDE, and a
// root agent then cannot write the worktree at all.
func TestAgentDropsCapabilitiesAndRunsAsTheWorktreeOwner(t *testing.T) {
	binary, logPath := fakeDocker(t, "true")
	_, release, err := Docker{Image: "alpine:3", Binary: binary}.Prepare(
		context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	line := agentRun(t, logPath)
	if !strings.Contains(line, "--cap-drop ALL") {
		t.Errorf("agent keeps its capabilities:\n%s", line)
	}
	want := "--user " + strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	if !strings.Contains(line, want) {
		t.Errorf("agent does not run as the worktree's owner (%s):\n%s", want, line)
	}
	// The limits main already fixed are still fixed.
	for _, flag := range []string{"--read-only", "--security-opt no-new-privileges",
		"--pids-limit 256", "--memory 1g", "--cpus 1"} {
		if !strings.Contains(line, flag) {
			t.Errorf("agent line misses %q:\n%s", flag, line)
		}
	}
	// And no socket ever reaches it.
	for _, call := range calls(t, logPath) {
		if strings.Contains(call, "docker.sock") {
			t.Fatalf("the Docker socket was mounted: %s", call)
		}
	}
}

// A container that did not stay up says so, with its own output, instead of
// surfacing later as an unexplained `docker exec` failure.
func TestContainerThatDidNotStayUpIsReported(t *testing.T) {
	binary, logPath := fakeDocker(t, "false")
	_, release, err := Docker{Image: "alpine:3", Binary: binary}.Prepare(
		context.Background(), t.TempDir(), nil)
	if err == nil {
		t.Fatal("a container that exited was accepted")
	}
	if !strings.Contains(err.Error(), "exited instead of staying up") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "executable file not found") {
		t.Fatalf("err does not carry the container's own output: %v", err)
	}
	// Prepare cleans up on its own way out, and the release it answers is a
	// no-op rather than nil, so deferring it is safe.
	if release == nil {
		t.Fatal("Prepare answered a nil release on failure")
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	var removed bool
	for _, call := range calls(t, logPath) {
		if strings.HasPrefix(call, "rm -f ") {
			removed = true
		}
	}
	if !removed {
		t.Fatalf("teardown did not remove the container:\n%s", strings.Join(calls(t, logPath), "\n"))
	}
}

// A service that exits is caught before its readiness command is waited on.
func TestServiceThatExitsIsCaughtBeforeReadiness(t *testing.T) {
	binary, logPath := fakeDocker(t, "false")
	_, release, err := Docker{Image: "alpine:3", Binary: binary,
		Services: []Service{{Name: "db", Image: "alpine:3", Ready: []string{"pg_isready"}}}}.Prepare(
		context.Background(), t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "service db exited") {
		t.Fatalf("err = %v", err)
	}
	release()
	for _, call := range calls(t, logPath) {
		if strings.Contains(call, "pg_isready") {
			t.Fatalf("readiness was waited on a container that was already gone: %s", call)
		}
	}
}

// An environment value with a newline would smuggle a second variable into the
// env file.
func TestEnvironmentValueWithANewlineIsRefused(t *testing.T) {
	binary, _ := fakeDocker(t, "true")
	_, release, err := Docker{Image: "alpine:3", Binary: binary}.Prepare(
		context.Background(), t.TempDir(), []string{"A=one\nB=two"})
	if err == nil || !strings.Contains(err.Error(), "newline") {
		t.Fatalf("err = %v", err)
	}
	release()
}

// None is unchanged: it runs in place and hands the environment straight back.
func TestNoneRunsInPlace(t *testing.T) {
	dir := t.TempDir()
	environment, release, err := None{}.Prepare(context.Background(), dir, []string{"A=1"})
	if err != nil || environment.Dir != dir || len(environment.Prefix) != 0 {
		t.Fatalf("%+v %v", environment, err)
	}
	if len(environment.Env) != 1 || environment.Env[0] != "A=1" {
		t.Fatalf("env = %v", environment.Env)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}
