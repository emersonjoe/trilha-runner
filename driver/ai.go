package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/emersonjoe/trilha-runner/internal/agentlink"
	"github.com/emersonjoe/trilha-spec/execution"
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
	if job.AI != nil {
		if err := validateAllowedHost(job.AI.BaseURL, job.AI.AllowedHosts); err != nil {
			return Output{Meta: map[string]string{"driver": "ai", "stage": "policy"}}, err
		}
		cli = &ai.Client{BaseURL: strings.TrimSuffix(job.AI.BaseURL, "/"), APIKey: job.AI.Credential, Model: job.AI.Model}
	} else if cli == nil {
		cli = ai.NewFromEnv()
	}
	model := ""
	if job.AI != nil && job.AI.Model != "" {
		model = job.AI.Model
	} else if job.Agent != nil && job.Agent.Model != "" {
		model = job.Agent.Model
	}
	may := func(tool string) bool { return job.Agent == nil || job.Agent.May(tool) }
	tools := []*ai.Tool{readFile(job.Dir), listFiles(job.Dir)}
	if may("write") && !job.ReadOnly {
		tools = append(tools, writeFile(job.Dir))
	}
	if may("run") {
		tools = append(tools, runCommand(job.Dir, job.AllowedChecks, job.Prefix))
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
	// The agent's state comes from the loop itself: a tool call starts and
	// ends, the loop stops. A question, a note and an overview are tools too.
	loopCtx, stopLoop := context.WithCancel(ctx)
	defer stopLoop()
	var asked atomic.Bool
	var afterAsk atomic.Int32
	if job.Signal != nil {
		if !job.ReadOnly {
			tools = append(tools, askTool(job, &asked))
		}
		tools = append(tools, memoryTool(job, "note", agentlink.TypeNote, "Leave a note the next run of this task should know (one fact, a sentence or two)."), memoryTool(job, "overview", agentlink.TypeOverview, "Set the task's overview: one or two sentences on what is being done and where it stands."))
		tools = append(tools, reportTools(job)...)
		for _, tool := range tools {
			tool.Func = signalled(job, tool.Name, tool.Func, &asked, &afterAsk, stopLoop)
		}
	}
	maxTurns := d.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 40
	}
	agent := &ai.Agent{
		Name:         "trilha-runner",
		Instructions: instructions(job),
		Model:        model,
		Tools:        tools,
		MaxTurns:     maxTurns,
	}
	start := time.Now()
	prompt := job.Prompt
	if strings.TrimSpace(job.Checkpoint) != "" {
		prompt += "\n\n## Recovery checkpoint\n\nContinue from the previous interrupted attempt. Reuse the work already present in the worktree and do not restart discovery.\n\n```json\n" + job.Checkpoint + "\n```\n"
	}
	res, err := ai.Run(loopCtx, cli, agent, prompt)
	job.signal(agentlink.Message{Type: agentlink.TypeEvent, Event: execution.EventStop})
	if err != nil && asked.Load() {
		// The agent asked and was told to stop; whatever ended the loop after
		// that (a refused tool, the cancel) is the stop, not a failure.
		err = nil
	}
	o := Output{Meta: map[string]string{"driver": "ai", "model": firstNonEmpty(model, cli.Model), "elapsed": time.Since(start).Round(time.Millisecond).String()}}
	if res != nil {
		secret := ""
		if job.AI != nil {
			secret = job.AI.Credential
		}
		o.Text = tail(redactSecrets(res.Output, secret))
		o.Meta["turns"] = strconv.Itoa(res.Turns)
		o.Meta["steps"] = strconv.Itoa(len(res.Steps))
		o.Meta["tokens"] = strconv.Itoa(res.Usage.TotalTokens)
		o.Meta["input_tokens"] = strconv.Itoa(res.Usage.PromptTokens)
		o.Meta["output_tokens"] = strconv.Itoa(res.Usage.CompletionTokens)
		o.Recovery = encodeRecovery(res)
		if job.AI != nil && job.AI.Credential != "" {
			o.Recovery = json.RawMessage(redactSecrets(string(o.Recovery), job.AI.Credential))
		}
	}
	if err != nil {
		secret := ""
		if job.AI != nil {
			secret = job.AI.Credential
		}
		return o, fmt.Errorf("ai: %s", redactSecrets(err.Error(), secret))
	}
	return o, nil
}

