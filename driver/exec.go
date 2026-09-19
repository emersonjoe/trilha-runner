package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/emersonjoe/trilha-spec/task"
)

// Exec starts a command-line agent — `claude -p -`, `codex exec -`, anything
// that reads a prompt on stdin and works in the current directory — inside
// the worktree. What the agent may do is the agent's own business here; the
// worktree and the checks are the fence.
type Exec struct{}

// ClaudeCode is the fixed, auditable Claude Code preset. The control plane
// chooses credentials, never argv.
type ClaudeCode struct{}

func (ClaudeCode) Name() string { return "claude-code" }

func (ClaudeCode) Execute(ctx context.Context, job Job) (Output, error) {
	job.Command = claudeCodeCommand(job.AI)
	if job.AI != nil && job.AI.Credential != "" {
		name := "ANTHROPIC_API_KEY"
		if strings.Contains(strings.ToLower(job.AI.Provider), "oauth") {
			name = "CLAUDE_CODE_OAUTH_TOKEN"
		}
		job.Env = append(job.Env, name+"="+job.AI.Credential)
	}
	output, err := (Exec{}).Execute(ctx, job)
	if output.Meta == nil {
		output.Meta = map[string]string{}
	}
	output.Meta["driver"] = "claude-code"
	output.Meta["command"] = job.Command
	return output, err
}

// IsClaudeCode answers whether a claimed item's provider selects this preset:
// `claude-code`, or `claude-code-oauth` when the credential is an OAuth token.
func IsClaudeCode(provider string) bool { return strings.HasPrefix(provider, "claude-code") }

// reModel is the shape of a model id the preset will put on argv. Anything
// else — spaces, a leading dash — is not a model and is dropped.
var reModel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// claudeCodeCommand is the preset's argv. It is fixed on purpose: the prompt
// comes on stdin, edits are accepted without a prompt because print mode has
// nobody to ask (checks run afterwards, by the runner, not by the agent), and
// the only value the control plane chooses is the model.
func claudeCodeCommand(ai *AIConfig) string {
	command := "claude -p - --permission-mode acceptEdits"
	if ai != nil && reModel.MatchString(ai.Model) {
		command += " --model " + ai.Model
	}
	return command
}

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
	runtimeDir := job.Dir
	if len(job.Prefix) > 0 {
		runtimeDir = "/workspace"
	}
	innerEnvironment := append([]string{"TRILHA_TASK=" + job.Task.ID, "TRILHA_WORKTREE=" + runtimeDir}, job.Env...)
	program, commandArgs := args[0], args[1:]
	if len(job.Prefix) > 0 {
		program = job.Prefix[0]
		commandArgs = append(append(append([]string(nil), job.Prefix[1:]...), innerEnvironment...), args...)
	}
	cmd := exec.CommandContext(ctx, program, commandArgs...)
	cmd.Dir = job.Dir
	cmd.Stdin = strings.NewReader(job.Prompt)
	cmd.Env = execEnvironment()
	if len(job.Prefix) == 0 {
		cmd.Env = append(cmd.Env, innerEnvironment...)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	secret := ""
	if job.AI != nil {
		secret = job.AI.Credential
	}
	o := Output{Text: tail(redactSecrets(out.String(), secret)), Meta: map[string]string{"driver": "exec", "command": command}}
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

func execEnvironment() []string {
	allowed := []string{"PATH", "HOME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "CGO_ENABLED", "GOCACHE", "GOMODCACHE", "GOPATH", "GOPROXY", "GOSUMDB", "GOTOOLCHAIN", "SSL_CERT_FILE", "SSL_CERT_DIR"}
	environment := make([]string, 0, len(allowed))
	for _, name := range allowed {
		if value, ok := os.LookupEnv(name); ok {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}
