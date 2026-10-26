package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emersonjoe/trilha-runner/internal/agentlink"
)

// The commands an agent runs during a run: `hook` (called by the agent's own
// hooks), `ask`, `note`, `overview` and `attention`. Each sends one message
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
	}
	return agentlink.Message{}, fmt.Errorf("unknown agent command %q", cmd)
}
