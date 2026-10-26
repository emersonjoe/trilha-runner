package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-spec/execution"
	"github.com/emersonjoe/trilha-spec/spec"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// repo is a git repository with one commit, the smallest project a task can
// run in.
func repo(t *testing.T) Manager {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "user.email", "t@t")
	git(t, dir, "config", "user.name", "t")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("demo\n"), 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "init")
	return Manager{Layout: spec.Layout{Root: dir}}
}

func TestBranchAndPath(t *testing.T) {
	m := Manager{Layout: spec.Layout{Root: "/p"}}
	if got := Branch("TASK-042"); got != "trilha/task-042" {
		t.Fatalf("branch = %q", got)
	}
	if got := m.Path("TASK-042"); got != filepath.Join("/p", ".trilha", "runs", "TASK-042", "wt") {
		t.Fatalf("path = %q", got)
	}
}

func TestCreateCommitLeavesTheCheckoutAlone(t *testing.T) {
	ctx := context.Background()
	m := repo(t)
	head := git(t, m.Layout.Root, "rev-parse", "HEAD")

	w, err := m.Create(ctx, "TASK-001")
	if err != nil {
		t.Fatal(err)
	}
	if w.Branch != "trilha/task-001" || w.Path != m.Path("TASK-001") {
		t.Fatalf("worktree = %+v", w)
	}
	if got := git(t, w.Path, "rev-parse", "--abbrev-ref", "HEAD"); got != w.Branch {
		t.Fatalf("checked out %q", got)
	}

	// Nothing changed: no commit, no error.
	if sha, err := m.Commit(ctx, w, "empty"); err != nil || sha != "" {
		t.Fatalf("empty commit: sha=%q err=%v", sha, err)
	}

	os.WriteFile(filepath.Join(w.Path, "work.txt"), []byte("done\n"), 0o644)
	sha, err := m.Commit(ctx, w, "TASK-001: work")
	if err != nil || sha == "" {
		t.Fatalf("commit: sha=%q err=%v", sha, err)
	}
	if author := git(t, w.Path, "log", "-1", "--format=%an <%ae>"); author != "trilha-runner <runner@trilha.local>" {
		t.Fatalf("author = %q", author)
	}
	if stat, err := m.DiffStat(ctx, w); err != nil || !strings.Contains(stat, "work.txt") {
		t.Fatalf("diffstat = %q err=%v", stat, err)
	}

	// The maintainer's branch and files did not move.
	if got := git(t, m.Layout.Root, "rev-parse", "main"); got != head {
		t.Fatalf("main moved: %s → %s", head, got)
	}
	if _, err := os.Stat(filepath.Join(m.Layout.Root, "work.txt")); !os.IsNotExist(err) {
		t.Fatalf("work.txt reached the checkout: %v", err)
	}
}

func TestCreateReusesWorktreeAndBranch(t *testing.T) {
	ctx := context.Background()
	m := repo(t)
	w, err := m.Create(ctx, "TASK-001")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(w.Path, "first.txt"), []byte("1\n"), 0o644)
	first, _ := m.Commit(ctx, w, "first")

	// Same worktree on disk: reused as is.
	again, err := m.Create(ctx, "TASK-001")
	if err != nil || again != w {
		t.Fatalf("reuse: %+v err=%v", again, err)
	}

	// Worktree removed, branch kept: a new checkout of the same branch,
	// carrying the earlier commit.
	if err := m.Remove(ctx, "TASK-001"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree still on disk: %v", err)
	}
	if got := git(t, m.Layout.Root, "rev-parse", w.Branch); got != first {
		t.Fatalf("branch lost its commit: %s vs %s", got, first)
	}
	back, err := m.Create(ctx, "TASK-001")
	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, back.Path, "rev-parse", "HEAD"); got != first {
		t.Fatalf("recreated at %s, want %s", got, first)
	}
}

func TestRemoveDirectoryGitDoesNotKnow(t *testing.T) {
	m := repo(t)
	stray := m.Path("TASK-009")
	os.MkdirAll(stray, 0o755)
	if err := m.Remove(context.Background(), "TASK-009"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(stray)); !os.IsNotExist(err) {
		t.Fatalf("stray run directory kept: %v", err)
	}
}