// instructions is the agent's standing instruction: implement, or — for a
// read-only run — read and report without changing anything.
func instructions(job Job) string {
	if job.ReadOnly {
		return "You are a software engineering agent reviewing a git worktree. Use the tools to read files and run allow-listed checks. You must not change any file. Report every problem you can prove with the finding tool, with its path relative to the repository and its line range, and end with a short summary."
	}
	return "You are a software engineering agent working inside a git worktree. Use the tools to read and change files and to run commands. Work only inside the worktree. When the task is done, answer with a short report of what changed and how it was verified."
}

// reportTools are the tools a review, hunt or repair run reports through:
// finding (review and hunt), verdict (review), reply (when the run answers
// review comments) and artifact (always). The runner validates and redacts
// what they carry, as it does for the same commands on the socket.
func reportTools(job Job) []*ai.Tool {
	var tools []*ai.Tool
	if job.Kind == execution.KindReview || job.Kind == execution.KindHunt {
		tools = append(tools, ai.NewTool("finding", "Report one problem you can prove: severity critical, high or medium; path relative to the repository; the line range; a short title; why it breaks; how to reproduce it. Never put code or secrets in a finding.",
			ai.Schema(`{"type":"object","properties":{"severity":{"type":"string","enum":["critical","high","medium"]},"path":{"type":"string"},"line_start":{"type":"integer"},"line_end":{"type":"integer"},"title":{"type":"string"},"why":{"type":"string"},"repro":{"type":"string"}},"required":["severity","path","line_start","line_end","title"]}`),
			func(ctx context.Context, args json.RawMessage) (string, error) {
				var finding execution.Finding
				if err := json.Unmarshal(args, &finding); err != nil {
					return "", fmt.Errorf("finding: %w", err)
				}
				finding.Path = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(finding.Path)), "./")
				if finding.LineEnd == 0 {
					finding.LineEnd = finding.LineStart
				}
				if err := finding.Validate(); err != nil {
					return "", err
				}
				job.signal(agentlink.Message{Type: agentlink.TypeFinding, Finding: &finding})
				return "finding recorded", nil
			}))
	}
	if job.Kind == execution.KindReview {
		tools = append(tools, ai.NewTool("verdict", "Give the review's verdict: pass when nothing needs to change, changes when at least one finding must be fixed.",
			ai.Schema(`{"type":"object","properties":{"verdict":{"type":"string","enum":["pass","changes"]}},"required":["verdict"]}`),
			ai.Typed(func(ctx context.Context, in struct{ Verdict string }) (string, error) {
				if in.Verdict != "pass" && in.Verdict != "changes" {
					return "", fmt.Errorf("verdict: pass or changes")
				}
				job.signal(agentlink.Message{Type: agentlink.TypeVerdict, Text: in.Verdict})
				return "verdict recorded", nil
			})))
	}
	if len(job.ReplyTo) > 0 {
		tools = append(tools, ai.NewTool("reply", "Answer one review comment by its id: what you did about it, or why not.",
			ai.Schema(`{"type":"object","properties":{"id":{"type":"string"},"reply":{"type":"string"}},"required":["id","reply"]}`),
			ai.Typed(func(ctx context.Context, in struct{ ID, Reply string }) (string, error) {
				if !slices.Contains(job.ReplyTo, in.ID) || strings.TrimSpace(in.Reply) == "" {
					return "", fmt.Errorf("reply: the id must be one of %s and the reply not empty", strings.Join(job.ReplyTo, ", "))
				}
				job.signal(agentlink.Message{Type: agentlink.TypeReply, ID: in.ID, Text: in.Reply})
				return "reply recorded", nil
			})))
	}
	tools = append(tools, ai.NewTool("artifact", "Publish a report for people to read: a name ending in .md or .html and its whole text. Never put a secret in it: a report with a secret is refused.",
		ai.Schema(`{"type":"object","properties":{"name":{"type":"string"},"content":{"type":"string"}},"required":["name","content"]}`),
		ai.Typed(func(ctx context.Context, in struct{ Name, Content string }) (string, error) {
			extension := strings.ToLower(filepath.Ext(in.Name))
			if extension != ".md" && extension != ".html" {
				return "", fmt.Errorf("artifact: the name must end in .md or .html")
			}
			job.signal(agentlink.Message{Type: agentlink.TypeArtifact, Name: in.Name, Content: in.Content})
			return "artifact sent to the runner", nil
		})))
	return tools
}

