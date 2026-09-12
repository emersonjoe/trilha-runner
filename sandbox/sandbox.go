// Package sandbox is where an execution is confined. The local runner has
// one real sandbox — the worktree, plus the manifest's tool list enforced
// by the ai driver — and this package is the seam trilha-cloud fills with
// containers and VMs: a Sandbox prepares an environment for a job and tears
// it down after.
package sandbox

import "context"

// Env is what a prepared sandbox hands the driver: the directory to work in
// and extra environment variables.
type Env struct {
	Dir string
	Env []string
}

// Sandbox prepares and releases an execution environment.
type Sandbox interface {
	Name() string
	Prepare(ctx context.Context, dir string) (Env, func() error, error)
}

// None runs in place: the worktree is the sandbox.
type None struct{}

// Name is "none".
func (None) Name() string { return "none" }

// Prepare answers the directory as is.
func (None) Prepare(ctx context.Context, dir string) (Env, func() error, error) {
	return Env{Dir: dir}, func() error { return nil }, nil
}
