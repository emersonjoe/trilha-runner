package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-spec/agent"
	"github.com/emersonjoe/trilha-spec/spec"
)

func TestFromAgent(t *testing.T) {
	document, err := spec.Parse([]byte("---\nsandbox:\n  image: postgres:16-alpine\n  services: '[{\"name\":\"db\",\"image\":\"postgres:16-alpine\",\"env\":{\"POSTGRES_PASSWORD\":\"test\"},\"ready\":[\"pg_isready\",\"-U\",\"postgres\"]}]'\n---\n"))
	if err != nil {
		t.Fatal(err)
	}
	configured, ok, err := FromAgent(&agent.Agent{Fields: document.Fields})
	if err != nil || !ok {
		t.Fatalf("configured=%v ok=%v err=%v", configured, ok, err)
	}
	docker := configured.(Docker)
	if docker.Image != "postgres:16-alpine" || len(docker.Services) != 1 || docker.Services[0].Name != "db" {
		t.Fatalf("docker=%+v", docker)
	}
}

func TestDockerPostgresAndTeardown(t *testing.T) {
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("Docker is unavailable")
	}
	ctx := context.Background()
	docker := Docker{Image: "postgres:16-alpine", Services: []Service{{Name: "db", Image: "postgres:16-alpine", Env: map[string]string{"POSTGRES_PASSWORD": "test"}, Ready: []string{"pg_isready", "-U", "postgres"}}}}
	worktree := t.TempDir()
	environment, release, err := docker.Prepare(ctx, worktree, []string{"TRILHA_TASK=TASK-001"})
	if err != nil {
		t.Fatal(err)
	}
	if len(environment.Prefix) == 0 {
		t.Fatal("Docker sandbox returned no execution prefix")
	}
	inside := func(argv ...string) *exec.Cmd {
		return exec.Command(environment.Prefix[0], append(append([]string(nil), environment.Prefix[1:]...), argv...)...)
	}
	if output, err := inside("pg_isready", "-h", "db", "-U", "postgres").CombinedOutput(); err != nil {
		t.Fatalf("postgres check: %v: %s", err, output)
	}
	// The run's environment reached the container without any command
	// carrying it.
	if output, err := inside("sh", "-c", "echo $TRILHA_TASK:$TRILHA_WORKTREE").Output(); err != nil ||
		strings.TrimSpace(string(output)) != "TASK-001:/workspace" {
		t.Fatalf("environment inside = %q (%v)", output, err)
	}
	// The agent is the worktree's owner, not root, and the worktree is
	// writable because of it: with every capability dropped there is no
	// CAP_DAC_OVERRIDE to fall back on.
	if output, err := inside("id", "-u").Output(); err != nil || strings.TrimSpace(string(output)) != strconv.Itoa(os.Getuid()) {
		t.Fatalf("agent uid = %q, want %d (%v)", output, os.Getuid(), err)
	}
	if output, err := inside("touch", "/workspace/written").CombinedOutput(); err != nil {
		t.Fatalf("the worktree is not writable: %v: %s", err, output)
	}
	if _, err := os.Stat(filepath.Join(worktree, "written")); err != nil {
		t.Fatalf("the worktree is not the host's: %v", err)
	}
	if output, err := inside("touch", "/etc/escaped").CombinedOutput(); err == nil {
		t.Fatalf("the root filesystem is writable: %s", output)
	}
	container := environment.Prefix[len(environment.Prefix)-1]
	runID := strings.TrimSuffix(container, "-agent")
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if output, _ := exec.Command("docker", "ps", "-a", "--filter", "label=trilha.run="+runID, "--format", "{{.Names}}").CombinedOutput(); strings.TrimSpace(string(output)) != "" {
		t.Fatalf("containers remained after teardown: %s", output)
	}
	if output, _ := exec.Command("docker", "network", "ls", "--filter", "label=trilha.run="+runID, "--format", "{{.Name}}").CombinedOutput(); strings.TrimSpace(string(output)) != "" {
		t.Fatalf("network remained after teardown: %s", output)
	}
}