// askTool records the question for a person and tells the model to stop.
func askTool(job Job, asked *atomic.Bool) *ai.Tool {
	return ai.NewTool("ask", "Ask the person who owns this task ONE objective question when information you need is missing and cannot be found in the worktree or in the prompt (an answer you already received is in the prompt: never ask it again). After asking, stop: call no more tools and end your turn.",
		ai.Schema(`{"type":"object","properties":{"question":{"type":"string"}},"required":["question"]}`),
		ai.Typed(func(ctx context.Context, in struct{ Question string }) (string, error) {
			if strings.TrimSpace(in.Question) == "" {
				return "", fmt.Errorf("ask: the question is empty")
			}
			job.signal(agentlink.Message{Type: agentlink.TypeAsk, Text: in.Question})
			asked.Store(true)
			return "Your question was recorded and will reach a person; the answer comes in a new session. Stop now: call no more tools and end your turn with one line saying what you did so far.", nil
		}))
}

func memoryTool(job Job, name, kind, description string) *ai.Tool {
	return ai.NewTool(name, description,
		ai.Schema(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		ai.Typed(func(ctx context.Context, in struct{ Text string }) (string, error) {
			if strings.TrimSpace(in.Text) == "" {
				return "", fmt.Errorf("%s: the text is empty", name)
			}
			job.signal(agentlink.Message{Type: kind, Text: in.Text})
			return name + " recorded", nil
		}))
}

// signalled reports a tool's start and end, and refuses work after the
// agent asked a question: the third refused call ends the loop.
func signalled(job Job, name string, fn ai.ToolFunc, asked *atomic.Bool, afterAsk *atomic.Int32, stop func()) ai.ToolFunc {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		if name == "ask" {
			// Asking is its own state; a tool end after it would read as working.
			return fn(ctx, args)
		}
		if asked.Load() {
			if afterAsk.Add(1) >= 3 {
				stop()
			}
			return "", fmt.Errorf("you already asked a question: stop now and end your turn")
		}
		job.signal(agentlink.Message{Type: agentlink.TypeEvent, Event: execution.EventToolStart})
		defer job.signal(agentlink.Message{Type: agentlink.TypeEvent, Event: execution.EventToolEnd})
		return fn(ctx, args)
	}
}

