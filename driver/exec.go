package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/emersonjoe/trilha-spec/task"
)

// Exec starts a command-line agent — `claude -p -`, `codex exec -`, anything
// that reads a prompt on stdin and works in the current directory — inside
// the worktree. What the agent may do is the agent's own business here; the
// worktree and the checks are the fence.
type Exec struct{}

// Name is "exec".
func (Exec) Name() string { return "exec" }

// Execute runs the job's command, or the manifest's, with the prompt on
// stdin and the worktree as working directory. No shell: the command is a
// program and its arguments.
func (Exec) Execute(ctx context.Context, job Job) (Output, error) {
	command := job.Command
	if command == "" && job.Agent != nil {
		command = job.Agent.Command
	}
	args := task.SplitCommand(command)
	if len(args) == 0 {
		return Output{}, errors.New("exec: no command: set `command:` in the agent manifest or pass --cmd")
	}
	// The run's model access, when the control plane sent one, reaches the
	// agent only here — as process environment — and a base URL outside the
	// project's allowed hosts is refused before the agent starts.
	access, err := job.Access.Env()
	if err != nil {
		return Output{Meta: policyMeta(job.Access)}, err
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = job.Dir
	cmd.Stdin = strings.NewReader(job.Prompt)
	cmd.Env = append(os.Environ(), append([]string{"TRILHA_TASK=" + job.Task.ID, "TRILHA_WORKTREE=" + job.Dir}, append(job.Env, access...)...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	o := Output{Text: Redact(tail(out.String()), job.Access.Secrets()), Meta: map[string]string{"driver": "exec", "command": command}}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			o.Meta["exit_code"] = strconv.Itoa(ee.ExitCode())
			return o, fmt.Errorf("exec: %s exited %d", args[0], ee.ExitCode())
		}
		return o, fmt.Errorf("exec: %w", err)
	}
	o.Meta["exit_code"] = "0"
	return o, nil
}
