// Package sandbox is where an execution is confined. The local runner has
// one sandbox that needs nothing — the worktree, plus the manifest's tool
// list enforced by the ai driver — and one that needs a container runtime:
// Docker, for a project whose checks need services.
//
// A Sandbox prepares an environment for a job and tears it down after. What
// it hands back says both where the work is on the host and how to start a
// command inside it, so the driver and the checks run in the same place.
package sandbox

import (
	"context"
	"strings"
)

// Request is what the runner needs run.
type Request struct {
	// Task is the task id, used to name what the sandbox creates.
	Task string
	// Dir is the worktree on the host: the only thing the sandbox makes
	// writable, and the only thing that survives it.
	Dir string
	// Env is KEY=VALUE the execution needs — TRILHA_TASK, the run's model
	// access. A sandbox must keep these off any command line.
	Env []string
	// Spec is the sandbox the agent manifest declared, if any.
	Spec *Spec
}

// Env is what a prepared sandbox hands the runner: the directory to work in
// as the host sees it, extra environment variables, and — when the work
// happens somewhere else — the prefix that starts a command there.
type Env struct {
	Dir string
	Env []string
	// Prefix starts a command inside the sandbox, e.g.
	// `docker exec --workdir /workspace trilha-task-001`. Empty means the
	// command runs on the host, in Dir.
	Prefix []string
	// WorkDir is the worktree as the sandbox sees it; Dir when there is no
	// sandbox.
	WorkDir string
}

// Wrap answers the command line that runs `command` inside the sandbox. The
// protocol's commands are program-and-arguments with quotes grouping, and so
// is the prefix, so wrapping is prepending: a check reads in the evidence as
// exactly what ran.
func (e Env) Wrap(command string) string {
	if len(e.Prefix) == 0 {
		return command
	}
	return strings.Join(e.Prefix, " ") + " " + command
}

// Inside answers whether commands run somewhere other than the host.
func (e Env) Inside() bool { return len(e.Prefix) > 0 }

// Sandbox prepares and releases an execution environment. Release is
// answered even when Prepare fails partway, and it is safe to call twice:
// whatever was created must be removed.
type Sandbox interface {
	Name() string
	Prepare(ctx context.Context, req Request) (Env, func() error, error)
}

// None runs in place: the worktree is the sandbox.
type None struct{}

// Name is "none".
func (None) Name() string { return "none" }

// Prepare answers the directory as is.
func (None) Prepare(ctx context.Context, req Request) (Env, func() error, error) {
	return Env{Dir: req.Dir, WorkDir: req.Dir, Env: req.Env}, func() error { return nil }, nil
}