func validateAllowedHost(baseURL string, allowed []string) error {
	if len(allowed) == 0 {
		return nil
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Hostname() == "" {
		return &PolicyError{Reason: "AI base URL is invalid"}
	}
	host := strings.ToLower(parsed.Hostname())
	for _, candidate := range allowed {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == host || (strings.HasPrefix(candidate, "*.") && strings.HasSuffix(host, candidate[1:])) {
			return nil
		}
	}
	return &PolicyError{Reason: fmt.Sprintf("AI host %q is not allowed", host)}
}

type recoveryStep struct {
	Agent     string `json:"agent,omitempty"`
	Tool      string `json:"tool"`
	CallID    string `json:"call_id,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
	Error     string `json:"error,omitempty"`
	HandoffTo string `json:"handoff_to,omitempty"`
}

func encodeRecovery(result *ai.Result) json.RawMessage {
	if result == nil {
		return nil
	}
	steps := make([]recoveryStep, 0, len(result.Steps))
	files := make([]string, 0)
	for _, step := range result.Steps {
		recovery := recoveryStep{Agent: step.Agent, Tool: step.Tool, CallID: step.CallID, Arguments: step.Arguments, Output: step.Output, HandoffTo: step.HandoffTo}
		if step.Err != nil {
			recovery.Error = step.Err.Error()
		}
		steps = append(steps, recovery)
		if step.Tool == "read_file" {
			var input struct {
				Path string `json:"path"`
			}
			if json.Unmarshal([]byte(step.Arguments), &input) == nil && input.Path != "" && !slices.Contains(files, input.Path) {
				files = append(files, input.Path)
			}
		}
	}
	encoded, _ := json.Marshal(struct {
		Turns         int            `json:"turns"`
		Messages      []ai.Message   `json:"messages,omitempty"`
		Steps         []recoveryStep `json:"tool_calls,omitempty"`
		FilesAnalyzed []string       `json:"files_analyzed,omitempty"`
	}{Turns: result.Turns, Messages: result.Messages, Steps: steps, FilesAnalyzed: files})
	return encoded
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

func runCommand(root string, allowed, prefix []string) *ai.Tool {
	return ai.NewTool("run_check", "Run one exact allow-listed verification command in the worktree. Network access and credentials are removed.",
		ai.Schema(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`),
		ai.Typed(func(ctx context.Context, in struct{ Command string }) (string, error) {
			if !allowedCommand(in.Command, allowed) {
				return "", fmt.Errorf("run_check: command is not allow-listed")
			}
			e := runSandboxedCheck(ctx, in.Command, root, prefix)
			out, _ := json.Marshal(map[string]any{"exit_code": e.ExitCode, "output": e.Output, "note": e.Note})
			return string(out), nil
		}))
}

func allowedCommand(command string, allowed []string) bool {
	want := strings.Join(task.SplitCommand(command), "\x00")
	if want == "" {
		return false
	}
	for _, candidate := range allowed {
		if strings.Join(task.SplitCommand(candidate), "\x00") == want {
			return true
		}
	}
	return false
}

// runSandboxedCheck runs a check for the model: on the host with a scrubbed
// environment, or through the sandbox prefix when the run has one, so a check
// of foreign code never runs on the host.
func runSandboxedCheck(ctx context.Context, command, root string, prefix []string) task.Evidence {
	evidence := task.Evidence{Task: "TASK-000", Kind: "check", By: "ai", Command: command, Dir: root}
	args := task.SplitCommand(command)
	if len(args) == 0 {
		evidence.ExitCode = -1
		evidence.Note = "empty command"
		return evidence
	}
	checkCtx, cancel := context.WithTimeout(ctx, task.CheckTimeout)
	defer cancel()
	program, commandArgs := args[0], args[1:]
	if len(prefix) > 0 {
		program, commandArgs = prefix[0], append(append([]string(nil), prefix[1:]...), args...)
	}
	cmd := exec.CommandContext(checkCtx, program, commandArgs...)
	cmd.Dir = root
	cmd.Env = safeCheckEnv()
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	evidence.Output = redactCheckOutput(output.String(), root)
	switch {
	case err == nil:
		evidence.Passed = true
		evidence.ExitCode = 0
	case checkCtx.Err() != nil:
		evidence.ExitCode = -1
		evidence.Note = "timed out after " + task.CheckTimeout.String()
	default:
		if exit, ok := err.(*exec.ExitError); ok {
			evidence.ExitCode = exit.ExitCode()
		} else {
			evidence.ExitCode = -1
			evidence.Note = err.Error()
		}
	}
	return evidence
}

func safeCheckEnv() []string {
	allowed := []string{"PATH", "HOME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "CGO_ENABLED", "GOCACHE", "GOMODCACHE", "GOPATH", "GOTOOLCHAIN"}
	env := make([]string, 0, len(allowed)+3)
	for _, name := range allowed {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return append(env, "GOPROXY=off", "GOSUMDB=off", "GIT_TERMINAL_PROMPT=0")
}

func redactCheckOutput(value, root string) string {
	value = strings.ReplaceAll(value, root, "[worktree]")
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		value = strings.ReplaceAll(value, home, "[home]")
	}
	if len(value) > task.MaxOutput {
		value = value[len(value)-task.MaxOutput:]
	}
	return value
}
