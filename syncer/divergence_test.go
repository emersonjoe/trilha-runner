package syncer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-runner/queue"
	"github.com/emersonjoe/trilha-spec/spec"
	"github.com/emersonjoe/trilha-spec/task"
)

// A bundle never overwrites a spec or task the Cloud did not write: the
// maintainer's own .trilha/ is theirs.
func TestMaterializeRefusesDivergentFiles(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, layout spec.Layout)
		want    string
	}{
		{"spec managed by another Cloud record", func(t *testing.T, layout spec.Layout) {
			document := spec.NewSpecDoc("001-user-access", "User access", "", "")
			document.Fields.Set("cloud_id", "spec-other")
			if err := layout.SaveSpec(document); err != nil {
				t.Fatal(err)
			}
		}, "managed by another Cloud record"},
		{"task written by hand", func(t *testing.T, layout spec.Layout) {
			saveTask(t, layout, "")
		}, "TASK-001 already exists outside this Cloud specification"},
		{"task of another Cloud specification", func(t *testing.T, layout spec.Layout) {
			saveTask(t, layout, "spec-other")
		}, "TASK-001 already exists outside this Cloud specification"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			root, origin := seedOrigin(t, func(seed string) {
				layout, _, err := spec.Init(seed, spec.InitOptions{Name: "sample-app"})
				if err != nil {
					t.Fatal(err)
				}
				c.prepare(t, layout)
			})
			repository, err := EnsureRepository(ctx, filepath.Join(root, "workspaces"), "sample-app", origin, "main")
			if err != nil {
				t.Fatal(err)
			}
			before := runGit(t, origin, "for-each-ref", "--format=%(refname)")
			if _, _, err := Materialize(ctx, repository, sampleBundle(origin), true); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if after := runGit(t, origin, "for-each-ref", "--format=%(refname)"); after != before {
				t.Fatalf("a refused bundle reached the remote:\n%s", after)
			}
		})
	}
}

// The same bundle again — a retried run — is accepted: its files carry the
// same cloud_id.
func TestMaterializeSameBundleTwice(t *testing.T) {
	ctx := context.Background()
	root, origin := seedOrigin(t, nil)
	repository, err := EnsureRepository(ctx, filepath.Join(root, "workspaces"), "sample-app", origin, "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Materialize(ctx, repository, sampleBundle(origin), true); err != nil {
		t.Fatal(err)
	}
	// The spec branch was pushed; main still has no .trilha/. Materialize
	// again from a checkout where the earlier files are present.
	runGit(t, repository.Path, "merge", "-q", "--ff-only", "trilha/spec-spec-1-round-001")
	runGit(t, repository.Path, "push", "-q", "origin", "HEAD:main")
	if _, _, err := Materialize(ctx, repository, sampleBundle(origin), false); err != nil {
		t.Fatalf("same bundle refused: %v", err)
	}
}

