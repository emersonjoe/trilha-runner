package syncer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-runner/queue"
)

func TestEnsureRepositoryAndMaterializeBundle(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	runGit(t, root, "init", "--bare", origin)
	if err := os.Mkdir(seed, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "README.md")
	runGit(t, seed, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	runGit(t, seed, "remote", "add", "origin", origin)
	runGit(t, seed, "push", "origin", "main")
	runGit(t, origin, "symbolic-ref", "HEAD", "refs/heads/main")

	repository, err := EnsureRepository(ctx, filepath.Join(root, "workspaces"), "sample-app", origin, "main")
	if err != nil {
		t.Fatal(err)
	}
	bundle := queue.Bundle{
		Version:       1,
		RunID:         "run-1",
		Project:       "sample-app",
		Repository:    origin,
		DefaultBranch: "main",
		Specification: queue.Specification{ID: "spec-1", ProtocolID: "001-user-access", Title: "User access", Description: "Login flow", Version: 1},
		Round: queue.Round{
			ID: "round-001", Number: 1, Goal: "Ship authentication",
			Stages: []queue.Stage{{
				ID: "stage-1", Title: "Build", Position: 1,
				Tasks: []queue.WorkTask{{ID: "TASK-001", Title: "Implement login", Status: "ready", Agent: "coder", Acceptance: []string{"User can sign in"}, Checks: []string{"go test ./..."}}},
			}},
		},
		Task: queue.WorkTask{ID: "TASK-001", Title: "Implement login", Status: "ready"},
	}
	branch, commit, err := Materialize(ctx, repository, bundle, true)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "trilha/spec-spec-1-round-001" || commit == "" {
		t.Fatalf("branch=%q commit=%q", branch, commit)
	}
	for _, relative := range []string{".trilha/project.md", ".trilha/specs/001-user-access.md", ".trilha/tasks/TASK-001.md"} {
		if _, err := os.Stat(filepath.Join(repository.Path, relative)); err != nil {
			t.Fatalf("%s: %v", relative, err)
		}
	}
	remoteBranch := strings.TrimSpace(runGit(t, origin, "rev-parse", "refs/heads/"+branch))
	if remoteBranch != commit {
		t.Fatalf("remote commit=%q want %q", remoteBranch, commit)
	}
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
