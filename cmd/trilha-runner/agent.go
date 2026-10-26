package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/emersonjoe/trilha-runner/internal/agentlink"
	"github.com/emersonjoe/trilha-spec/execution"
)

// The commands an agent runs during a run: `hook` (called by the agent's own
// hooks), `ask`, `note`, `overview`, `attention` and, for review, hunt and
// repair runs, `finding`, `verdict`, `reply` and `artifact`. Each sends one message
// to the runner over TRILHA_RUN_SOCKET and always exits 0, within a second:
// an agent is never failed or held up because the runner is not listening.

// selfPath is this binary, resolved once in main; what the agent's PATH and
// claude-code's hooks point at. Tests that run the worker in-process set it
// to the binary they built.
var selfPath string

func resolveSelf() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// agentBudget is how long any agent command may take, start to finish.
const agentBudget = 600 * time.Millisecond

func cmdAgent(ctx context.Context, cmd string, args []string, stdin io.Reader, out, errOut io.Writer) {
	ctx, cancel := context.WithTimeout(ctx, agentBudget)
	defer cancel()
	message, err := agentMessage(ctx, cmd, args, stdin)
	if err == nil {
		deadline, _ := ctx.Deadline()
		err = agentlink.Send(os.Getenv(agentlink.EnvSocket), message, time.Until(deadline))
	}
	if err != nil {
		if cmd != "hook" {
			fmt.Fprintf(errOut, "trilha-runner %s: %v\n", cmd, err)
		}
		return
	}
	switch cmd {
	case "ask":
		fmt.Fprintln(out, "Question recorded. Stop now and end your turn: a person will answer, and the answer comes back in a new session.")
	case "note", "overview":
		fmt.Fprintln(out, cmd+" recorded")
	case "attention":
		fmt.Fprintln(out, "attention sent")
	case "finding", "verdict", "reply", "artifact":
		fmt.Fprintln(out, cmd+" sent to the runner")
	}
}

func agentMessage(ctx context.Context, cmd string, args []string, stdin io.Reader) (agentlink.Message, error) {
	text := strings.TrimSpace(strings.Join(args, " "))
	switch cmd {
	case "hook":
		if len(args) != 1 || (args[0] != "claude" && args[0] != "claude-code") {
			return agentlink.Message{}, errors.New("usage: trilha-runner hook claude")
		}
		payload, err := agentlink.ReadHook(ctx, func() ([]byte, error) { return io.ReadAll(io.LimitReader(stdin, agentlink.MaxMessage)) })
		if err != nil {
			return agentlink.Message{}, err
		}
		message, ok := agentlink.FromClaudeHook(payload)
		if !ok {
			return agentlink.Message{}, errors.New("not a hook event the runner follows")
		}
		return message, nil
	case "ask":
		if text == "" {
			return agentlink.Message{}, errors.New(`usage: trilha-runner ask "<one objective question>"`)
		}
		return agentlink.Message{Type: agentlink.TypeAsk, Text: text}, nil
	case "note", "overview":
		if text == "" {
			return agentlink.Message{}, fmt.Errorf(`usage: trilha-runner %s "<text>"`, cmd)
		}
		kind := agentlink.TypeNote
		if cmd == "overview" {
			kind = agentlink.TypeOverview
		}
		return agentlink.Message{Type: kind, Text: text}, nil
	case "attention":
		if len(args) == 1 && args[0] == "--clear" {
			return agentlink.Message{Type: agentlink.TypeAttention, Clear: true}, nil
		}
		if text == "" {
			return agentlink.Message{}, errors.New(`usage: trilha-runner attention "<reason>" | --clear`)
		}
		return agentlink.Message{Type: agentlink.TypeAttention, Text: text}, nil
	case "finding":
		return findingMessage(args)
	case "verdict":
		if len(args) != 1 || (args[0] != "pass" && args[0] != "changes") {
			return agentlink.Message{}, errors.New("usage: trilha-runner verdict pass|changes")
		}
		return agentlink.Message{Type: agentlink.TypeVerdict, Text: args[0]}, nil
	case "reply":
		if len(args) < 2 || strings.TrimSpace(args[0]) == "" || strings.TrimSpace(strings.Join(args[1:], " ")) == "" {
			return agentlink.Message{}, errors.New(`usage: trilha-runner reply <comment-id> "<what you did, or why not>"`)
		}
		return agentlink.Message{Type: agentlink.TypeReply, ID: strings.TrimSpace(args[0]), Text: strings.TrimSpace(strings.Join(args[1:], " "))}, nil
	case "artifact":
		return artifactMessage(args)
	}
	return agentlink.Message{}, fmt.Errorf("unknown agent command %q", cmd)
}