func TestEnsureRepositoryReusesAndGuardsTheCheckout(t *testing.T) {
	ctx := context.Background()
	root, origin := seedOrigin(t, nil)
	workspaces := filepath.Join(root, "workspaces")
	repository, err := EnsureRepository(ctx, workspaces, "sample-app", origin, "main")
	if err != nil {
		t.Fatal(err)
	}

	// Leftovers of an earlier run are discarded: the checkout is the
	// runner's, reset to the remote default branch.
	os.WriteFile(filepath.Join(repository.Path, "README.md"), []byte("dirty\n"), 0o600)
	runGit(t, repository.Path, "switch", "-q", "-c", "leftover")
	again, err := EnsureRepository(ctx, workspaces, "sample-app", origin, "main")
	if err != nil || again != repository {
		t.Fatalf("reuse: %+v %v", again, err)
	}
	if branch := strings.TrimSpace(runGit(t, repository.Path, "branch", "--show-current")); branch != "main" {
		t.Fatalf("branch = %q", branch)
	}
	if content, _ := os.ReadFile(filepath.Join(repository.Path, "README.md")); string(content) != "# app\n" {
		t.Fatalf("README = %q", content)
	}

	// The same project name pointing at another remote is refused, not
	// silently reused.
	_, other := seedOrigin(t, nil)
	if _, err := EnsureRepository(ctx, workspaces, "sample-app", other, "main"); err == nil || !strings.Contains(err.Error(), "origin does not match") {
		t.Fatalf("other origin: %v", err)
	}

	for _, c := range []struct{ project, remote, branch, want string }{
		{"../escape", origin, "main", "invalid project name"},
		{"Sample", origin, "main", "invalid project name"},
		{"sample-app", "", "main", "repository are required"},
		{"sample-app", origin + "\nother", "main", "repository are required"},
		{"sample-app", origin, "main~1", "invalid default branch"},
		{"sample-app", origin, "a b", "invalid default branch"},
	} {
		if _, err := EnsureRepository(ctx, workspaces, c.project, c.remote, c.branch); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("EnsureRepository(%q, %q, %q) = %v, want %q", c.project, c.remote, c.branch, err, c.want)
		}
	}
}

