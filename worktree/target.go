package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Mode says how a run gets its branch.
type Mode int

const (
	// Own is the task's own branch: reused when it exists, cut from HEAD
	// when it does not. What an implement run has always done.
	Own Mode = iota
	// Variant is a fresh branch for one variant of a task. It never adopts a
	// branch it cannot prove it created: an existing trilha/task-nnn-vN that
	// is not proven moves the run to -vN-2, -vN-3 and so on.
	Variant
	// Existing works on a branch that must already exist here or on origin
	// (a fix-mode review, a repair run answering review comments); it is
	// never cut from HEAD.
	Existing
	// Detached checks out the tip of an existing branch without a branch of
	// its own: a read-only run (a hunt, a report-mode review). Nothing is
	// created, so there is nothing to adopt or delete.
	Detached
)

// Target is where a run works and on which branch.
type Target struct {
	Task   string
	Key    string
	Branch string
	Mode   Mode
}

// ProofFile is the file, in a run's directory, that names the branch the
// runner created there. Cleanup deletes a branch only when this file proves
// the runner made it.
const ProofFile = "created-branch"

// ErrNoBranch is a branch a run needs that is neither here nor on origin.
var ErrNoBranch = errors.New("worktree: branch not found here or on origin")

// maxVariantSuffix bounds the -vN-2, -vN-3… search.
const maxVariantSuffix = 20

// VariantBranch answers the branch of variant n of a task.
func VariantBranch(taskID string, n int) string {
	return fmt.Sprintf("%s-v%d", Branch(taskID), n)
}

// RunDir answers a run's directory under runs/.
func (m Manager) RunDir(key string) string { return filepath.Join(m.Layout.Runs(), key) }

