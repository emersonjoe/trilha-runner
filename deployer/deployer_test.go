package deployer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-runner/queue"
)

func TestExecuteUsesAllowListAndRedactsSecrets(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "deploy")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s %s' \"$TRILHA_REVISION\" \"$DATABASE_URL\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := Config{Profiles: map[string]Profile{"production": {Deploy: []string{script}}}}
	work := queue.DeploymentWork{Deployment: queue.Deployment{ID: "deploy-1", Project: "app", Environment: "production", Profile: "production", Action: "deploy", Revision: "abc123"}, Secrets: map[string]string{"DATABASE_URL": "private-value"}}
	result := config.Execute(context.Background(), work)
	if !result.Passed {
		t.Fatalf("result = %#v", result)
	}
	if strings.Contains(result.Log, "private-value") || !strings.Contains(result.Log, "[REDACTED]") {
		t.Fatalf("secret leaked: %q", result.Log)
	}
}

func TestMigrationFailureAbortsBeforeSwitch(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "order")
	script := func(name, body string) []string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		return []string{path}
	}
	config := Config{Profiles: map[string]Profile{"compose": {
		Steps:   []Step{{Name: "pull", Argv: script("pull", "echo pull >> "+log)}, {Name: "switch", Argv: script("switch", "echo switch >> "+log)}},
		Migrate: &Migration{Argv: script("migrate", "echo migrate >> "+log+"; exit 7")},
	}}}
	result := config.Execute(context.Background(), queue.DeploymentWork{Deployment: queue.Deployment{Profile: "compose", Action: "deploy"}})
	order, _ := os.ReadFile(log)
	if result.Passed || string(order) != "pull\nmigrate\n" || !strings.Contains(result.Error, "switch aborted") {
		t.Fatalf("result=%+v order=%q", result, order)
	}
}

func TestHealthFailureRollsBackImageOnly(t *testing.T) {
	root := t.TempDir()
	order := filepath.Join(root, "order")
	write := func(name, line string) []string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\necho "+line+" >> "+order+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		return []string{path}
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Error(response, "not ready", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	config := Config{Profiles: map[string]Profile{"compose": {
		Steps:    []Step{{Name: "pull", Argv: write("pull", "pull")}, {Name: "switch", Argv: write("switch", "switch")}},
		Migrate:  &Migration{Argv: write("migrate", "migrate"), Downgrade: write("down", "down"), Reversible: false},
		Rollback: write("rollback", "rollback"),
		Health:   []HealthCheck{{Name: "api", URL: server.URL}},
	}}}
	result := config.Execute(context.Background(), queue.DeploymentWork{Deployment: queue.Deployment{Profile: "compose", Action: "deploy"}})
	content, _ := os.ReadFile(order)
	if result.Passed || string(content) != "pull\nmigrate\nswitch\nrollback\n" {
		t.Fatalf("result=%+v order=%q", result, content)
	}
	if !strings.Contains(result.Log, "image-only rollback") || strings.Contains(string(content), "down") {
		t.Fatalf("rollback semantics missing: log=%q order=%q", result.Log, content)
	}
}

func TestExecuteRejectsUnknownProfile(t *testing.T) {
	result := (Config{Profiles: map[string]Profile{}}).Execute(context.Background(), queue.DeploymentWork{Deployment: queue.Deployment{Profile: "missing"}})
	if result.Passed || result.Error == "" {
		t.Fatalf("result = %#v", result)
	}
}
