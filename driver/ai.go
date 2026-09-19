package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/emersonjoe/trilha-spec/task"
	"github.com/emersonjoe/trilha/ai"
	"github.com/emersonjoe/trilha/ai/mcp"
)

// AI runs the agent loop of github.com/emersonjoe/trilha/ai — any provider
// that speaks the OpenAI chat protocol: OpenAI, Ollama, OpenRouter, vLLM —
// with tools that never leave the worktree. The manifest decides what the
// model may do: `read` is always on; `write` and `run` only when listed.
//
// Configuration comes from the environment the framework already reads:
// OPENAI_BASE_URL, OPENAI_API_KEY, TRILHA_AI_MODEL; the manifest's `model`
// wins when set.
type AI struct {
	// Client overrides the one built from the environment (tests).
	Client *ai.Client
	// MaxTurns caps model calls per job (default 40).
	MaxTurns int
}

// Name is "ai".
func (AI) Name() string { return "ai" }

// Execute runs the loop.
func (d AI) Execute(ctx context.Context, job Job) (Output, error) {
	cli := d.Client
	if cli == nil {
		cli = ai.NewFromEnv()
	}
	// A run that carries its own access uses it instead of the worker's
	// environment: one worker, one key per project.
	if a := job.Access; a != nil && d.Client == nil {
		if a.BaseURL != "" {
			cli.BaseURL = strings.TrimSuffix(a.BaseURL, "/")
		}
		if a.Credential != "" {
			cli.APIKey = a.Credential
		}
		if a.Model != "" {
			cli.Model = a.Model
		}
	}
	// Residency: the endpoint this run is about to call must be one the
	// project allows. The refusal happens before the first request, so no
	// data leaves the machine.
	if err := job.Access.Allows(cli.BaseURL); err != nil {
		return Output{Meta: policyMeta(job.Access)}, err
	}
	model := ""
	if job.Agent != nil && job.Agent.Model != "" {
		model = job.Agent.Model
	}
	may := func(tool string) bool { return job.Agent == nil || job.Agent.May(tool) }
	tools := []*ai.Tool{readFile(job.Dir), listFiles(job.Dir)}
	if may("write") {
		tools = append(tools, writeFile(job.Dir))
	}
	if may("run") {
		tools = append(tools, runCommand(job.Dir))
	}
	// The protocol itself, when its CLI is installed: the model can read the
	// task, its dependencies and the evidence so far through the same MCP
	// server a person's editor uses. Read-only, always.
	if path, err := exec.LookPath("trilha-spec"); err == nil {
		if c, err := mcp.Dial(ctx, mcp.Stdio(path, "mcp")); err == nil {
			defer c.Close()
			if extra, err := c.Tools(ctx); err == nil {
				tools = append(tools, extra...)
			}
		}
	}
	maxTurns := d.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 40
	}
	agent := &ai.Agent{
		Name:         "trilha-runner",
		Instructions: "You are a software engineering agent working inside a git worktree. Use the tools to read and change files and to run commands. Work only inside the worktree. When the task is done, answer with a short report of what changed and how it was verified.",
		Model:        model,
		Tools:        tools,
		MaxTurns:     maxTurns,
	}
	start := time.Now()
	res, err := ai.Run(ctx, cli, agent, job.Prompt)
	o := Output{Meta: map[string]string{"driver": "ai", "model": firstNonEmpty(model, cli.Model), "elapsed": time.Since(start).Round(time.Millisecond).String()}}
	if res != nil {
		o.Text = Redact(tail(res.Output), job.Access.Secrets())
		o.Meta["turns"] = strconv.Itoa(res.Turns)
		o.Meta["steps"] = strconv.Itoa(len(res.Steps))
		o.Meta["tokens"] = strconv.Itoa(res.Usage.TotalTokens)
	}
	if err != nil {
		return o, fmt.Errorf("ai: %w", err)
	}
	return o, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// maxRead caps a file the model reads.
const maxRead = 128 << 10

func readFile(root string) *ai.Tool {
	return ai.NewTool("read_file", "Read a file in the worktree. Path is relative to the worktree root.",
		ai.Schema(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		ai.Typed(func(ctx context.Context, in struct{ Path string }) (string, error) {
			p, err := inside(root, in.Path)
			if err != nil {
				return "", err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return "", err
			}
			if len(b) > maxRead {
				return string(b[:maxRead]) + "\n[truncated]", nil
			}
			return string(b), nil
		}))
}

func listFiles(root string) *ai.Tool {
	return ai.NewTool("list_files", "List files under a directory of the worktree (default: root), skipping .git and .trilha.",
		ai.Schema(`{"type":"object","properties":{"dir":{"type":"string"}}}`),
		ai.Typed(func(ctx context.Context, in struct{ Dir string }) (string, error) {
			if in.Dir == "" {
				in.Dir = "."
			}
			p, err := inside(root, in.Dir)
			if err != nil {
				return "", err
			}
			var b strings.Builder
			n := 0
			err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if d.IsDir() && (d.Name() == ".git" || d.Name() == ".trilha" || d.Name() == "node_modules") {
					return filepath.SkipDir
				}
				if !d.IsDir() {
					rel, _ := filepath.Rel(root, path)
					b.WriteString(filepath.ToSlash(rel) + "\n")
					if n++; n >= 2000 {
						return filepath.SkipAll
					}
				}
				return nil
			})
			return b.String(), err
		}))
}

func writeFile(root string) *ai.Tool {
	return ai.NewTool("write_file", "Write a whole file in the worktree, creating directories as needed.",
		ai.Schema(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`),
		ai.Typed(func(ctx context.Context, in struct{ Path, Content string }) (string, error) {
			p, err := inside(root, in.Path)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(p, []byte(in.Content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(in.Content), in.Path), nil
		}))
}

func runCommand(root string) *ai.Tool {
	return ai.NewTool("run", "Run a command in the worktree (program and arguments; no shell — use `sh -c \"...\"` for pipes). Answers exit code and output.",
		ai.Schema(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`),
		ai.Typed(func(ctx context.Context, in struct{ Command string }) (string, error) {
			e := task.RunCheck(ctx, "TASK-000", in.Command, root, "ai")
			out, _ := json.Marshal(map[string]any{"exit_code": e.ExitCode, "output": e.Output, "note": e.Note})
			return string(out), nil
		}))
}
