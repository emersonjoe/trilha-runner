package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// withOrigin gives a repository a bare origin holding main, and a second
// clone ("another worker") that can push branches to it.
func withOrigin(t *testing.T, m Manager) (other string) {
	t.Helper()
	origin := filepath.Join(t.TempDir(), "origin.git")
	git(t, m.Layout.Root, "init", "-q", "--bare", origin)
	git(t, m.Layout.Root, "remote", "add", "origin", origin)
	git(t, m.Layout.Root, "push", "-q", "origin", "main")
	other = filepath.Join(t.TempDir(), "other")
	git(t, m.Layout.Root, "clone", "-q", origin, other)
	git(t, other, "config", "user.email", "t@t")
	git(t, other, "config", "user.name", "t")
	return other
}

// DV-05b: a variant gets trilha/task-nnn-v<N>, proves it created it, and
// never adopts a branch someone else made — an existing -v2 moves the run to
// -v2-2 and stays untouched; once proven, the same run reuses its branch.
func TestVariantBranchIsProvenAndNeverAdoptsAnother(t *testing.T) {
	ctx := context.Background()
	m := repo(t)
	first, err := m.Open(ctx, Target{Task: "TASK-001", Key: "TASK-001-v1", Branch: VariantBranch("TASK-001", 1), Mode: Variant})
	if err != nil {
		t.Fatal(err)
	}
	if first.Branch != "trilha/task-001-v1" || m.ProvenBranch("TASK-001-v1") != "trilha/task-001-v1" || first.Path != m.Path("TASK-001-v1") {
		t.Fatalf("variant 1 = %+v, proof %q", first, m.ProvenBranch("TASK-001-v1"))
	}
	// Someone else's branch with the name variant 2 would take.
	git(t, m.Layout.Root, "branch", "trilha/task-001-v2")
	theirs := git(t, m.Layout.Root, "rev-parse", "trilha/task-001-v2")
	second, err := m.Open(ctx, Target{Task: "TASK-001", Key: "TASK-001-v2", Branch: VariantBranch("TASK-001", 2), Mode: Variant})
	if err != nil {
		t.Fatal(err)
	}
	if second.Branch != "trilha/task-001-v2-2" || m.ProvenBranch("TASK-001-v2") != "trilha/task-001-v2-2" {
		t.Fatalf("variant 2 adopted an unproven branch: %+v", second)
	}
	if git(t, m.Layout.Root, "rev-parse", "trilha/task-001-v2") != theirs {
		t.Fatal("the existing branch moved")
	}
	if current := git(t, second.Path, "branch", "--show-current"); current != "trilha/task-001-v2-2" {
		t.Fatalf("worktree on %q", current)
	}
	// The same run again, after its worktree was removed: its proof lets it
	// take its own branch back.
	if err := m.Remove(ctx, "TASK-001-v2"); err != nil {
		t.Fatal(err)
	}
	if err := m.prove("TASK-001-v2", "trilha/task-001-v2-2"); err != nil {
		t.Fatal(err)
	}
	again, err := m.Open(ctx, Target{Task: "TASK-001", Key: "TASK-001-v2", Branch: VariantBranch("TASK-001", 2), Mode: Variant})
	if err != nil || again.Branch != "trilha/task-001-v2-2" {
		t.Fatalf("again = %+v, %v", again, err)
	}
	list, _ := m.List()
	if len(list) != 2 || list[1].Key != "TASK-001-v2" || list[1].Task != "TASK-001" || list[1].Branch != "trilha/task-001-v2-2" {
		t.Fatalf("list = %+v", list)
	}
}

// DV-06/DV-08: a run that needs an existing branch takes it from origin when
// this worker does not have it (another worker pushed it), never cuts it from
// HEAD, and proves nothing it did not create.
func TestExistingBranchComesFromOriginOrIsRefused(t *testing.T) {
	ctx := context.Background()
	m := repo(t)
	other := withOrigin(t, m)
	if _, err := m.Open(ctx, Target{Task: "TASK-001", Mode: Existing}); !errors.Is(err, ErrNoBranch) {
		t.Fatalf("missing branch = %v", err)
	}
	git(t, other, "switch", "-q", "-c", "trilha/task-001")
	os.WriteFile(filepath.Join(other, "work.txt"), []byte("work\n"), 0o644)
	git(t, other, "add", "-A")
	git(t, other, "commit", "-q", "-m", "work")
	git(t, other, "push", "-q", "origin", "trilha/task-001")
	pushed := git(t, other, "rev-parse", "HEAD")
	w, err := m.Open(ctx, Target{Task: "TASK-001", Mode: Existing})
	if err != nil {
		t.Fatal(err)
	}
	if head := git(t, w.Path, "rev-parse", "HEAD"); head != pushed {
		t.Fatalf("worktree at %s, origin at %s", head, pushed)
	}
	if m.ProvenBranch("TASK-001") != "" {
		t.Fatal("a branch taken from origin was proven as created")
	}
}

// DV-06/DV-07: a read-only run checks out the branch's tip detached — no
// branch created, the one under review left alone — and Changes sees any
// file it touches.
func TestDetachedCheckoutCreatesNoBranch(t *testing.T) {
	ctx := context.Background()
	m := repo(t)
	git(t, m.Layout.Root, "branch", "trilha/task-001")
	tip := git(t, m.Layout.Root, "rev-parse", "trilha/task-001")
	branches := git(t, m.Layout.Root, "branch", "--list")
	w, err := m.Open(ctx, Target{Task: "TASK-001", Key: "TASK-001-hunt", Mode: Detached})
	if err != nil {
		t.Fatal(err)
	}
	if !w.Detached || w.Head != tip || w.Branch != "trilha/task-001" || w.Ref != "refs/heads/trilha/task-001" {
		t.Fatalf("worktree = %+v", w)
	}
	if git(t, m.Layout.Root, "branch", "--list") != branches || m.ProvenBranch("TASK-001-hunt") != "" {
		t.Fatal("a read-only checkout created or proved a branch")
	}
	if changes, err := m.Changes(ctx, w); err != nil || len(changes) != 0 {
		t.Fatalf("fresh checkout changes = %v %v", changes, err)
	}
	os.WriteFile(filepath.Join(w.Path, "new.txt"), []byte("x"), 0o644)
	if changes, _ := m.Changes(ctx, w); len(changes) != 1 {
		t.Fatalf("changes = %v", changes)
	}
	// Opened again, the disposable checkout starts clean.
	again, err := m.Open(ctx, Target{Task: "TASK-001", Key: "TASK-001-hunt", Mode: Detached})
	if err != nil {
		t.Fatal(err)
	}
	if changes, _ := m.Changes(ctx, again); len(changes) != 0 {
		t.Fatalf("reopened checkout changes = %v", changes)
	}
	if _, err := m.Open(ctx, Target{Task: "TASK-002", Key: "TASK-002-hunt", Mode: Detached}); !errors.Is(err, ErrNoBranch) {
		t.Fatalf("missing branch = %v", err)
	}
	if base := m.BaseRef(ctx, "main"); base != "refs/heads/main" {
		t.Fatalf("base = %q", base)
	}
	if TaskOf("TASK-001-hunt-run-000007") != "TASK-001" || TaskOf("notes") != "" {
		t.Fatal("TaskOf")
	}
}
