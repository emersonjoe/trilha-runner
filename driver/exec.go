package driver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/emersonjoe/trilha-runner/internal/agentlink"
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
	args := claudeCodeArgs(job.AI)
	// Every session has an id the runner knows, so a question it asks can be
	// answered in the same conversation: a fresh one gets a new id, a
	// continuation resumes the id it was handed (only if it is a UUID — it
	// came over the wire and lands on argv).
	sessionID := job.Resume
	if ValidSessionID(sessionID) {
		args = append(args, "--resume", sessionID)
	} else {
		sessionID = NewSessionID()
		args = append(args, "--session-id", sessionID)
	}
	// Hooks tell the runner what the agent is doing. Their settings file
	// lives in the run's directory, beside the worktree and never inside it.
	if job.RunDir != "" && job.Self != "" && len(job.Prefix) == 0 {
		if path, err := writeClaudeSettings(job.RunDir, job.Self); err == nil {
			args = append(args, "--settings", path)
		}
	}
	// Inside a sandbox the credential is already in the container, put there
	// by Prepare; adding it here would also put it on the host's argv.
	if len(job.Prefix) == 0 {
		job.Env = append(job.Env, CredentialEnv(job.AI)...)
	}
	output, raw, err := runArgsRaw(ctx, job, args, strings.Join(args, " "))
	output.Meta["driver"] = "claude-code"
	output.Meta["session_id"] = sessionID
	secret := ""
	if job.AI != nil {
		secret = job.AI.Credential
	}
	applyClaudeResult(&output, raw, secret)
	return output, err
}

