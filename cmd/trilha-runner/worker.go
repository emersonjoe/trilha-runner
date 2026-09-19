package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/emersonjoe/trilha-runner/deployer"
	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-runner/queue"
	"github.com/emersonjoe/trilha-runner/runner"
	"github.com/emersonjoe/trilha-runner/syncer"
)

// workerOptions is one worker process: which control plane it serves, what
// it can do and how much of it at once.
type workerOptions struct {
	Queue queue.Remote
	Name  string
	// Dir is the checkout the worker serves when a run carries no bundle.
	Dir           string
	Driver        string
	Command       string
	WorkspaceRoot string
	Repository    string
	DefaultBranch string
	Push          bool
	// Delivery is set when --delivery-config named allow-listed profiles.
	Delivery    deployer.Config
	HasDelivery bool
	// Capacity is how many runs execute at once; at least 1.
	Capacity int
	Once     bool
	Every    time.Duration
	Out      io.Writer
	// Progress receives one line per lifecycle event; nil discards them.
	Progress func(string)
}

func (o workerOptions) capacity() int {
	if o.Capacity < 1 {
		return 1
	}
	return o.Capacity
}

func (o workerOptions) progressf(format string, args ...any) {
	if o.Progress != nil {
		o.Progress(fmt.Sprintf(format, args...))
	}
}

// worker holds what the slots share: the output writer, the count of runs in
// flight and the first transport error that should stop the process.
type worker struct {
	workerOptions
	out      sync.Mutex
	state    sync.Mutex
	running  int
	firstErr error
}

// beat reports the worker's state and its load to the control plane. A
// heartbeat that does not arrive is not worth failing a run over, so the
// error is only reported.
func (w *worker) beat(ctx context.Context, status string) {
	w.state.Lock()
	running := w.running
	w.state.Unlock()
	if status == "" {
		if running == 0 {
			status = "idle"
		} else {
			status = fmt.Sprintf("running %d/%d", running, w.capacity())
		}
	}
	if err := w.Queue.HeartbeatRunning(ctx, status, running); err != nil {
		w.progressf("heartbeat: %v", err)
	}
}

func (w *worker) enter() int {
	w.state.Lock()
	defer w.state.Unlock()
	w.running++
	return w.running
}

func (w *worker) leave() {
	w.state.Lock()
	defer w.state.Unlock()
	w.running--
}

func (w *worker) inFlight() int {
	w.state.Lock()
	defer w.state.Unlock()
	return w.running
}

// fail keeps the first error that must stop the worker — a control plane
// that refuses a result, not a run that failed on its merits.
func (w *worker) fail(err error) {
	w.state.Lock()
	defer w.state.Unlock()
	if w.firstErr == nil {
		w.firstErr = err
	}
}

func (w *worker) err() error {
	w.state.Lock()
	defer w.state.Unlock()
	return w.firstErr
}

func (w *worker) printf(format string, args ...any) {
	w.out.Lock()
	defer w.out.Unlock()
	fmt.Fprintf(w.Out, format, args...)
}

func (w *worker) printResult(res *runner.Result) {
	w.out.Lock()
	defer w.out.Unlock()
	printResult(w.Out, res)
}

// runWorker is the fleet's unit: one process on one checkout of one project,
// asking the control plane for the next run, executing it locally and
// reporting back. The checkout is the operator's; the cloud never sees the
// code, only the result and the evidence.
//
// With --capacity above 1 the worker keeps that many runs in flight, each in
// its own worktree and reported independently; it never claims a run it has
// no slot for, so the control plane can hand the work to another host.
func runWorker(ctx context.Context, o workerOptions) error {
	o.Queue.Capabilities.Capacity = o.capacity()
	if o.Queue.Capabilities.Runner == "" {
		o.Queue.Capabilities.Runner = version
	}
	if o.Queue.Capabilities.Drivers == nil {
		o.Queue.Capabilities.Drivers = driver.Names()
	}
	w := &worker{workerOptions: o}
	if err := o.Queue.HeartbeatRunning(ctx, "idle", 0); err != nil {
		return err
	}
	slots := make(chan struct{}, o.capacity())
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		// A slot first: a worker that cannot execute a run does not claim it.
		select {
		case <-ctx.Done():
			return w.err()
		case slots <- struct{}{}:
		}
		release := func() { <-slots }

		item, err := o.Queue.NextRunning(ctx, w.inFlight())
		switch {
		case errors.Is(err, queue.ErrEmpty):
			release()
			if done, err := w.idle(ctx); err != nil {
				return err
			} else if done {
				return w.err()
			}
			continue
		case errors.Is(err, queue.ErrUnmet):
			// Claimed but not executable here: it is reported, never dropped.
			w.printf("%s → refused (%v)\n", item.TaskID, err)
			if reportErr := o.Queue.Done(ctx, item, queue.Result{Status: "failed", Error: err.Error()}); reportErr != nil {
				release()
				return reportErr
			}
			release()
			if o.Once {
				return w.err()
			}
			continue
		case err != nil:
			release()
			return err
		}

		w.enter()
		w.beat(ctx, "running "+item.TaskID)
		wg.Add(1)
		go func(item queue.Item) {
			defer wg.Done()
			defer release()
			defer func() { w.leave(); w.beat(ctx, "") }()
			w.execute(ctx, item)
		}(item)

		if o.Once {
			wg.Wait()
			return w.err()
		}
		if err := w.err(); err != nil {
			return err
		}
	}
}

