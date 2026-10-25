package runner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-runner/sandbox"
	"github.com/emersonjoe/trilha-spec/task"
)

// A tool the checks need is not installed on the worker. The run must say so
// before the agent is asked anything: issues #13 and #14 — acervo run-000007
// spent three attempts of a model because `python` was missing.
func TestMissingCheckToolFailsBeforeTheDriverRuns(t *testing.T) {
	r, agent, tk, _ := retrying(t, nil)
	tk.Checks = []string{"trilha-no-such-tool-0a1b2c --version", "test -f done.txt"}
	if err := r.Store.Save(tk); err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), tk.ID)
	if err == nil || !strings.Contains(err.Error(), "trilha-no-such-tool-0a1b2c") {
		t.Fatalf("err = %v", err)
	}
	if res.Status != task.Failed || res.Attempt != 1 || res.FailureClass != "environment" || res.ErrorCode != "environment_missing_tool" {
		t.Fatalf("res = %+v", res)
	}
	if len(agent.jobs) != 0 {
		t.Fatalf("the driver was called %d times for a check that could never run", len(agent.jobs))
	}
	if len(res.Attempts) != 1 || res.Attempts[0].Status != "failed" || res.Attempts[0].FailureClass != "environment" || res.Attempts[0].ErrorCode != "environment_missing_tool" {
		t.Fatalf("attempts = %+v", res.Attempts)
	}
	ev, _ := task.ListEvidence(r.Layout, tk.ID)
	if len(ev) != 1 || ev[0].Kind != "run" || ev[0].Meta["failure_class"] != "environment" || ev[0].Meta["error_code"] != "environment_missing_tool" || ev[0].Meta["tool"] != "trilha-no-such-tool-0a1b2c" || !strings.Contains(ev[0].Note, "trilha-no-such-tool-0a1b2c") {
		t.Fatalf("evidence = %+v", ev)
	}
	if got, _ := r.Store.Get(tk.ID); got.Status != task.Failed {
		t.Fatalf("status = %s", got.Status)
	}
}

// prefixSandbox stands in for Docker: every command enters through a fake
// `docker exec` that logs its argv, so the test can see how the tool was
// looked up inside the sandbox and that nothing else ran there.
type prefixSandbox struct{ binary string }

func (prefixSandbox) Name() string { return "fake" }

func (s prefixSandbox) Prepare(ctx context.Context, dir string, env []string) (sandbox.Env, func() error, error) {
	return sandbox.Env{Dir: dir, Env: env, Prefix: []string{s.binary, "exec", "-i", "--workdir", "/workspace", "trilha-fake-agent"}}, func() error { return nil }, nil
}

// Inside a sandbox the tool is looked up where the check will run — through
// the sandbox prefix with `command -v` — never on the host.
func TestMissingCheckToolInsideTheSandboxFailsBeforeTheDriverRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := t.TempDir()
	binary, logPath := filepath.Join(dir, "docker"), filepath.Join(dir, "calls.log")
	// `command -v` of anything answers "not found" (127), like a container
	// image without the tool; every other command would succeed.
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\ncase \"$*\" in *'command -v'*) exit 127 ;; esac\nexit 0\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	r, agent, tk, _ := retrying(t, nil)
	r.Sandbox = prefixSandbox{binary: binary}
	tk.Checks = []string{"python -c 'import app'"}
	if err := r.Store.Save(tk); err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), tk.ID)
	if err == nil || res.Status != task.Failed || res.FailureClass != "environment" || res.ErrorCode != "environment_missing_tool" || res.Attempt != 1 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if len(agent.jobs) != 0 {
		t.Fatalf("the driver was called %d times", len(agent.jobs))
	}
	calls, _ := os.ReadFile(logPath)
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "exec -i --workdir /workspace trilha-fake-agent sh -c ") || !strings.Contains(lines[0], "command -v") || !strings.HasSuffix(lines[0], " python") {
		t.Fatalf("sandbox calls = %q", lines)
	}
	// The host was not consulted: the check names a tool the host may well
	// have, and the sandbox said it does not.
	ev, _ := task.ListEvidence(r.Layout, tk.ID)
	if len(ev) != 1 || ev[0].Meta["tool"] != "python" || ev[0].Meta["error_code"] != "environment_missing_tool" {
		t.Fatalf("evidence = %+v", ev)
	}
}

// A check that exits 127 at run time — a script calling a tool preflight
// could not see — is the same environment failure, and it does not earn a
// corrective attempt: asking the agent again does not install anything.
func TestCheckExitingWith127IsAnEnvironmentFailureWithoutRetry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	r, agent, tk, _ := retrying(t, nil, nil, nil)
	tk.Checks = []string{"sh -c trilha-no-such-tool-0a1b2c"}
	if err := r.Store.Save(tk); err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), tk.ID)
	if err == nil || res.Status != task.Failed || res.FailureClass != "environment" || res.ErrorCode != "environment_missing_tool" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if len(agent.jobs) != 1 || res.Attempt != 1 {
		t.Fatalf("driver calls = %d, attempt = %d: an environment failure must not be retried", len(agent.jobs), res.Attempt)
	}
	if len(res.Attempts) != 1 || res.Attempts[0].FailureClass != "environment" || res.Attempts[0].ErrorCode != "environment_missing_tool" {
		t.Fatalf("attempts = %+v", res.Attempts)
	}
}
