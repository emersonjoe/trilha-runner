package worktree

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Doctor (DV-13a) reports what lives in .trilha/runs/: each run directory's
// class, age and size. It deletes only when an operator typed a flag that
// says so, never a live run, never a branch the runner did not prove it
// created, and never unmerged or uncommitted work without --force-unmerged.

// Class is what a run directory is.
type Class string

const (
	// Live: a runner process or a process inside the worktree holds it.
	Live Class = "live"
	// FinishedKept: a registered worktree of a known task, nobody in it.
	FinishedKept Class = "finished-kept"
	// Orphaned: a worktree git does not know, or of a task that is gone.
	Orphaned Class = "orphaned"
	// MetadataOnly: logs and evidence of a run, no worktree.
	MetadataOnly Class = "metadata-only"
)

// Entry is one run directory.
type Entry struct {
	Key      string        `json:"key"`
	Task     string        `json:"task,omitempty"`
	Class    Class         `json:"class"`
	Reason   string        `json:"reason,omitempty"`
	Dir      string        `json:"dir"`
	Worktree string        `json:"worktree,omitempty"`
	Branch   string        `json:"branch,omitempty"`
	Proven   bool          `json:"proven,omitempty"`
	Merged   *bool         `json:"merged,omitempty"`
	Dirty    bool          `json:"dirty,omitempty"`
	Age      time.Duration `json:"age_ns"`
	Bucket   string        `json:"age"`
	Size     int64         `json:"size_bytes"`
	Pids     []int         `json:"pids,omitempty"`
	// Action is kept, removed, skipped or failed; Note says why.
	Action string `json:"action"`
	Note   string `json:"note,omitempty"`
}

// DoctorOptions are the operator's flags. The zero value only reports.
type DoctorOptions struct {
	PruneOrphans   bool
	PruneOlderThan time.Duration
	ForceUnmerged  bool
	// DefaultBranch is what "merged" means: merged into origin/<it>.
	DefaultBranch string
	Now           func() time.Time
	Log           func(string)
}

// DoctorReport is every entry and the counts.
type DoctorReport struct {
	Entries []Entry        `json:"entries"`
	Classes map[Class]int  `json:"classes"`
	Removed int            `json:"removed"`
	Skipped int            `json:"skipped"`
	Failed  int            `json:"failed"`
	Base    string         `json:"base,omitempty"`
	Notes   []string       `json:"notes,omitempty"`
	Buckets map[string]int `json:"ages"`
}

// Incomplete answers whether something the operator asked for was skipped
// or failed: the doctor command then exits 12.
func (r DoctorReport) Incomplete() bool { return r.Skipped > 0 || r.Failed > 0 }

// Age buckets.
const (
	bucketFresh  = "<14d"
	bucketRecent = "<30d"
	bucketOld    = ">=30d"
)

func bucket(age time.Duration) string {
	switch {
	case age < 14*24*time.Hour:
		return bucketFresh
	case age < 30*24*time.Hour:
		return bucketRecent
	}
	return bucketOld
}

// Doctor classifies every run directory and prunes only what the options
// ask for.
func (m Manager) Doctor(ctx context.Context, opts DoctorOptions) (DoctorReport, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	log := opts.Log
	if log == nil {
		log = func(string) {}
	}
	report := DoctorReport{Classes: map[Class]int{}, Buckets: map[string]int{}}
	entries, err := os.ReadDir(m.Layout.Runs())
	if err != nil {
		if os.IsNotExist(err) {
			return report, nil
		}
		return report, err
	}
	registered := m.registered(ctx)
	report.Base = m.BaseRef(ctx, opts.DefaultBranch)
	table, note, err := processTable(ctx)
	if err != nil {
		note = "process scan failed: " + err.Error()
	}
	if note != "" {
		report.Notes = append(report.Notes, note)
	}
	for _, dirEntry := range entries {
		if !dirEntry.IsDir() {
			continue
		}
		entry := m.inspect(ctx, dirEntry.Name(), registered, report.Base, now(), table)
		report.Classes[entry.Class]++
		report.Buckets[entry.Bucket]++
		entry.Action = "kept"
		if m.wanted(entry, opts) {
			m.prune(ctx, &entry, opts, log)
		}
		switch entry.Action {
		case "removed":
			report.Removed++
		case "skipped":
			report.Skipped++
		case "failed":
			report.Failed++
		}
		report.Entries = append(report.Entries, entry)
	}
	if report.Removed > 0 {
		_, _ = m.git(ctx, m.Layout.Root, "worktree", "prune")
	}
	sort.Slice(report.Entries, func(i, j int) bool { return report.Entries[i].Key < report.Entries[j].Key })
	return report, nil
}

// registered answers the worktree paths git knows, resolved.
func (m Manager) registered(ctx context.Context) map[string]bool {
	known := map[string]bool{}
	out, err := m.git(ctx, m.Layout.Root, "worktree", "list", "--porcelain")
	if err != nil {
		return known
	}
	for _, line := range strings.Split(out, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			known[resolve(strings.TrimSpace(path))] = true
		}
	}
	return known
}

