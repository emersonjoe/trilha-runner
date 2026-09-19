package sandbox

import (
	"context"
	"os/exec"
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
	environment, release, err := docker.Prepare(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(environment.Prefix) == 0 {
		t.Fatal("Docker sandbox returned no execution prefix")
	}
	args := append(append([]string(nil), environment.Prefix[1:]...), "pg_isready", "-h", "db", "-U", "postgres")
	if output, err := exec.Command(environment.Prefix[0], args...).CombinedOutput(); err != nil {
		t.Fatalf("postgres check: %v: %s", err, output)
	}
	container := environment.Prefix[len(environment.Prefix)-2]
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