// The implementation branch is published from the task's worktree. A rerun
// that rewrote the branch is pushed again; a branch someone else moved on
// the remote is not overwritten.
func TestPushBranch(t *testing.T) {
	ctx := context.Background()
	root, origin := seedOrigin(t, nil)
	repository, err := EnsureRepository(ctx, filepath.Join(root, "workspaces"), "sample-app", origin, "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := PushBranch(ctx, repository.Path, " "); err != nil {
		t.Fatalf("no branch is a no-op: %v", err)
	}

	wt := filepath.Join(root, "wt")
	runGit(t, repository.Path, "worktree", "add", "-q", "-b", "trilha/task-001", wt)
	commit := func(content string) string {
		os.WriteFile(filepath.Join(wt, "work.txt"), []byte(content), 0o600)
		runGit(t, wt, "add", "-A")
		runGit(t, wt, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--amend", "--allow-empty", "-m", "TASK-001")
		return strings.TrimSpace(runGit(t, wt, "rev-parse", "HEAD"))
	}
	remote := func() string {
		return strings.TrimSpace(runGit(t, origin, "rev-parse", "refs/heads/trilha/task-001"))
	}

	first := commit("one\n")
	if err := PushBranch(ctx, wt, "trilha/task-001"); err != nil || remote() != first {
		t.Fatalf("first push: %v (remote %s, want %s)", err, remote(), first)
	}
	second := commit("two\n")
	if err := PushBranch(ctx, wt, "trilha/task-001"); err != nil || remote() != second {
		t.Fatalf("rewritten branch: %v (remote %s, want %s)", err, remote(), second)
	}

	// Someone else pushes to the branch; the runner's next push must not
	// discard their commit.
	other := filepath.Join(root, "other")
	runGit(t, root, "clone", "-q", "--branch", "trilha/task-001", origin, other)
	os.WriteFile(filepath.Join(other, "theirs.txt"), []byte("theirs\n"), 0o600)
	runGit(t, other, "add", "-A")
	runGit(t, other, "-c", "user.name=other", "-c", "user.email=other@example.com", "commit", "-q", "-m", "theirs")
	runGit(t, other, "push", "-q", "origin", "trilha/task-001")
	theirs := remote()

	commit("three\n")
	if err := PushBranch(ctx, wt, "trilha/task-001"); err == nil {
		t.Fatal("pushed over a commit the runner had not seen")
	}
	if remote() != theirs {
		t.Fatalf("their commit was overwritten: remote %s, want %s", remote(), theirs)
	}
}

func saveTask(t *testing.T, layout spec.Layout, cloudID string) {
	t.Helper()
	item := &task.Task{ID: "TASK-001", Title: "Hand-written", Status: task.Ready, Acceptance: []string{"ok"}}
	if cloudID != "" {
		item.Fields.Set("cloud_id", cloudID)
	}
	if err := (&task.Store{Layout: layout}).Save(item); err != nil {
		t.Fatal(err)
	}
}

// Every task of a round materializes the same spec branch. A worker with
// --push publishes it for the first task and again for the second; the
// second push must not be refused as stale.
func TestMaterializeSecondTaskOfTheRoundPushes(t *testing.T) {
	ctx := context.Background()
	root, origin := seedOrigin(t, nil)
	workspaces := filepath.Join(root, "workspaces")
	bundle := sampleBundle(origin)
	bundle.Round.Stages[0].Tasks = append(bundle.Round.Stages[0].Tasks, queue.WorkTask{ID: "TASK-002", Title: "Logout", Status: "idea", Acceptance: []string{"User can sign out"}})

	repository, err := EnsureRepository(ctx, workspaces, "sample-app", origin, "main")
	if err != nil {
		t.Fatal(err)
	}
	branch, _, err := Materialize(ctx, repository, bundle, true)
	if err != nil {
		t.Fatal(err)
	}

	// The next claim: the worker resets its checkout and materializes the
	// round again, now for TASK-002.
	if repository, err = EnsureRepository(ctx, workspaces, "sample-app", origin, "main"); err != nil {
		t.Fatal(err)
	}
	bundle.RunID, bundle.Task = "run-2", queue.WorkTask{ID: "TASK-002", Title: "Logout", Status: "ready"}
	_, commit, err := Materialize(ctx, repository, bundle, true)
	if err != nil {
		t.Fatalf("second task of the round: %v", err)
	}
	if remote := strings.TrimSpace(runGit(t, origin, "rev-parse", "refs/heads/"+branch)); remote != commit {
		t.Fatalf("remote %s, want %s", remote, commit)
	}
}

// A worker upgraded in the middle of a round has a checkout cloned before
// the runner tracked its branches, and a spec branch already on the remote.
// The first claim after the upgrade adopts what is there, once.
func TestCheckoutFromBeforeTrackingRecovers(t *testing.T) {
	ctx := context.Background()
	root, origin := seedOrigin(t, nil)
	workspaces := filepath.Join(root, "workspaces")
	bundle := sampleBundle(origin)
	bundle.Round.Stages[0].Tasks = append(bundle.Round.Stages[0].Tasks, queue.WorkTask{ID: "TASK-002", Title: "Logout", Status: "idea", Acceptance: []string{"User can sign out"}})
	repository, err := EnsureRepository(ctx, workspaces, "sample-app", origin, "main")
	if err != nil {
		t.Fatal(err)
	}
	branch, _, err := Materialize(ctx, repository, bundle, true)
	if err != nil {
		t.Fatal(err)
	}
	// Back to how an earlier checkout looks: no refspec, no tracking ref.
	runGit(t, repository.Path, "config", "--unset", "remote.origin.fetch", `^\+refs/heads/trilha/`)
	runGit(t, repository.Path, "update-ref", "-d", "refs/remotes/origin/"+branch)

	if repository, err = EnsureRepository(ctx, workspaces, "sample-app", origin, "main"); err != nil {
		t.Fatal(err)
	}
	bundle.RunID, bundle.Task = "run-2", queue.WorkTask{ID: "TASK-002", Title: "Logout", Status: "ready"}
	if _, _, err := Materialize(ctx, repository, bundle, true); err != nil {
		t.Fatalf("after upgrade: %v", err)
	}
	// Reusing the checkout again does not add the refspec twice.
	EnsureRepository(ctx, workspaces, "sample-app", origin, "main")
	if refspecs := runGit(t, repository.Path, "config", "--get-all", "remote.origin.fetch"); strings.Count(refspecs, "refs/heads/trilha/") != 1 {
		t.Fatalf("refspecs:\n%s", refspecs)
	}
}