func resolve(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

func (m Manager) inspect(ctx context.Context, key string, registered map[string]bool, base string, now time.Time, table cwdTable) Entry {
	entry := Entry{Key: key, Task: TaskOf(key), Dir: m.RunDir(key)}
	entry.Age, entry.Size = ageAndSize(entry.Dir, now)
	entry.Bucket = bucket(entry.Age)
	wt := m.Path(key)
	_, wtErr := os.Stat(wt)
	hasTree := m.has(wt)
	if wtErr == nil {
		entry.Worktree = wt
	}
	if pid := m.livePid(key, entry.Task); pid != 0 {
		entry.Pids = append(entry.Pids, pid)
	}
	if wtErr == nil {
		entry.Pids = append(entry.Pids, table.in(wt)...)
	}
	if hasTree {
		if branch, err := m.git(ctx, wt, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
			entry.Branch = strings.TrimSpace(branch)
		}
		entry.Proven = entry.Branch != "" && m.ProvenBranch(key) == entry.Branch
		if changes, err := m.Changes(ctx, Worktree{Path: wt}); err == nil {
			entry.Dirty = len(changes) > 0
		}
		if entry.Branch != "" && base != "" {
			merged := m.ancestor(ctx, "refs/heads/"+entry.Branch, base)
			entry.Merged = &merged
		}
	}
	taskKnown := false
	if entry.Task != "" {
		_, err := os.Stat(m.Layout.TaskFile(entry.Task))
		taskKnown = err == nil
	}
	switch {
	case len(entry.Pids) > 0:
		entry.Class, entry.Reason = Live, "held by process "+joinPids(entry.Pids)
	case wtErr != nil:
		entry.Class, entry.Reason = MetadataOnly, "no worktree"
	case !hasTree:
		entry.Class, entry.Reason = Orphaned, "the worktree directory is not a checkout"
	case !registered[resolve(wt)]:
		entry.Class, entry.Reason = Orphaned, "git does not list this worktree"
	case !taskKnown:
		entry.Class, entry.Reason = Orphaned, "no task file for "+firstNonEmpty(entry.Task, key)
	default:
		entry.Class = FinishedKept
	}
	return entry
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func joinPids(pids []int) string {
	parts := make([]string, len(pids))
	for i, pid := range pids {
		parts[i] = strconv.Itoa(pid)
	}
	return strings.Join(parts, ", ")
}

// livePid answers a runner process marked in the run's directory (or its
// task's) that still exists, or 0.
func (m Manager) livePid(key, taskID string) int {
	for _, dir := range []string{key, taskID} {
		if dir == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.RunDir(dir), "runner.pid"))
		if err != nil {
			continue
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 && alive(pid) {
			return pid
		}
	}
	return 0
}

// ageAndSize answers how long since anything at the top of dir (or of its
// worktree) changed, and how many bytes dir holds, symbolic links not
// followed.
func ageAndSize(dir string, now time.Time) (time.Duration, int64) {
	var newest time.Time
	consider := func(path string) {
		if info, err := os.Lstat(path); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	consider(dir)
	if entries, err := os.ReadDir(dir); err == nil {
		for _, entry := range entries {
			consider(filepath.Join(dir, entry.Name()))
		}
	}
	var size int64
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				size += info.Size()
			}
		}
		return nil
	})
	if newest.IsZero() {
		return 0, size
	}
	return now.Sub(newest), size
}

// wanted answers whether the options ask to prune the entry.
func (m Manager) wanted(entry Entry, opts DoctorOptions) bool {
	if entry.Class == Live {
		return false
	}
	if opts.PruneOrphans && entry.Class == Orphaned {
		return true
	}
	return opts.PruneOlderThan > 0 && entry.Age > opts.PruneOlderThan
}

// prune removes one entry in order — processes, worktree, directory, then a
// proven and merged branch — or says why it did not.
func (m Manager) prune(ctx context.Context, entry *Entry, opts DoctorOptions, log func(string)) {
	unmerged := entry.Branch != "" && (entry.Merged == nil || !*entry.Merged)
	switch {
	case unmerged && !opts.ForceUnmerged:
		entry.Action, entry.Note = "skipped", fmt.Sprintf("branch %s is not merged into %s (--force-unmerged removes it anyway)", entry.Branch, firstNonEmpty(baseName(opts.DefaultBranch), "the default branch"))
		return
	case entry.Dirty && !opts.ForceUnmerged:
		entry.Action, entry.Note = "skipped", "the worktree has uncommitted changes (--force-unmerged removes it anyway)"
		return
	}
	step := func(line string) { log(entry.Key + ": " + line) }
	if entry.Worktree != "" {
		Teardown(ctx, entry.Worktree, step)
		if m.has(entry.Worktree) {
			if _, err := m.git(ctx, m.Layout.Root, "worktree", "remove", "--force", entry.Worktree); err != nil {
				step("git worktree remove: " + err.Error() + "; removing the directory")
			}
		}
	}
	if err := os.RemoveAll(entry.Dir); err != nil {
		entry.Action, entry.Note = "failed", err.Error()
		step("remove: " + err.Error())
		return
	}
	entry.Action = "removed"
	step("removed " + entry.Dir)
	switch {
	case entry.Branch == "":
	case !entry.Proven:
		entry.Note = "branch " + entry.Branch + " kept: the runner did not prove it created it"
	default:
		if _, err := m.git(ctx, m.Layout.Root, "branch", "-D", entry.Branch); err != nil {
			entry.Note = "branch " + entry.Branch + " kept: " + err.Error()
		} else {
			entry.Note = "branch " + entry.Branch + " deleted (created by the runner)"
		}
		step(entry.Note)
	}
}

func baseName(branch string) string {
	if branch == "" {
		return ""
	}
	return "origin/" + branch
}