// findingMessage reads `finding --severity S --path P --lines A-B --title T
// [--why W] [--repro R]` and refuses, before anything is sent, a finding the
// control plane could not store.
func findingMessage(args []string) (agentlink.Message, error) {
	const usage = `usage: trilha-runner finding --severity critical|high|medium --path <file> --lines <start>[-<end>] --title "<title>" [--why "<why it breaks>"] [--repro "<how to reproduce>"]`
	fs := flags("finding")
	severity := fs.String("severity", "", "critical, high or medium")
	path := fs.String("path", "", "file relative to the repository")
	lines := fs.String("lines", "", "start-end")
	title := fs.String("title", "", "short title")
	why := fs.String("why", "", "why it breaks")
	repro := fs.String("repro", "", "how to reproduce")
	if rest, err := parse(fs, args); err != nil || len(rest) > 0 {
		return agentlink.Message{}, errors.New(usage)
	}
	start, end, err := lineRange(*lines)
	if err != nil {
		return agentlink.Message{}, fmt.Errorf("%v\n%s", err, usage)
	}
	finding := execution.Finding{Severity: strings.ToLower(strings.TrimSpace(*severity)), Path: strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(*path)), "./"), LineStart: start, LineEnd: end,
		Title: strings.TrimSpace(*title), Why: strings.TrimSpace(*why), Repro: strings.TrimSpace(*repro)}
	if err := finding.Validate(); err != nil {
		return agentlink.Message{}, fmt.Errorf("%v\n%s", err, usage)
	}
	return agentlink.Message{Type: agentlink.TypeFinding, Finding: &finding}, nil
}

// lineRange reads "12" or "12-18".
func lineRange(value string) (int, int, error) {
	first, last, isRange := strings.Cut(strings.TrimSpace(value), "-")
	start, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil {
		return 0, 0, errors.New("--lines must be <start> or <start>-<end>")
	}
	end := start
	if isRange {
		if end, err = strconv.Atoi(strings.TrimSpace(last)); err != nil {
			return 0, 0, errors.New("--lines must be <start> or <start>-<end>")
		}
	}
	return start, end, nil
}

// artifactMessage reads `artifact <file> [--name N]`. The runner makes the
// decision — type, size, secrets, how many — and copies the file; this only
// refuses what can never be accepted and sends the absolute path.
func artifactMessage(args []string) (agentlink.Message, error) {
	const usage = "usage: trilha-runner artifact <file .html|.md|.png|.jpg> [--name <name>]"
	fs := flags("artifact")
	name := fs.String("name", "", "the name people see")
	rest, err := parse(fs, args)
	if err != nil || len(rest) != 1 {
		return agentlink.Message{}, errors.New(usage)
	}
	path, err := filepath.Abs(rest[0])
	if err != nil {
		return agentlink.Message{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return agentlink.Message{}, err
	}
	if !info.Mode().IsRegular() {
		return agentlink.Message{}, errors.New("artifact: not a regular file")
	}
	if info.Size() > execution.MaxArtifactBytes {
		return agentlink.Message{}, fmt.Errorf("artifact: %d bytes is more than the %d allowed", info.Size(), execution.MaxArtifactBytes)
	}
	label := strings.TrimSpace(*name)
	if label == "" {
		label = filepath.Base(path)
	}
	return agentlink.Message{Type: agentlink.TypeArtifact, Path: path, Name: label}, nil
}
