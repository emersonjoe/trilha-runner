// Command trilha-runner executes tasks of the Trilha protocol: one task in
// its own worktree with the agent its manifest names, or a worker loop fed
// by trilha-cloud. `trilha runner …` once the framework dispatches external
// subcommands; `trilha-runner …` always.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/emersonjoe/trilha-runner/deployer"
	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-runner/queue"
	"github.com/emersonjoe/trilha-runner/runner"
	"github.com/emersonjoe/trilha-runner/sandbox"
	"github.com/emersonjoe/trilha-runner/worktree"
	"github.com/emersonjoe/trilha-spec/agent"
)

const version = "0.3.0"

const usage = `trilha-runner ` + version + ` — executes tasks of the Trilha protocol

usage: trilha-runner <command> [flags]

  run <task-id> [--driver exec|claude-code|ai|echo] [--cmd "claude -p -"] [--json]
                [--sandbox docker]
                         run one ready task in its own worktree, verify, record evidence
  next [flags of run] [--repo trilha=../trilha]
                         run the first task that is ready with every dependency done,
                         including dependencies in the sibling checkouts --repo names
  worker --cloud URL --token T --project P [--workspace-root DIR] [--repo URL]
         [--default-branch main] [--push] [--name N] [--once] [--every 10s]
         [--label docker --label region:br] [--capacity 2]
                         take runs from trilha-cloud and report results
  worktree list | clean <task-id>
  drivers                the drivers available
  version

--sandbox docker runs the agent and the checks inside a container with the services the
agent manifest's "sandbox:" declares; the worktree is the only writable path that survives
and the resource limits are the runner's, not the manifest's.

--label declares what this host can do (repeatable; TRILHA_WORKER_LABELS) and --capacity how
many runs it executes at once (TRILHA_WORKER_CAPACITY): both ride the heartbeat and the claim,
so the control plane routes a run to a host that meets its requirements.

The driver comes from the task's agent manifest (.trilha/agents/<name>.md) unless --driver
is given. exec needs a command (manifest or --cmd); ai reads OPENAI_BASE_URL, OPENAI_API_KEY
and TRILHA_AI_MODEL.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1], os.Args[2:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cmd string, args []string, out io.Writer) error {
	switch cmd {
	case "run", "next":
		return cmdRun(ctx, cmd, args, out)
	case "worker":
		return cmdWorker(ctx, args, out)
	case "worktree":
		return cmdWorktree(ctx, args, out)
	case "drivers":
		fmt.Fprintln(out, strings.Join(driver.Names(), "\n"))
		return nil
	case "version", "-v", "--version":
		fmt.Fprintln(out, "trilha-runner", version)
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(out, usage)
		return nil
	}
	return fmt.Errorf("unknown command %q\n%s", cmd, usage)
}

func flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parse reads flags anywhere on the line and answers the positionals.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func newRunner(drv, command string, out io.Writer) (*runner.Runner, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return newRunnerAt(cwd, drv, command, out)
}

func newRunnerAt(path, drv, command string, out io.Writer) (*runner.Runner, error) {
	r, err := runner.New(path)
	if err != nil {
		return nil, err
	}
	if drv != "" {
		if r.Driver, err = driver.New(drv); err != nil {
			return nil, err
		}
	}
	r.Command = command
	r.Log = func(s string) { fmt.Fprintln(os.Stderr, "·", s) }
	return r, nil
}

// withSandbox puts the runner in a container when the operator asked for
// one. The manifest says what to build; the host says whether it may.
func withSandbox(ctx context.Context, r *runner.Runner, name, agentName string) error {
	switch name {
	case "", "none":
		return nil
	case "docker":
	default:
		return fmt.Errorf("unknown sandbox %q (have none, docker)", name)
	}
	man, err := agentManifest(r, agentName)
	if err != nil {
		return err
	}
	spec, err := sandbox.FromAgent(man, r.Layout.Root)
	if err != nil {
		return fmt.Errorf("--sandbox docker: %w", err)
	}
	box := sandbox.Docker{Log: r.Log}
	if err := box.Available(ctx); err != nil {
		return err
	}
	r.Sandbox, r.SandboxSpec = box, spec
	return nil
}

// agentManifest answers the manifest a run will use: the one named, or the
// project's default.
func agentManifest(r *runner.Runner, name string) (*agent.Agent, error) {
	if name == "" {
		proj, err := r.Layout.LoadProject()
		if err != nil {
			return nil, err
		}
		name = proj.DefaultAgent
	}
	if name == "" {
		return nil, errors.New("no agent manifest: name one with --agent or set default_agent in project.md")
	}
	return agent.Load(r.Layout, name)
}

func cmdRun(ctx context.Context, cmd string, args []string, out io.Writer) error {
	fs := flags(cmd)
	drv := fs.String("driver", "", "exec | claude-code | ai | echo (default: the agent manifest's)")
	command := fs.String("cmd", "", "command for the exec driver")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	var repos repeated
	fs.Var(&repos, "repo", "a sibling checkout for cross-repository dependencies, repeatable: alias=path")
	box := fs.String("sandbox", os.Getenv("TRILHA_SANDBOX"), "none | docker (docker needs a sandbox: in the agent manifest)")
	agentName := fs.String("agent", "", "the agent manifest whose sandbox to use (default: the project's)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	r, err := newRunner(*drv, *command, out)
	if err != nil {
		return err
	}
	if err := withSandbox(ctx, r, *box, *agentName); err != nil {
		return err
	}
	var res *runner.Result
	if cmd == "run" {
		if len(pos) != 1 {
			return errors.New("usage: trilha-runner run <task-id>")
		}
		res, err = r.Run(ctx, pos[0])
	} else {
		var id string
		if id, err = nextTask(ctx, r, repos, out); err == nil {
			res, err = r.Run(ctx, id)
		}
	}
	if res != nil {
		if *asJSON {
			printJSON(out, res)
		} else {
			printResult(out, res)
		}
	}
	return err
}

// nextTask picks the task to run: the first one that is ready with every
// dependency done, in this repository and — when --repo named the sibling
// checkouts — in the others. What it passed over is printed, so an agenda
// that is waiting says what for instead of "nothing to run".
func nextTask(ctx context.Context, r *runner.Runner, repos []string, out io.Writer) (string, error) {
	q := queue.Local{Store: r.Store, By: "trilha-runner next"}
	if len(repos) > 0 {
		checkouts := queue.Checkouts{}
		for _, value := range repos {
			alias, dir, err := queue.ParseRepo(value)
			if err != nil {
				return "", err
			}
			checkouts[alias] = dir
		}
		q.Resolver = checkouts
	}
	item, waiting, err := q.NextWaiting(ctx)
	for _, w := range waiting {
		fmt.Fprintf(out, "· %s\n", w)
	}
	if errors.Is(err, queue.ErrEmpty) {
		return "", runner.ErrNothing
	}
	if err != nil {
		return "", err
	}
	return item.TaskID, nil
}

func printResult(out io.Writer, res *runner.Result) {
	fmt.Fprintf(out, "%s → %s (driver %s, %s)\n", res.Task, res.Status, res.Driver, res.Elapsed.Round(time.Millisecond))
	fmt.Fprintf(out, "branch %s in %s\n", res.Branch, res.Worktree)
	if res.Commit != "" {
		fmt.Fprintf(out, "commit %s\n", res.Commit)
	}
	for _, e := range res.Evidence {
		mark := "✗"
		if e.Passed {
			mark = "✓"
		}
		if e.Command != "" {
			fmt.Fprintf(out, "%s %s (exit %d)\n", mark, e.Command, e.ExitCode)
		}
	}
}

func printJSON(out io.Writer, v any) {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func cmdWorker(ctx context.Context, args []string, out io.Writer) error {
	fs := flags("worker")
	cloud := fs.String("cloud", os.Getenv("TRILHA_CLOUD_URL"), "control plane URL")
	token := fs.String("token", os.Getenv("TRILHA_CLOUD_TOKEN"), "bearer token")
	project := fs.String("project", os.Getenv("TRILHA_PROJECT"), "project name on the control plane")
	name := fs.String("name", hostname(), "worker name")
	once := fs.Bool("once", false, "run at most one item and exit")
	every := fs.Duration("every", 10*time.Second, "poll interval when idle")
	drv := fs.String("driver", "", "driver override")
	command := fs.String("cmd", "", "command for the exec driver")
	workspaceRoot := fs.String("workspace-root", os.Getenv("TRILHA_WORKSPACE_ROOT"), "root for isolated project checkouts")
	repository := fs.String("repo", os.Getenv("TRILHA_REPOSITORY"), "trusted repository URL override")
	defaultBranch := fs.String("default-branch", os.Getenv("TRILHA_DEFAULT_BRANCH"), "trusted default branch override")
	push := fs.Bool("push", false, "publish synchronized spec and implementation branches")
	deployConfig := fs.String("delivery-config", os.Getenv("TRILHA_DELIVERY_CONFIG"), "JSON file with local allow-listed deployment profiles")
	labels := repeated(envList("TRILHA_WORKER_LABELS"))
	fs.Var(&labels, "label", "a capability of this host, repeatable: docker, gpu, region:br")
	capacity := fs.Int("capacity", envInt("TRILHA_WORKER_CAPACITY", 1), "how many runs to execute at once")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *cloud == "" || *token == "" || *project == "" {
		return errors.New("worker needs --cloud, --token and --project")
	}
	if *capacity < 1 {
		return errors.New("--capacity must be at least 1")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	options := workerOptions{
		Queue: queue.Remote{
			BaseURL: strings.TrimSuffix(*cloud, "/"), Token: *token, Worker: *name, Project: *project,
			Capabilities: queue.Capabilities{Labels: labels, Capacity: *capacity, Runner: version, Drivers: driver.Names()},
		},
		Name: *name, Dir: cwd, Driver: *drv, Command: *command,
		WorkspaceRoot: *workspaceRoot, Repository: *repository, DefaultBranch: *defaultBranch,
		Push: *push, Capacity: *capacity, Once: *once, Every: *every, Out: out,
		Progress: func(s string) { fmt.Fprintln(os.Stderr, "·", s) },
	}
	if *deployConfig != "" {
		if options.Delivery, err = deployer.Load(*deployConfig); err != nil {
			return err
		}
		options.HasDelivery = true
	}
	fmt.Fprintf(os.Stderr, "worker %s on %s, project %s (capacity %d%s)\n", *name, *cloud, *project, *capacity, labelSuffix(labels))
	return runWorker(ctx, options)
}

func labelSuffix(labels []string) string {
	if len(labels) == 0 {
		return ""
	}
	return ", labels " + strings.Join(labels, " ")
}

// repeated is a flag given more than once: --label docker --label region:br.
type repeated []string

func (r repeated) String() string { return strings.Join(r, ",") }

func (r *repeated) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return errors.New("empty value")
	}
	*r = append(*r, v)
	return nil
}

// envList reads a comma- or space-separated default for a repeated flag.
func envList(name string) []string {
	var out []string
	for _, v := range strings.FieldsFunc(os.Getenv(name), func(r rune) bool { return r == ',' || r == ' ' }) {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func envInt(name string, fallback int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && v > 0 {
		return v
	}
	return fallback
}

func cmdWorktree(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: trilha-runner worktree list | clean <task-id>")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	r, err := runner.New(cwd)
	if err != nil {
		return err
	}
	wm := worktree.Manager{Layout: r.Layout}
	switch args[0] {
	case "list":
		list, err := wm.List()
		if err != nil {
			return err
		}
		for _, w := range list {
			fmt.Fprintf(out, "%-10s %-22s %s\n", w.Task, w.Branch, w.Path)
		}
		return nil
	case "clean":
		if len(args) != 2 {
			return errors.New("usage: trilha-runner worktree clean <task-id>")
		}
		if err := wm.Remove(ctx, args[1]); err != nil {
			return err
		}
		fmt.Fprintf(out, "removed worktree of %s (branch %s kept)\n", args[1], worktree.Branch(args[1]))
		return nil
	}
	return fmt.Errorf("unknown worktree command %q", args[0])
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "worker"
	}
	return h
}
