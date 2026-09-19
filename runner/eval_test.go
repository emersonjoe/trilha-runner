package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-runner/sandbox"
	"github.com/emersonjoe/trilha-spec/task"
)

// metrics answers a result's eval records, which the protocol writes while the
// checks run.
func metrics(res *Result) []task.Evidence {
	var out []task.Evidence
	for _, e := range res.Evidence {
		if e.Kind == task.KindEval {
			out = append(out, e)
		}
	}
	return out
}

// A harness prints two metrics, one below its threshold: every command exits 0
// and the task still fails, with one `eval` record per metric and the numbers
// in the run record's summary.
func TestEvalMetricBelowThresholdFailsTheRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := repo(t)
	os.WriteFile(filepath.Join(dir, "harness.sh"), []byte(`#!/bin/sh
echo "harness: 42 cases"
echo '{"metric":"triage_top1","value":0.87,"threshold":0.85,"comparator":">=","dataset":{"id":"triage-v3","sha256":"c0ffee"}}'
echo '{"metric":"p95_latency_ms","value":410,"threshold":300,"comparator":"<=","unit":"ms"}'
exit 0
`), 0o755)
	commit(t, dir)

	r, _ := New(dir)
	r.Driver = driver.Echo{}
	tk, _ := r.Store.Create("Quality harness", func(x *task.Task) {
		x.Status = task.Ready
		x.Acceptance = []string{"metric: triage_top1 >= 0.85"}
		x.Checks = []string{"sh harness.sh"}
	})
	res, err := r.Run(context.Background(), tk.ID)
	if err == nil {
		t.Fatal("a metric below its threshold must fail the run")
	}
	if res.Passed || res.Status != task.Failed {
		t.Fatalf("res = %+v", res)
	}
	evals := metrics(res)
	if len(evals) != 2 {
		t.Fatalf("got %d eval records of %d evidence", len(evals), len(res.Evidence))
	}
	if !evals[0].Passed || evals[0].Metric != "triage_top1" || evals[0].Comparator != ">=" {
		t.Fatalf("first eval = %+v", evals[0])
	}
	// The value and the threshold are fields of the record, not prose in it.
	if evals[0].Value == nil || *evals[0].Value != 0.87 || evals[0].Threshold == nil || *evals[0].Threshold != 0.85 {
		t.Fatalf("first eval numbers = %+v", evals[0])
	}
	if evals[0].Dataset == nil || evals[0].Dataset.ID != "triage-v3" || evals[0].Dataset.SHA256 != "c0ffee" {
		t.Fatalf("dataset = %+v", evals[0].Dataset)
	}
	if evals[1].Passed || evals[1].Metric != "p95_latency_ms" || evals[1].Unit != "ms" {
		t.Fatalf("second eval = %+v", evals[1])
	}
	// The check itself passed: the exit code says the harness ran.
	for _, e := range res.Evidence {
		if e.Kind == "check" && !e.Passed {
			t.Fatalf("check should have passed: %+v", e)
		}
	}
	// The run record's summary carries the numbers.
	run := res.Evidence[len(res.Evidence)-1]
	if run.Kind != "run" || run.Passed {
		t.Fatalf("run record = %+v", run)
	}
	want := "p95_latency_ms 410 ms <= 300 fail; triage_top1 0.87 >= 0.85 on triage-v3 pass"
	if run.Meta["metrics"] != want {
		t.Fatalf("metrics = %q, want %q", run.Meta["metrics"], want)
	}
}

// A metric that is met leaves the run passing, and one with no threshold is a
// measurement rather than a gate: it is recorded and it does not fail anything.
func TestEvalMetricMetAndMeasurementWithoutAGate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := repo(t)
	os.WriteFile(filepath.Join(dir, "harness.sh"), []byte(`#!/bin/sh
echo '{"metric":"a11y_violations","value":0,"threshold":0,"comparator":"=="}'
echo '{"metric":"pages_scanned","value":137}'
`), 0o755)
	commit(t, dir)

	r, _ := New(dir)
	r.Driver = driver.Echo{}
	tk, _ := r.Store.Create("Accessibility", func(x *task.Task) {
		x.Status = task.Ready
		x.Acceptance = []string{"metric: a11y_violations == 0"}
		x.Checks = []string{"sh harness.sh"}
	})
	res, err := r.Run(context.Background(), tk.ID)
	if err != nil || !res.Passed || res.Status != task.Review {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	evals := metrics(res)
	if len(evals) != 2 {
		t.Fatalf("got %d eval records", len(evals))
	}
	// Zero is a number a gate cares about: it must not be read as "no value".
	if evals[0].Value == nil || *evals[0].Value != 0 || !evals[0].Passed {
		t.Fatalf("a11y_violations = %+v", evals[0])
	}
	if evals[1].Threshold != nil || !evals[1].Passed {
		t.Fatalf("a metric with no threshold is a measurement: %+v", evals[1])
	}
}

