package deployer

import (
	"context"
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

func TestExecuteRejectsUnknownProfile(t *testing.T) {
	result := (Config{Profiles: map[string]Profile{}}).Execute(context.Background(), queue.DeploymentWork{Deployment: queue.Deployment{Profile: "missing"}})
	if result.Passed || result.Error == "" {
		t.Fatalf("result = %#v", result)
	}
}