func TestListOnlyCheckouts(t *testing.T) {
	ctx := context.Background()
	m := repo(t)
	if list, err := m.List(); err != nil || list != nil {
		t.Fatalf("no runs dir: %v %v", list, err)
	}
	for _, id := range []string{"TASK-002", "TASK-001"} {
		if _, err := m.Create(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	// A run directory without a checkout (evidence only) is not a worktree.
	os.MkdirAll(filepath.Join(m.Layout.Runs(), "TASK-003"), 0o755)
	os.WriteFile(filepath.Join(m.Layout.Runs(), "notes.txt"), nil, 0o644)

	list, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Task != "TASK-001" || list[1].Task != "TASK-002" || list[1].Branch != "trilha/task-002" {
		t.Fatalf("list = %+v", list)
	}
}

func TestIsRepo(t *testing.T) {
	ctx := context.Background()
	if err := repo(t).IsRepo(ctx); err != nil {
		t.Fatalf("repository with a commit: %v", err)
	}
	plain := Manager{Layout: spec.Layout{Root: t.TempDir()}}
	if err := plain.IsRepo(ctx); err == nil || !strings.Contains(err.Error(), "git repository") {
		t.Fatalf("plain directory: %v", err)
	}
	empty := t.TempDir()
	git(t, empty, "init", "-q")
	if err := (Manager{Layout: spec.Layout{Root: empty}}).IsRepo(ctx); err == nil {
		t.Fatal("repository without a commit was accepted")
	}
}

// DV-12: the branch's diff in numbers — files, insertions, deletions — from
// the checkout's HEAD to the task's commit, over two commits; a binary file
// counts as a file with nothing added or removed.
func TestNumStatCountsWhatTheBranchChanged(t *testing.T) {
	ctx := context.Background()
	m := repo(t)
	w, err := m.Create(ctx, "TASK-001")
	if err != nil {
		t.Fatal(err)
	}
	if stats, err := m.NumStat(ctx, w); err != nil || stats.Files != 0 {
		t.Fatalf("fresh branch: %+v %v", stats, err)
	}
	os.WriteFile(filepath.Join(w.Path, "README.md"), []byte("demo\nline two\nline three\n"), 0o644)
	os.WriteFile(filepath.Join(w.Path, "new.txt"), []byte("a\nb\n"), 0o644)
	if _, err := m.Commit(ctx, w, "one"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(w.Path, "README.md"), []byte("line two\nline three\n"), 0o644)
	os.WriteFile(filepath.Join(w.Path, "logo.bin"), []byte{0, 1, 2, 0, 255}, 0o644)
	if _, err := m.Commit(ctx, w, "two"); err != nil {
		t.Fatal(err)
	}
	stats, err := m.NumStat(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 3 || stats.Insertions != 4 || stats.Deletions != 1 {
		t.Fatalf("stats = %+v, want 3 files, 4 insertions, 1 deletion", stats)
	}
}

// Proportional changes: what the maintainer commits on the checkout after the
// task's branch was cut is not the task's change — the count stays the
// branch's own 3 lines against the commit it left from.
func TestNumStatIgnoresWhatTheCheckoutGainedAfterTheCut(t *testing.T) {
	ctx := context.Background()
	m := repo(t)
	w, err := m.Create(ctx, "TASK-001")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(w.Path, "README.md"), []byte("demo\none\ntwo\nthree\n"), 0o644)
	if _, err := m.Commit(ctx, w, "three lines"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(m.Layout.Root, "CHANGELOG.md"), []byte(strings.Repeat("entry\n", 50)), 0o644)
	git(t, m.Layout.Root, "add", "CHANGELOG.md")
	git(t, m.Layout.Root, "commit", "-q", "-m", "the maintainer's own work")
	stats, err := m.NumStat(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if stats != (execution.DiffStats{Files: 1, Insertions: 3}) {
		t.Fatalf("stats = %+v, want 1 file, 3 insertions", stats)
	}
}
