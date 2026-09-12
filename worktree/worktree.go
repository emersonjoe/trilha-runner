// Package worktree gives each task its own checkout. A task runs on
// `trilha/task-nnn`, in `.trilha/runs/TASK-NNN/wt`, and what it changed is
// a branch — reviewable, pushable, discardable — never the maintainer's
// working copy. Everything here is `git` as a subprocess; there is no
// library to depend on and the CLI is what people debug with.
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/emersonjoe/trilha-spec/spec"
)

// Manager creates and lists worktrees under a layout's runs/ directory.
type Manager struct{ Layout spec.Layout }

// Worktree is one checkout.
type Worktree struct {
	Task   string `json:"task"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
}

// Branch answers the branch name of a task.
func Branch(taskID string) string { return "trilha/" + strings.ToLower(taskID) }

// Path answers where a task's worktree lives.
func (m Manager) Path(taskID string) string { return filepath.Join(m.Layout.Runs(), taskID, "wt") }

// Create checks out the task's branch in its worktree, creating the branch
// from HEAD when it does not exist and reusing the worktree when it does.
func (m Manager) Create(ctx context.Context, taskID string) (Worktree, error) {
	w := Worktree{Task: taskID, Path: m.Path(taskID), Branch: Branch(taskID)}
	if _, err := os.Stat(filepath.Join(w.Path, ".git")); err == nil {
		return w, nil
	}
	if err := os.MkdirAll(filepath.Dir(w.Path), 0o755); err != nil {
		return w, err
	}
	if _, err := m.git(ctx, m.Layout.Root, "rev-parse", "--verify", "--quiet", w.Branch); err == nil {
		_, err = m.git(ctx, m.Layout.Root, "worktree", "add", w.Path, w.Branch)
		return w, err
	}
	_, err := m.git(ctx, m.Layout.Root, "worktree", "add", "-b", w.Branch, w.Path, "HEAD")
	return w, err
}

// Remove drops the worktree, keeping the branch.
func (m Manager) Remove(ctx context.Context, taskID string) error {
	_, err := m.git(ctx, m.Layout.Root, "worktree", "remove", "--force", m.Path(taskID))
	if err != nil && strings.Contains(err.Error(), "is not a working tree") {
		return os.RemoveAll(filepath.Dir(m.Path(taskID)))
	}
	return err
}

// List answers the worktrees this manager created, by task.
func (m Manager) List() ([]Worktree, error) {
	entries, err := os.ReadDir(m.Layout.Runs())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Worktree
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(m.Path(e.Name()), ".git")); err == nil {
			out = append(out, Worktree{Task: e.Name(), Path: m.Path(e.Name()), Branch: Branch(e.Name())})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Task < out[j].Task })
	return out, nil
}

// Commit stages everything in the worktree and commits it. It answers the
// new commit's hash, or "" when there was nothing to commit.
func (m Manager) Commit(ctx context.Context, w Worktree, message string) (string, error) {
	if _, err := m.git(ctx, w.Path, "add", "-A"); err != nil {
		return "", err
	}
	status, err := m.git(ctx, w.Path, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(status) == "" {
		return "", nil
	}
	if _, err := m.git(ctx, w.Path, "-c", "user.name=trilha-runner", "-c", "user.email=runner@trilha.local", "commit", "-q", "-m", message); err != nil {
		return "", err
	}
	sha, err := m.git(ctx, w.Path, "rev-parse", "HEAD")
	return strings.TrimSpace(sha), err
}

// DiffStat summarizes what the branch changed against the base it was cut
// from.
func (m Manager) DiffStat(ctx context.Context, w Worktree) (string, error) {
	base, err := m.git(ctx, w.Path, "merge-base", "HEAD", "HEAD@{upstream}")
	if err != nil {
		// No upstream (the usual case): compare with the default branch's tip
		// as recorded when the worktree was made, i.e. the first parent chain.
		out, err := m.git(ctx, w.Path, "diff", "--stat", "HEAD~1", "HEAD")
		if err != nil {
			return "", nil
		}
		return strings.TrimSpace(out), nil
	}
	out, err := m.git(ctx, w.Path, "diff", "--stat", strings.TrimSpace(base), "HEAD")
	return strings.TrimSpace(out), err
}

// IsRepo answers whether root is inside a git repository with a commit.
func (m Manager) IsRepo(ctx context.Context) error {
	if _, err := m.git(ctx, m.Layout.Root, "rev-parse", "--verify", "HEAD"); err != nil {
		return errors.New("worktree: the project must be a git repository with at least one commit")
	}
	return nil
}

func (m Manager) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(errb.String()+out.String()))
	}
	return out.String(), nil
}
