package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/emersonjoe/trilha-spec/task"
)

func TestVerificationRecordsEvalAndFailsThreshold(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := repo(t)
	runner, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "eval", "golden", "manifest.json")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"cases":10}`)
	if err := os.WriteFile(manifest, content, 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "metric.sh")
	metric := `{"metric":"triage_top1","value":0.84,"threshold":0.85,"comparator":">=","dataset":{"id":"triage-v3","manifest":"eval/golden/manifest.json"}}`
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' '"+metric+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	item := &task.Task{ID: "TASK-001"}
	verification, err := runVerification(context.Background(), runner.Layout, item, dir, "test", []string{script}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if verification.Passed || len(verification.Evidence) != 2 || verification.Evidence[1].Kind != "eval" {
		t.Fatalf("verification = %+v", verification)
	}
	digest := sha256.Sum256(content)
	if got := verification.Evidence[1].Meta["dataset_sha256"]; got != hex.EncodeToString(digest[:]) {
		t.Fatalf("dataset hash = %q", got)
	}
	recorded, err := os.ReadFile(verification.Paths[1])
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(recorded, &object); err != nil {
		t.Fatal(err)
	}
	if object["metric"] != "triage_top1" || object["dataset"].(map[string]any)["sha256"] == "" {
		t.Fatalf("eval json = %s", recorded)
	}
}
