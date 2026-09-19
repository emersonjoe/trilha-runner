// Package syncer materializes Cloud work bundles in a dedicated Git checkout.
package syncer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/emersonjoe/trilha-runner/queue"
	"github.com/emersonjoe/trilha-spec/spec"
	"github.com/emersonjoe/trilha-spec/task"
)

var safeName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Repository is one dedicated executor checkout.
type Repository struct {
	Path          string
	DefaultBranch string
}

// EnsureRepository clones a missing checkout or resets an existing dedicated
// checkout to the configured remote default branch.
func EnsureRepository(ctx context.Context, root, project, remote, defaultBranch string) (Repository, error) {
	if !safeName.MatchString(project) {
		return Repository{}, errors.New("syncer: invalid project name")
	}
	if strings.TrimSpace(root) == "" || strings.TrimSpace(remote) == "" || strings.ContainsAny(remote, "\r\n") {
		return Repository{}, errors.New("syncer: workspace root and repository are required")
	}
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	if strings.ContainsAny(defaultBranch, " ~^:?*[\\\r\n") {
		return Repository{}, errors.New("syncer: invalid default branch")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Repository{}, err
	}
	if err := os.MkdirAll(absRoot, 0o700); err != nil {
		return Repository{}, err
	}
	path := filepath.Join(absRoot, project)
	if filepath.Dir(path) != absRoot {
		return Repository{}, errors.New("syncer: workspace escaped root")
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); os.IsNotExist(err) {
		if _, err := git(ctx, absRoot, "clone", "--branch", defaultBranch, "--single-branch", "--", remote, project); err != nil {
			return Repository{}, err
		}
	} else if err != nil {
		return Repository{}, err
	} else {
		origin, err := git(ctx, path, "remote", "get-url", "origin")
		if err != nil || strings.TrimSpace(origin) != remote {
			return Repository{}, errors.New("syncer: checkout origin does not match configured repository")
		}
		if _, err := git(ctx, path, "fetch", "--prune", "origin", defaultBranch); err != nil {
			return Repository{}, err
		}
		if _, err := git(ctx, path, "switch", "-f", defaultBranch); err != nil {
			return Repository{}, err
		}
		if _, err := git(ctx, path, "reset", "--hard", "origin/"+defaultBranch); err != nil {
			return Repository{}, err
		}
	}
	return Repository{Path: path, DefaultBranch: defaultBranch}, nil
}