// Residency is enforced by the runner too: a refusal is a `run` record with
// stage `policy` and the task goes to failed, never to review.
func TestPolicyRefusalIsEvidenceAndFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := repo(t)
	r, _ := New(dir)
	r.Command = "true"
	r.Access = &driver.Access{
		Provider:     "anthropic",
		BaseURL:      "https://api.anthropic.com/v1",
		Credential:   "sk-ant-0123456789abcdef",
		AllowedHosts: []string{"gateway.example.br"},
	}
	tk, _ := r.Store.Create("Residency", func(x *task.Task) {
		x.Status = task.Ready
		x.Acceptance = []string{"stays in the country"}
		x.Checks = []string{"true"}
	})
	res, err := r.Run(context.Background(), tk.ID)
	if !errors.Is(err, driver.ErrPolicy) {
		t.Fatalf("err = %v", err)
	}
	if res.Status != task.Failed || res.Passed {
		t.Fatalf("res = %+v", res)
	}
	ev, _ := task.ListEvidence(r.Layout, tk.ID)
	if len(ev) != 1 || ev[0].Kind != "run" || ev[0].Meta["stage"] != "policy" {
		t.Fatalf("evidence = %+v", ev)
	}
	if ev[0].Meta["base_url"] != "https://api.anthropic.com/v1" || ev[0].Meta["allowed_hosts"] != "gateway.example.br" {
		t.Fatalf("meta = %v", ev[0].Meta)
	}
	// Never the credential, in any field of the record.
	blob := ev[0].Note + ev[0].Output
	for _, v := range ev[0].Meta {
		blob += v
	}
	if strings.Contains(blob, r.Access.Credential) {
		t.Fatalf("the credential leaked into the evidence: %+v", ev[0])
	}
}

// wrapping is a sandbox that says the work happens somewhere else without
// needing a container runtime: it is how the runner's own wiring is tested
// on a machine with no Docker.
type wrapping struct {
	prefix   []string
	released bool
}

func (w *wrapping) Name() string { return "wrapping" }

func (w *wrapping) Prepare(ctx context.Context, req sandbox.Request) (sandbox.Env, func() error, error) {
	return sandbox.Env{Dir: req.Dir, WorkDir: "/workspace", Prefix: w.prefix},
		func() error { w.released = true; return nil }, nil
}

// With a sandbox in the way, the checks run through it and the evidence says
// so — and the sandbox is released whatever happened.
func TestChecksRunInsideTheSandbox(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := repo(t)
	// A script, because a command line here is argv: no shell, no quoting.
	os.WriteFile(filepath.Join(dir, "inside.sh"), []byte("#!/bin/sh\ntest -n \"$TRILHA_INSIDE\"\n"), 0o755)
	commit(t, dir)
	r, _ := New(dir)
	r.Driver = driver.Echo{}
	// `env` is a program: it sets what follows in the environment and runs
	// it, which is the shape of `docker exec <container>`.
	box := &wrapping{prefix: []string{"env", "TRILHA_INSIDE=1"}}
	r.Sandbox = box
	tk, _ := r.Store.Create("Sandboxed", func(x *task.Task) {
		x.Status = task.Ready
		x.Acceptance = []string{"it runs inside"}
		x.Checks = []string{"sh inside.sh"}
	})
	res, err := r.Run(context.Background(), tk.ID)
	if err != nil || !res.Passed {
		t.Fatalf("%+v %v", res, err)
	}
	var ran string
	for _, e := range res.Evidence {
		if e.Kind == "check" {
			ran = e.Command
		}
	}
	if ran != "env TRILHA_INSIDE=1 sh inside.sh" {
		t.Fatalf("the evidence does not say where it ran: %q", ran)
	}
	if !box.released {
		t.Fatal("the sandbox was not released")
	}
	// The same check without the sandbox fails: the wrapping is what made it
	// pass, so the test proves the wrapping and not the check.
	r.Store.Move(tk.ID, task.Ready)
	r.Sandbox = sandbox.None{}
	if res, err := r.Run(context.Background(), tk.ID); err == nil || res.Passed {
		t.Fatalf("%+v %v", res, err)
	}
}

// The sandboxed path records metrics the same way the protocol's own does: a
// gate missed inside the container fails the task just as it would outside.
func TestMetricsInsideTheSandbox(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := repo(t)
	os.WriteFile(filepath.Join(dir, "harness.sh"), []byte(`#!/bin/sh
echo '{"metric":"triage_top1","value":0.10,"threshold":0.85,"comparator":">="}'
`), 0o755)
	commit(t, dir)
	r, _ := New(dir)
	r.Driver = driver.Echo{}
	r.Sandbox = &wrapping{prefix: []string{"env", "TRILHA_INSIDE=1"}}
	tk, _ := r.Store.Create("Sandboxed harness", func(x *task.Task) {
		x.Status = task.Ready
		x.Acceptance = []string{"metric: triage_top1 >= 0.85"}
		x.Checks = []string{"sh harness.sh"}
	})
	res, err := r.Run(context.Background(), tk.ID)
	if err == nil || res.Passed || res.Status != task.Failed {
		t.Fatalf("%+v %v", res, err)
	}
	evals := metrics(res)
	if len(evals) != 1 || evals[0].Passed || evals[0].Metric != "triage_top1" {
		t.Fatalf("evals = %+v", evals)
	}
	// The record says where it ran, wrapping and all.
	if !strings.HasPrefix(evals[0].Command, "env TRILHA_INSIDE=1 ") {
		t.Fatalf("eval command = %q", evals[0].Command)
	}
}
