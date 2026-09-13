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
	"strings"
	"time"

	"github.com/emersonjoe/trilha-runner/deployer"
	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-runner/queue"
	"github.com/emersonjoe/trilha-runner/runner"
	"github.com/emersonjoe/trilha-runner/syncer"
	"github.com/emersonjoe/trilha-runner/worktree"
)

const version = "0.2.0"

const usage = `trilha-runner ` + version + ` — executes tasks of the Trilha protocol

usage: trilha-runner <command> [flags]

  run <task-id> [--driver exec|ai|echo] [--cmd "claude -p -"] [--json]
                         run one ready task in its own worktree, verify, record evidence
  next [flags of run]    run the first task that is ready with every dependency done
  worker --cloud URL --token T --project P [--workspace-root DIR] [--repo URL]
         [--default-branch main] [--push] [--name N] [--once] [--every 10s]
                         take runs from trilha-cloud and report results
  worktree list | clean <task-id>
  drivers                the drivers available
  version

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

func cmdRun(ctx context.Context, cmd string, args []string, out io.Writer) error {
	fs := flags(cmd)
	drv := fs.String("driver", "", "exec | ai | echo (default: the agent manifest's)")
	command := fs.String("cmd", "", "command for the exec driver")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	r, err := newRunner(*drv, *command, out)
	if err != nil {
		return err
	}
	var res *runner.Result
	if cmd == "run" {
		if len(pos) != 1 {
			return errors.New("usage: trilha-runner run <task-id>")
		}
		res, err = r.Run(ctx, pos[0])
	} else {
		res, err = r.Next(ctx)
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

// cmdWorker is the fleet's unit: one process on one checkout of one
// project, asking trilha-cloud for the next run, executing it locally and
// reporting back. The checkout is the operator's; the cloud never sees the
// code, only the result and the evidence.
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
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *cloud == "" || *token == "" || *project == "" {
		return errors.New("worker needs --cloud, --token and --project")
	}
	r, err := newRunner(*drv, *command, out)
	if err != nil {
		return err
	}
	r.By = "trilha-runner worker " + *name
	q := queue.Remote{BaseURL: strings.TrimSuffix(*cloud, "/"), Token: *token, Worker: *name, Project: *project}
	var delivery deployer.Config
	if *deployConfig != "" {
		delivery, err = deployer.Load(*deployConfig)
		if err != nil {
			return err
		}
	}
	if err := q.Heartbeat(ctx, "idle"); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "worker %s on %s, project %s\n", *name, *cloud, *project)
	for {
		item, err := q.Next(ctx)
		switch {
		case errors.Is(err, queue.ErrEmpty):
			if *deployConfig != "" {
				work, deploymentErr := q.NextDeployment(ctx)
				if deploymentErr == nil {
					q.Heartbeat(ctx, "deploying "+work.Deployment.Environment)
					result := delivery.Execute(ctx, work)
					if err := q.DoneDeployment(ctx, work, queue.DeploymentResult{Passed: result.Passed, Log: result.Log, Health: result.Health, Error: result.Error}); err != nil {
						return err
					}
					q.Heartbeat(ctx, "idle")
					fmt.Fprintf(out, "%s → %s (%s)\n", work.Deployment.ID, map[bool]string{true: "succeeded", false: "failed"}[result.Passed], work.Deployment.Environment)
					if *once {
						return nil
					}
					continue
				}
				if !errors.Is(deploymentErr, queue.ErrEmpty) {
					return deploymentErr
				}
			}
			if *once {
				fmt.Fprintln(out, "nothing to run")
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(*every):
			}
			continue
		case err != nil:
			return err
		}
		q.Heartbeat(ctx, "running "+item.TaskID)
		activeRunner := r
		bundle, bundleErr := q.Bundle(ctx, item)
		if bundleErr == nil {
			if *workspaceRoot == "" {
				return errors.New("worker needs --workspace-root for Cloud-managed work bundles")
			}
			bundleRepository := bundle.Repository
			if *repository != "" {
				if bundleRepository != "" && bundleRepository != *repository {
					return errors.New("worker repository does not match the Cloud project")
				}
				bundleRepository = *repository
			}
			bundleBranch := bundle.DefaultBranch
			if *defaultBranch != "" {
				if bundleBranch != "" && bundleBranch != *defaultBranch {
					return errors.New("worker default branch does not match the Cloud project")
				}
				bundleBranch = *defaultBranch
			}
			repositoryCheckout, err := syncer.EnsureRepository(ctx, *workspaceRoot, item.Project, bundleRepository, bundleBranch)
			if err != nil {
				bundleErr = err
			} else if _, _, err := syncer.Materialize(ctx, repositoryCheckout, bundle, *push); err != nil {
				bundleErr = err
			} else {
				activeRunner, bundleErr = newRunnerAt(repositoryCheckout.Path, *drv, *command, out)
				if bundleErr == nil {
					activeRunner.By = "trilha-runner worker " + *name
				}
			}
		}
		var res *runner.Result
		var runErr error
		if bundleErr != nil && !errors.Is(bundleErr, queue.ErrNoBundle) {
			runErr = bundleErr
		} else {
			res, runErr = activeRunner.Run(ctx, item.TaskID)
			if runErr == nil && res != nil && *push {
				runErr = syncer.PushBranch(ctx, res.Worktree, res.Branch)
			}
		}
		report := queue.Result{}
		if res != nil {
			report = queue.Result{Passed: res.Passed, Status: string(res.Status), Branch: res.Branch, Commit: res.Commit, Evidence: res.Evidence, Log: res.Output}
			printResult(out, res)
		}
		if runErr != nil {
			report.Error = runErr.Error()
			if report.Status == "" {
				report.Status = "failed"
			}
		}
		if err := q.Done(ctx, item, report); err != nil {
			return err
		}
		q.Heartbeat(ctx, "idle")
		if *once {
			return nil
		}
	}
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
