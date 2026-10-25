package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/emersonjoe/trilha-spec/task"
)

// preflightTimeout bounds one tool lookup inside a sandbox.
const preflightTimeout = 30 * time.Second

// preflight resolves the program of every check — the task's `checks:` and
// the project's `verify:` — in the environment the checks will run in, before
// the first attempt. A tool that is not there would fail every attempt the
// same way, after the agent has been paid for each of them: acervo run-000007
// spent three attempts of a model because `python` was not installed on the
// worker (#13, #14). It answers the first missing program and the check that
// needs it, or "" when every program resolves.
//
// On the host the lookup is exec.LookPath, with the runner's own PATH — the
// one the checks inherit. Inside a sandbox the host's PATH says nothing, so
// the lookup goes through the sandbox prefix as `sh -c 'command -v "$1"' sh
// PROGRAM`: the program's name is an argument, never interpolated, and the
// run's environment is already in the container — nothing of it touches the
// host's argv.
func (l *attemptLoop) preflight() (tool, command string, err error) {
	for _, check := range l.checks {
		args := task.SplitCommand(check)
		if len(args) == 0 {
			continue
		}
		if lookErr := lookPath(l.ctx, l.dir, l.prefix, args[0]); lookErr != nil {
			return args[0], check, lookErr
		}
	}
	return "", "", nil
}

func lookPath(ctx context.Context, dir string, prefix []string, program string) error {
	if len(prefix) > 0 {
		ctx, cancel := context.WithTimeout(ctx, preflightTimeout)
		defer cancel()
		args := append(append([]string(nil), prefix[1:]...), "sh", "-c", `command -v "$1"`, "sh", program)
		output, err := exec.CommandContext(ctx, prefix[0], args...).CombinedOutput()
		if err != nil {
			text := strings.TrimSpace(string(output))
			if text == "" {
				text = err.Error()
			}
			return fmt.Errorf("%s is not installed in the sandbox: %s", program, text)
		}
		return nil
	}
	if strings.ContainsRune(program, os.PathSeparator) {
		// A path: relative to the worktree, like the check itself runs.
		path := program
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("%s is not in the worktree: %w", program, err)
		}
		if info.IsDir() || info.Mode()&0o111 == 0 {
			return errors.New(program + " is not an executable file")
		}
		return nil
	}
	if _, err := exec.LookPath(program); err != nil {
		return fmt.Errorf("%s is not installed on this worker: %w", program, err)
	}
	return nil
}
