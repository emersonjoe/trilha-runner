// Package driver is how the runner starts an agent. A driver takes a Job —
// the task, its manifest, the context pack as a prompt and the worktree to
// work in — and answers what the agent said. The runner does not care which
// model or which CLI is behind it; the manifest's `driver` field picks one.
//
//	exec  runs the manifest's command with the prompt on stdin (claude, codex, aider…)
//	ai    runs an agent loop on a chat-completion model, with file tools scoped to the worktree
//	echo  writes the prompt's first line to a file; the deterministic driver tests use
package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/emersonjoe/trilha-spec/agent"
	"github.com/emersonjoe/trilha-spec/task"
)

// Job is one execution.
type Job struct {
	Task  *task.Task
	Agent *agent.Agent
	// Prompt is the context pack rendered as Markdown.
	Prompt string
	// Dir is the worktree; every file the agent touches lives under it.
	Dir string
	// Command overrides the manifest's command (exec driver).
	Command string
	// Env is added to the agent's environment.
	Env []string
}

// Output is what an execution answered.
type Output struct {
	// Text is the agent's final message, or the tail of its output.
	Text string
	// Meta is what the driver knows and the evidence should keep: model,
	// turns, tokens, exit code.
	Meta map[string]string
}

// Driver runs a job.
type Driver interface {
	Name() string
	Execute(ctx context.Context, job Job) (Output, error)
}

// ErrUnknown is a driver name nobody registered.
var ErrUnknown = errors.New("driver: unknown driver")

var registry = map[string]func() Driver{
	"exec": func() Driver { return Exec{} },
	"ai":   func() Driver { return AI{} },
	"echo": func() Driver { return Echo{} },
}

// New answers a driver by name.
func New(name string) (Driver, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q (have %s)", ErrUnknown, name, strings.Join(Names(), ", "))
	}
	return f(), nil
}

// Names lists the drivers registered.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Register adds a driver; a runner built on this package may bring its own.
func Register(name string, f func() Driver) { registry[name] = f }

// MaxOutput caps what a driver keeps of an agent's output.
const MaxOutput = 256 << 10

func tail(s string) string {
	if len(s) > MaxOutput {
		return "[…]\n" + s[len(s)-MaxOutput:]
	}
	return s
}

// Echo is the driver with no model: it writes the first line of the prompt
// to TRILHA_RUN.md in the worktree and answers it. It exists so the whole
// pipeline — worktree, commit, verify, evidence — can be tested without a
// network or a key.
type Echo struct{}

// Name is "echo".
func (Echo) Name() string { return "echo" }

// Execute writes the file.
func (Echo) Execute(ctx context.Context, job Job) (Output, error) {
	first, _, _ := strings.Cut(strings.TrimSpace(job.Prompt), "\n")
	p := filepath.Join(job.Dir, "TRILHA_RUN.md")
	if err := os.WriteFile(p, []byte(first+"\n"), 0o644); err != nil {
		return Output{}, err
	}
	return Output{Text: "wrote TRILHA_RUN.md: " + first, Meta: map[string]string{"driver": "echo"}}, nil
}

// inside answers the absolute path of rel under root, or refuses a path
// that climbs out. Every file tool goes through it.
func inside(root, rel string) (string, error) {
	p := filepath.Join(root, filepath.FromSlash(rel))
	r, err := filepath.Rel(root, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the worktree", rel)
	}
	return p, nil
}