// claudeResult is the part of Claude Code's `--output-format json` result the
// runner reports: the agent's final message and what the run used.
type claudeResult struct {
	Type         string  `json:"type"`
	Result       string  `json:"result"`
	NumTurns     int     `json:"num_turns"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
	ModelUsage map[string]struct {
		OutputTokens int `json:"outputTokens"`
	} `json:"modelUsage"`
}

// applyClaudeResult finds the result line Claude Code prints last and puts
// its final message in Text and its usage in Meta: the model it resolved (the
// one that wrote the most), tokens with cache writes and reads counted as
// input, and the cost in dollars. Output with no result line — an older
// Claude Code, a crash — keeps its text and claims nothing.
func applyClaudeResult(output *Output, raw, secret string) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var result claudeResult
		if json.Unmarshal([]byte(line), &result) != nil || result.Type != "result" {
			continue
		}
		output.Text = tail(redactSecrets(result.Result, secret))
		in := result.Usage.InputTokens + result.Usage.CacheCreationInputTokens + result.Usage.CacheReadInputTokens
		output.Meta["provider"] = "anthropic"
		output.Meta["input_tokens"] = strconv.Itoa(in)
		output.Meta["output_tokens"] = strconv.Itoa(result.Usage.OutputTokens)
		output.Meta["tokens"] = strconv.Itoa(in + result.Usage.OutputTokens)
		output.Meta["turns"] = strconv.Itoa(result.NumTurns)
		if result.TotalCostUSD > 0 {
			output.Meta["cost_usd"] = strconv.FormatFloat(result.TotalCostUSD, 'f', -1, 64)
		}
		model, most := "", -1
		for name, usage := range result.ModelUsage {
			if usage.OutputTokens > most || (usage.OutputTokens == most && name < model) {
				model, most = name, usage.OutputTokens
			}
		}
		if model != "" {
			output.Meta["model"] = model
		}
		return
	}
}

// ClaudeSettingsFile is the name of the settings file claude-code gets.
const ClaudeSettingsFile = "claude-settings.json"

// writeClaudeSettings writes the hooks that call `trilha-runner hook claude`
// for every event the runner follows, and answers the file's path. Print
// mode has nobody to approve a shell command, so the file also allows the
// agent's own runner commands — and only those — through the Bash tool.
func writeClaudeSettings(dir, self string) (string, error) {
	command := shellQuote(self) + " hook claude"
	hooks := map[string]any{}
	for _, event := range agentlink.ClaudeHooks {
		hooks[event] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 5}}}}
	}
	var allow []string
	for _, sub := range []string{"ask", "note", "overview", "attention", "finding", "verdict", "reply", "artifact"} {
		allow = append(allow, "Bash(trilha-runner "+sub+":*)")
	}
	data, err := json.MarshalIndent(map[string]any{"hooks": hooks, "permissions": map[string]any{"allow": allow}}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, ClaudeSettingsFile)
	return path, os.WriteFile(path, append(data, '\n'), 0o600)
}

// shellQuote quotes a path for the shell Claude Code runs hook commands in.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

var reSessionID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidSessionID answers whether id is a UUID, the only shape of session id
// the runner puts on an agent's argv.
func ValidSessionID(id string) bool { return reSessionID.MatchString(id) }

// NewSessionID answers a random (version 4) UUID.
func NewSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// IsClaudeCode answers whether a claimed item's provider selects this preset:
// `claude-code`, or `claude-code-oauth` when the credential is an OAuth token.
func IsClaudeCode(provider string) bool { return strings.HasPrefix(provider, "claude-code") }

// reModel is the shape of a model id the preset will put on argv. Anything
// else — spaces, a leading dash — is not a model and is dropped.
var reModel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// claudeCodeArgs is the preset's argv. It is fixed on purpose: the prompt
// comes on stdin, edits are accepted without a prompt because print mode has
// nobody to ask (checks run afterwards, by the runner, not by the agent), and
// the only value the control plane chooses is the model. The runner adds the
// session (--session-id or --resume) and its hooks (--settings).
func claudeCodeArgs(ai *AIConfig) []string {
	args := []string{"claude", "-p", "-", "--permission-mode", "acceptEdits"}
	if ai != nil && reModel.MatchString(ai.Model) {
		args = append(args, "--model", ai.Model)
	}
	// JSON, so the run reports the model, tokens and cost Claude Code used.
	return append(args, "--output-format", "json")
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
	return runArgs(ctx, job, args, command)
}

// runArgs runs a program and its arguments as the job's agent: the prompt on
// stdin, the worktree as working directory, the allow-listed environment.
func runArgs(ctx context.Context, job Job, args []string, command string) (Output, error) {
	output, _, err := runArgsRaw(ctx, job, args, command)
	return output, err
}

// runArgsRaw is runArgs that also answers the whole output, untrimmed, for a
// driver that reads structure out of it.
func runArgsRaw(ctx context.Context, job Job, args []string, command string) (Output, string, error) {
	runtimeDir := job.Dir
	if len(job.Prefix) > 0 {
		runtimeDir = "/workspace"
	}
	innerEnvironment := append([]string{"TRILHA_TASK=" + job.Task.ID, "TRILHA_WORKTREE=" + runtimeDir}, job.Env...)
	program, commandArgs := args[0], args[1:]
	if len(job.Prefix) > 0 {
		// The prefix enters a sandbox that already holds this run's
		// environment. Passing it again as arguments would publish every
		// value — the project's credential among them — to `ps` on the host.
		program = job.Prefix[0]
		commandArgs = append(append([]string(nil), job.Prefix[1:]...), args...)
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
	raw := out.String()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			o.Meta["exit_code"] = strconv.Itoa(ee.ExitCode())
			return o, raw, fmt.Errorf("exec: %s exited %d", args[0], ee.ExitCode())
		}
		return o, raw, fmt.Errorf("exec: %w", err)
	}
	o.Meta["exit_code"] = "0"
	return o, raw, nil
}

func execEnvironment() []string {
	allowed := []string{"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "CGO_ENABLED", "GOCACHE", "GOMODCACHE", "GOPATH", "GOPROXY", "GOSUMDB", "GOTOOLCHAIN", "SSL_CERT_FILE", "SSL_CERT_DIR"}
	environment := make([]string, 0, len(allowed))
	for _, name := range allowed {
		if value, ok := os.LookupEnv(name); ok {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}