// ProvenBranch answers the branch the proof file of a run's directory names,
// or "".
func (m Manager) ProvenBranch(key string) string {
	data, err := os.ReadFile(filepath.Join(m.RunDir(key), ProofFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (m Manager) prove(key, branch string) error {
	if err := os.MkdirAll(m.RunDir(key), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.RunDir(key), ProofFile), []byte(branch+"\n"), 0o644)
}

// Open checks out the target's branch in its worktree, as its mode says.
func (m Manager) Open(ctx context.Context, t Target) (Worktree, error) {
	if t.Key == "" {
		t.Key = t.Task
	}
	if t.Branch == "" {
		t.Branch = Branch(t.Task)
	}
	w := Worktree{Task: t.Task, Key: t.Key, Path: m.Path(t.Key), Branch: t.Branch}
	if err := os.MkdirAll(filepath.Dir(w.Path), 0o755); err != nil {
		return w, err
	}
	switch t.Mode {
	case Variant:
		return m.openVariant(ctx, w)
	case Existing:
		return m.openExisting(ctx, w)
	case Detached:
		return m.openDetached(ctx, w)
	}
	return m.openOwn(ctx, w)
}

func (m Manager) has(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

func (m Manager) openOwn(ctx context.Context, w Worktree) (Worktree, error) {
	if m.has(w.Path) {
		return w, nil
	}
	if m.localBranch(ctx, w.Branch) {
		_, err := m.git(ctx, m.Layout.Root, "worktree", "add", w.Path, w.Branch)
		return w, err
	}
	if _, err := m.git(ctx, m.Layout.Root, "worktree", "add", "-b", w.Branch, w.Path, "HEAD"); err != nil {
		return w, err
	}
	return w, m.prove(w.Key, w.Branch)
}

func (m Manager) openVariant(ctx context.Context, w Worktree) (Worktree, error) {
	if m.has(w.Path) {
		if proven := m.ProvenBranch(w.Key); proven != "" {
			w.Branch = proven
		}
		return w, nil
	}
	m.fetch(ctx, w.Branch)
	base := w.Branch
	for n := 1; n <= maxVariantSuffix; n++ {
		candidate := base
		if n > 1 {
			candidate = fmt.Sprintf("%s-%d", base, n)
		}
		local, remote := m.localBranch(ctx, candidate), m.remoteBranch(ctx, candidate)
		if !local && !remote {
			w.Branch = candidate
			if _, err := m.git(ctx, m.Layout.Root, "worktree", "add", "-b", candidate, w.Path, "HEAD"); err != nil {
				return w, err
			}
			return w, m.prove(w.Key, candidate)
		}
		if local && m.ProvenBranch(w.Key) == candidate {
			w.Branch = candidate
			_, err := m.git(ctx, m.Layout.Root, "worktree", "add", w.Path, candidate)
			return w, err
		}
	}
	return w, fmt.Errorf("worktree: %s and its %d alternatives all exist and none was created by this run", base, maxVariantSuffix-1)
}

func (m Manager) openExisting(ctx context.Context, w Worktree) (Worktree, error) {
	if m.has(w.Path) {
		return w, nil
	}
	m.fetch(ctx, w.Branch)
	local, remote := m.localBranch(ctx, w.Branch), m.remoteBranch(ctx, w.Branch)
	switch {
	case local:
		// Origin ahead of the local branch (another worker pushed): move
		// the local branch forward, never back or sideways.
		if remote && m.ancestor(ctx, w.Branch, remoteRef(w.Branch)) {
			_, _ = m.git(ctx, m.Layout.Root, "branch", "-f", w.Branch, remoteRef(w.Branch))
		}
		_, err := m.git(ctx, m.Layout.Root, "worktree", "add", w.Path, w.Branch)
		return w, err
	case remote:
		_, err := m.git(ctx, m.Layout.Root, "worktree", "add", "-b", w.Branch, w.Path, remoteRef(w.Branch))
		return w, err
	}
	return w, fmt.Errorf("%w: %s", ErrNoBranch, w.Branch)
}

func (m Manager) openDetached(ctx context.Context, w Worktree) (Worktree, error) {
	m.fetch(ctx, w.Branch)
	local, remote := m.localBranch(ctx, w.Branch), m.remoteBranch(ctx, w.Branch)
	switch {
	case local && remote && m.ancestor(ctx, w.Branch, remoteRef(w.Branch)):
		w.Ref = remoteRef(w.Branch)
	case local:
		w.Ref = "refs/heads/" + w.Branch
	case remote:
		w.Ref = remoteRef(w.Branch)
	default:
		return w, fmt.Errorf("%w: %s", ErrNoBranch, w.Branch)
	}
	if m.has(w.Path) {
		// A read-only checkout is disposable: the run that left it either
		// changed nothing or was refused for changing something.
		if _, err := m.git(ctx, m.Layout.Root, "worktree", "remove", "--force", w.Path); err != nil {
			return w, err
		}
	}
	if _, err := m.git(ctx, m.Layout.Root, "worktree", "add", "--detach", w.Path, w.Ref); err != nil {
		return w, err
	}
	head, err := m.git(ctx, w.Path, "rev-parse", "HEAD")
	w.Detached, w.Head = true, strings.TrimSpace(head)
	return w, err
}

func remoteRef(branch string) string { return "refs/remotes/origin/" + branch }

func (m Manager) localBranch(ctx context.Context, branch string) bool {
	_, err := m.git(ctx, m.Layout.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func (m Manager) remoteBranch(ctx context.Context, branch string) bool {
	_, err := m.git(ctx, m.Layout.Root, "rev-parse", "--verify", "--quiet", remoteRef(branch))
	return err == nil
}

func (m Manager) ancestor(ctx context.Context, older, newer string) bool {
	_, err := m.git(ctx, m.Layout.Root, "merge-base", "--is-ancestor", older, newer)
	return err == nil
}

// fetch updates origin's copy of a branch, best effort: a checkout without
// origin, or a branch origin does not have, is not an error here.
func (m Manager) fetch(ctx context.Context, branch string) {
	if _, err := m.git(ctx, m.Layout.Root, "remote", "get-url", "origin"); err != nil {
		return
	}
	_, _ = m.git(ctx, m.Layout.Root, "fetch", "--quiet", "origin", "+refs/heads/"+branch+":"+remoteRef(branch))
}

// SHA answers the commit a ref names, from the main checkout.
func (m Manager) SHA(ctx context.Context, ref string) (string, error) {
	out, err := m.git(ctx, m.Layout.Root, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return strings.TrimSpace(out), err
}

// Head answers the commit a worktree is at.
func (m Manager) Head(ctx context.Context, w Worktree) (string, error) {
	out, err := m.git(ctx, w.Path, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// Changes answers what `git status --porcelain --untracked-files=all` says
// in a worktree: every modified, staged, deleted and untracked path.
func (m Manager) Changes(ctx context.Context, w Worktree) ([]string, error) {
	out, err := m.git(ctx, w.Path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var changes []string
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			changes = append(changes, strings.TrimSpace(line))
		}
	}
	return changes, nil
}

// BaseRef answers the first of the candidates that names a commit in the
// main checkout, origin's copy first: the base a review or hunt compares
// against.
func (m Manager) BaseRef(ctx context.Context, branch string) string {
	var candidates []string
	if branch != "" {
		candidates = append(candidates, "refs/remotes/origin/"+branch, "refs/heads/"+branch)
	}
	if out, err := m.git(ctx, m.Layout.Root, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
		candidates = append(candidates, strings.TrimSpace(out))
	}
	candidates = append(candidates, "refs/remotes/origin/main", "refs/heads/main", "refs/heads/master")
	for _, candidate := range candidates {
		if _, err := m.SHA(ctx, candidate); err == nil {
			return candidate
		}
	}
	return ""
}

// MergeBase answers the merge base of a worktree's HEAD and a ref.
func (m Manager) MergeBase(ctx context.Context, w Worktree, ref string) (string, error) {
	out, err := m.git(ctx, w.Path, "merge-base", ref, "HEAD")
	return strings.TrimSpace(out), err
}

// ChangedFiles answers `git diff --name-only <base> HEAD` in a worktree.
func (m Manager) ChangedFiles(ctx context.Context, w Worktree, base string) ([]string, error) {
	out, err := m.git(ctx, w.Path, "diff", "--name-only", base, "HEAD")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			files = append(files, strings.TrimSpace(line))
		}
	}
	return files, nil
}

// Show answers a file as a ref has it, from the main checkout.
func (m Manager) Show(ctx context.Context, ref, path string) ([]byte, error) {
	out, err := m.git(ctx, m.Layout.Root, "show", ref+":"+filepath.ToSlash(path))
	return []byte(out), err
}
