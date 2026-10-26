package worktree

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// countFiles answers how many files and directories are under dir.
func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		n++
		return nil
	})
	return n
}

// background starts a program whose current directory is dir and reaps it,
// answering a channel that closes when it exits.
func background(t *testing.T, dir string, args ...string) (*exec.Cmd, <-chan struct{}) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	t.Cleanup(func() { cmd.Process.Kill(); <-done })
	return cmd, done
}

// taskFile makes the task known to the layout.
func taskFile(t *testing.T, m Manager, id string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(m.Layout.TaskFile(id)), 0o755)
	os.WriteFile(m.Layout.TaskFile(id), []byte("---\nid: "+id+"\ntitle: t\nstatus: review\n---\n"), 0o644)
}

func age(t *testing.T, m Manager, key string, days int) {
	t.Helper()
	when := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	dir := m.RunDir(key)
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		os.Chtimes(filepath.Join(dir, entry.Name()), when, when)
	}
	os.Chtimes(dir, when, when)
}

// doctorRepo has one run directory of each class: TASK-001 finished and
// kept, TASK-002 orphaned (its task is gone), TASK-003 metadata only, 40
// days old, TASK-004 live (a process sits in its worktree), TASK-005 an
// orphan whose proven branch has unmerged work, TASK-006 an orphan whose
// branch the runner never proved it created.
func doctorRepo(t *testing.T) Manager {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	ctx := context.Background()
	m := repo(t)
	for _, id := range []string{"TASK-001", "TASK-002", "TASK-004", "TASK-005", "TASK-006"} {
		if _, err := m.Create(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	taskFile(t, m, "TASK-001")
	taskFile(t, m, "TASK-004")
	os.MkdirAll(m.RunDir("TASK-003"), 0o755)
	os.WriteFile(filepath.Join(m.RunDir("TASK-003"), "agent.log"), []byte("done\n"), 0o644)
	age(t, m, "TASK-003", 40)
	age(t, m, "TASK-001", 20)
	w5 := m.Path("TASK-005")
	os.WriteFile(filepath.Join(w5, "work.txt"), []byte("unmerged\n"), 0o644)
	git(t, w5, "add", "-A")
	git(t, w5, "commit", "-q", "-m", "unmerged work")
	os.Remove(filepath.Join(m.RunDir("TASK-006"), ProofFile))
	background(t, m.Path("TASK-004"), "sleep", "60")
	return m
}

// DV-13a: the doctor without flags classifies and sizes every run directory
// and deletes nothing — the file count is the same before and after.
func TestDoctorWithoutFlagsNeverDeletes(t *testing.T) {
	m := doctorRepo(t)
	before := countFiles(t, m.Layout.Runs())
	branches := git(t, m.Layout.Root, "branch", "--list")
	report, err := m.Doctor(context.Background(), DoctorOptions{DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if after := countFiles(t, m.Layout.Runs()); after != before || git(t, m.Layout.Root, "branch", "--list") != branches {
		t.Fatalf("the doctor deleted something: %d files before, %d after", before, after)
	}
	want := map[string]Class{"TASK-001": FinishedKept, "TASK-002": Orphaned, "TASK-003": MetadataOnly, "TASK-004": Live, "TASK-005": Orphaned, "TASK-006": Orphaned}
	if len(report.Entries) != len(want) || report.Incomplete() || report.Removed != 0 {
		t.Fatalf("report = %+v", report)
	}
	for _, entry := range report.Entries {
		if entry.Class != want[entry.Key] || entry.Action != "kept" || entry.Size <= 0 {
			t.Errorf("%s = %s (%s), action %s, size %d", entry.Key, entry.Class, entry.Reason, entry.Action, entry.Size)
		}
	}
	buckets := map[string]string{}
	for _, entry := range report.Entries {
		buckets[entry.Key] = entry.Bucket
	}
	if buckets["TASK-001"] != "<30d" || buckets["TASK-003"] != ">=30d" || buckets["TASK-002"] != "<14d" {
		t.Fatalf("buckets = %v", buckets)
	}
	if e := report.Entries[4]; e.Key != "TASK-005" || e.Merged == nil || *e.Merged || !e.Proven {
		t.Fatalf("TASK-005 = %+v", e)
	}
}

// DV-13a: --prune-orphans removes orphans whose branch is merged, deleting a
// branch only when the runner proved it created it; it skips unmerged work
// (exit 12) unless --force-unmerged; --prune-older-than removes old entries
// of any class but live, never a live one.
func TestDoctorPrunesOnlyWhatTheFlagsSay(t *testing.T) {
	ctx := context.Background()
	m := doctorRepo(t)
	report, err := m.Doctor(ctx, DoctorOptions{PruneOrphans: true, DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]Entry{}
	for _, entry := range report.Entries {
		actions[entry.Key] = entry
	}
	if actions["TASK-002"].Action != "removed" || actions["TASK-006"].Action != "removed" || actions["TASK-005"].Action != "skipped" ||
		actions["TASK-001"].Action != "kept" || actions["TASK-003"].Action != "kept" || actions["TASK-004"].Action != "kept" || !report.Incomplete() {
		t.Fatalf("actions = %+v", report.Entries)
	}
	if _, err := os.Stat(m.RunDir("TASK-002")); !os.IsNotExist(err) {
		t.Fatal("TASK-002 is still there")
	}
	branches := git(t, m.Layout.Root, "branch", "--list")
	if strings.Contains(branches, "trilha/task-002") || !strings.Contains(branches, "trilha/task-006") || !strings.Contains(branches, "trilha/task-005") {
		t.Fatalf("branches = %s", branches)
	}
	if !strings.Contains(actions["TASK-006"].Note, "did not prove") || !strings.Contains(actions["TASK-005"].Note, "not merged") {
		t.Fatalf("notes: %q / %q", actions["TASK-006"].Note, actions["TASK-005"].Note)
	}
	report, err = m.Doctor(ctx, DoctorOptions{PruneOrphans: true, ForceUnmerged: true, PruneOlderThan: 30 * 24 * time.Hour, DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Incomplete() || report.Removed != 2 {
		t.Fatalf("second pass = %+v", report)
	}
	if strings.Contains(git(t, m.Layout.Root, "branch", "--list"), "trilha/task-005") {
		t.Fatal("the forced unmerged, proven branch was kept")
	}
	if _, err := os.Stat(m.RunDir("TASK-003")); !os.IsNotExist(err) {
		t.Fatal("the 40-day-old metadata was kept")
	}
	for _, key := range []string{"TASK-001", "TASK-004"} {
		if _, err := os.Stat(m.Path(key)); err != nil {
			t.Fatalf("%s was removed: %v", key, err)
		}
	}
	// Old but live: never.
	age(t, m, "TASK-004", 60)
	report, _ = m.Doctor(ctx, DoctorOptions{PruneOlderThan: time.Hour, DefaultBranch: "main"})
	for _, entry := range report.Entries {
		if entry.Key == "TASK-004" && (entry.Class != Live || entry.Action != "kept") {
			t.Fatalf("live entry = %+v", entry)
		}
	}
}

// DV-13b: cleaning a worktree stops the processes inside it first — one that
// exits on SIGTERM, one that ignores it and is killed after the grace — and
// then removes the worktree, keeping the branch.
func TestCleanStopsProcessesInsideTheWorktreeBeforeRemovingIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh and signals")
	}
	ctx := context.Background()
	m := repo(t)
	w, err := m.Create(ctx, "TASK-001")
	if err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(w.Path, "server")
	os.MkdirAll(sub, 0o755)
	_, polite := background(t, w.Path, "sleep", "60")
	_, stubborn := background(t, sub, "sh", "-c", `trap "" TERM; while :; do sleep 0.2; done`)
	_, untouched := background(t, m.Layout.Root, "sleep", "60")
	time.Sleep(200 * time.Millisecond) // let sh install its trap
	if pids, note, err := ProcessesIn(ctx, w.Path); err != nil || note != "" || len(pids) < 2 {
		t.Fatalf("processes in the worktree = %v %q %v", pids, note, err)
	}
	var log []string
	if err := m.Clean(ctx, "TASK-001", func(line string) { log = append(log, line) }); err != nil {
		t.Fatal(err)
	}
	for name, done := range map[string]<-chan struct{}{"polite": polite, "stubborn": stubborn} {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("the %s process survived the teardown:\n%s", name, strings.Join(log, "\n"))
		}
	}
	select {
	case <-untouched:
		t.Fatal("a process outside the worktree was stopped")
	default:
	}
	joined := strings.Join(log, "\n")
	if !strings.Contains(joined, "SIGTERM") || !strings.Contains(joined, "SIGKILL") || !strings.Contains(joined, "removed") {
		t.Fatalf("log:\n%s", joined)
	}
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Fatal("the worktree is still there")
	}
	if git(t, m.Layout.Root, "branch", "--list", "trilha/task-001") == "" {
		t.Fatal("cleaning deleted the branch")
	}
}