// idle is what the worker does when the queue has nothing: a deployment if
// one is waiting and profiles are configured, then a pause. It answers
// whether the worker should stop.
func (w *worker) idle(ctx context.Context) (bool, error) {
	if w.HasDelivery {
		work, err := w.Queue.NextDeployment(ctx)
		switch {
		case err == nil:
			w.beat(ctx, "deploying "+work.Deployment.Environment)
			result := w.Delivery.Execute(ctx, work)
			if err := w.Queue.DoneDeployment(ctx, work, queue.DeploymentResult{Passed: result.Passed, Log: result.Log, Health: result.Health, Error: result.Error}); err != nil {
				return true, err
			}
			w.beat(ctx, "")
			w.printf("%s → %s (%s)\n", work.Deployment.ID, map[bool]string{true: "succeeded", false: "failed"}[result.Passed], work.Deployment.Environment)
			return w.Once, nil
		case !errors.Is(err, queue.ErrEmpty):
			return true, err
		}
	}
	// --once with runs still in flight waits for them rather than exiting.
	if w.Once && w.inFlight() == 0 {
		w.printf("nothing to run\n")
		return true, nil
	}
	select {
	case <-ctx.Done():
		return true, nil
	case <-time.After(w.Every):
	}
	return false, nil
}

// execute runs one claimed item and reports it. A run that fails on its
// merits is a result, not a worker error; only a control plane that refuses
// the report stops the process.
func (w *worker) execute(ctx context.Context, item queue.Item) {
	active, err := newRunnerAt(w.Dir, w.Driver, w.Command, w.Out)
	if err != nil {
		w.report(ctx, item, nil, err)
		return
	}
	active.By = "trilha-runner worker " + w.Name
	active.Access = item.AI
	bundle, bundleErr := w.Queue.Bundle(ctx, item)
	if bundleErr == nil {
		active, bundleErr = w.materialize(ctx, item, bundle)
	}
	if bundleErr != nil && !errors.Is(bundleErr, queue.ErrNoBundle) {
		w.report(ctx, item, nil, bundleErr)
		return
	}
	res, runErr := active.Run(ctx, item.TaskID)
	if runErr == nil && res != nil && w.Push {
		runErr = syncer.PushBranch(ctx, res.Worktree, res.Branch)
	}
	w.report(ctx, item, res, runErr)
}

// materialize turns a Cloud work bundle into a checkout the run executes in.
func (w *worker) materialize(ctx context.Context, item queue.Item, bundle queue.Bundle) (*runner.Runner, error) {
	if w.WorkspaceRoot == "" {
		return nil, errors.New("worker needs --workspace-root for Cloud-managed work bundles")
	}
	repository := bundle.Repository
	if w.Repository != "" {
		if repository != "" && repository != w.Repository {
			return nil, errors.New("worker repository does not match the Cloud project")
		}
		repository = w.Repository
	}
	branch := bundle.DefaultBranch
	if w.DefaultBranch != "" {
		if branch != "" && branch != w.DefaultBranch {
			return nil, errors.New("worker default branch does not match the Cloud project")
		}
		branch = w.DefaultBranch
	}
	checkout, err := syncer.EnsureRepository(ctx, w.WorkspaceRoot, item.Project, repository, branch)
	if err != nil {
		return nil, err
	}
	if _, _, err := syncer.Materialize(ctx, checkout, bundle, w.Push); err != nil {
		return nil, err
	}
	active, err := newRunnerAt(checkout.Path, w.Driver, w.Command, w.Out)
	if err != nil {
		return nil, err
	}
	active.By = "trilha-runner worker " + w.Name
	active.Access = item.AI
	return active, nil
}

func (w *worker) report(ctx context.Context, item queue.Item, res *runner.Result, runErr error) {
	report := queue.Result{}
	if res != nil {
		report = queue.Result{Passed: res.Passed, Status: string(res.Status), Branch: res.Branch, Commit: res.Commit, Evidence: res.Evidence, Log: res.Output}
		w.printResult(res)
	}
	if runErr != nil {
		report.Error = runErr.Error()
		if report.Status == "" {
			report.Status = "failed"
		}
	}
	if err := w.Queue.Done(ctx, item, report); err != nil {
		w.fail(err)
	}
}