// Materialize writes a versioned bundle through the trilha-spec libraries and
// commits it on a dedicated spec branch. It refuses divergent managed files.
func Materialize(ctx context.Context, repository Repository, bundle queue.Bundle, push bool) (string, string, error) {
	if bundle.Version != 1 || bundle.Task.ID == "" || bundle.Specification.Title == "" {
		return "", "", errors.New("syncer: invalid work bundle")
	}
	branch := fmt.Sprintf("trilha/spec-%s-round-%03d", slug(bundle.Specification.ID), bundle.Round.Number)
	if _, err := git(ctx, repository.Path, "switch", "-C", branch, "origin/"+repository.DefaultBranch); err != nil {
		return "", "", err
	}
	layout, _, err := spec.Init(repository.Path, spec.InitOptions{Name: bundle.Project, Description: bundle.Specification.Description})
	if err != nil {
		return "", "", err
	}
	specID := bundle.Specification.ProtocolID
	if !spec.ValidSpecID(specID) {
		specID = fmt.Sprintf("%03d-%s", max(1, bundle.Specification.Version), slug(bundle.Specification.Title))
	}
	// The body is the bundle's, so the language of the protocol's template
	// never comes into it.
	document := spec.NewSpecDoc(specID, bundle.Specification.Title, "", specificationBody(bundle))
	document.Status = "approved"
	document.Fields.Set("cloud_id", bundle.Specification.ID)
	document.Fields.Set("cloud_round", bundle.Round.ID)
	if err := refuseDivergentSpec(layout, document); err != nil {
		return "", "", err
	}
	if err := layout.SaveSpec(document); err != nil {
		return "", "", err
	}
	store, err := task.Open(repository.Path)
	if err != nil {
		return "", "", err
	}
	for _, stage := range bundle.Round.Stages {
		for _, item := range stage.Tasks {
			status := protocolStatus(item.Status)
			if item.ID == bundle.Task.ID {
				status = task.Ready
			}
			record := &task.Task{ID: item.ID, Title: item.Title, Status: status, Spec: specID, Agent: first(item.Agent, "coder"), DependsOn: append([]string(nil), item.DependsOn...), Acceptance: append([]string(nil), item.Acceptance...), Checks: append([]string(nil), item.Checks...)}
			record.Fields.Set("cloud_id", bundle.Specification.ID)
			record.Fields.Set("cloud_round", bundle.Round.ID)
			record.Fields.Set("cloud_stage", stage.ID)
			if err := refuseDivergentTask(store, record); err != nil {
				return "", "", err
			}
			if err := store.Save(record); err != nil {
				return "", "", err
			}
		}
	}
	if _, err := git(ctx, repository.Path, "add", "--", ".trilha"); err != nil {
		return "", "", err
	}
	status, err := git(ctx, repository.Path, "status", "--porcelain", "--", ".trilha")
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(status) != "" {
		if _, err := git(ctx, repository.Path, "-c", "user.name=trilha-runner", "-c", "user.email=runner@trilha.local", "commit", "-m", fmt.Sprintf("%s: synchronize %s", bundle.Task.ID, bundle.Specification.Title)); err != nil {
			return "", "", err
		}
	}
	commit, err := git(ctx, repository.Path, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	if push {
		if _, err := git(ctx, repository.Path, "push", "--force-with-lease", "-u", "origin", branch); err != nil {
			return "", "", err
		}
	}
	return branch, strings.TrimSpace(commit), nil
}

// PushBranch publishes the implementation branch produced by the runner.
func PushBranch(ctx context.Context, worktreePath, branch string) error {
	if strings.TrimSpace(branch) == "" {
		return nil
	}
	_, err := git(ctx, worktreePath, "push", "--force-with-lease", "-u", "origin", branch)
	return err
}

func refuseDivergentSpec(layout spec.Layout, wanted *spec.Spec) error {
	existing, err := layout.LoadSpec(wanted.ID)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("syncer: read specification %s: %w", wanted.ID, err)
	}
	if existing.Fields.Get("cloud_id") != "" && existing.Fields.Get("cloud_id") != wanted.Fields.Get("cloud_id") {
		return fmt.Errorf("syncer: specification %s is managed by another Cloud record", wanted.ID)
	}
	return nil
}

func refuseDivergentTask(store *task.Store, wanted *task.Task) error {
	if _, err := os.Stat(store.Layout.TaskFile(wanted.ID)); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("syncer: inspect task %s: %w", wanted.ID, err)
	}
	existing, err := store.Get(wanted.ID)
	if err != nil {
		return fmt.Errorf("syncer: read task %s: %w", wanted.ID, err)
	}
	cloudID := existing.Fields.Get("cloud_id")
	if cloudID == "" || cloudID != wanted.Fields.Get("cloud_id") {
		return fmt.Errorf("syncer: task %s already exists outside this Cloud specification", wanted.ID)
	}
	return nil
}

func specificationBody(bundle queue.Bundle) string {
	var body strings.Builder
	fmt.Fprintf(&body, "# %s\n\n## Why\n\n%s\n\n## Development round %d\n\n%s\n\n## Stages\n", bundle.Specification.Title, first(bundle.Specification.Description, "Managed through Trilha Cloud."), bundle.Round.Number, first(bundle.Round.Goal, "Current delivery iteration."))
	for _, stage := range bundle.Round.Stages {
		fmt.Fprintf(&body, "\n### %s\n", stage.Title)
		for _, item := range stage.Tasks {
			fmt.Fprintf(&body, "\n- `%s` — %s\n", item.ID, item.Title)
		}
	}
	return body.String()
}

func protocolStatus(status string) task.Status {
	switch status {
	case "ready":
		return task.Ready
	case "running":
		return task.Running
	case "review":
		return task.Review
	case "blocked":
		return task.Blocked
	case "done":
		return task.Done
	default:
		return task.Idea
	}
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()+stdout.String()))
	}
	return stdout.String(), nil
}

func slug(value string) string {
	value = strings.ToLower(value)
	var result strings.Builder
	dash := false
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			result.WriteRune(character)
			dash = false
		} else if !dash && result.Len() > 0 {
			result.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(result.String(), "-")
}

func first(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
