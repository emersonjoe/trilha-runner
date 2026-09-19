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

func TestScanMetricsIgnoresOrdinaryOutput(t *testing.T) {
	out := `running the harness
{"metric":"triage_top1","value":0.87,"threshold":0.85,"comparator":">="}
{"not":"a metric"}
[1,2,3]
{"metric":"p95_latency_ms","value":410,"threshold":300,"comparator":"<=","unit":"ms"}
{"metric":"no comparator","value":1,"threshold":0}
done
`
	got := ScanMetrics(out)
	if len(got) != 2 {
		t.Fatalf("scanned %d metrics: %+v", len(got), got)
	}
	if got[0].Metric != "triage_top1" || !got[0].OK() {
		t.Fatalf("first = %+v", got[0])
	}
	if got[1].Metric != "p95_latency_ms" || got[1].OK() || got[1].Unit != "ms" {
		t.Fatalf("second = %+v", got[1])
	}
	if got[0].String() != "triage_top1=0.87 >= 0.85" {
		t.Fatalf("string = %q", got[0].String())
	}
}

func TestMetricComparators(t *testing.T) {
	for _, c := range []struct {
		comparator string
		value      float64
		threshold  float64
		want       bool
	}{
		{">=", 1, 1, true}, {">=", 0.9, 1, false},
		{"<=", 1, 1, true}, {"<=", 1.1, 1, false},
		{">", 1, 1, false}, {">", 2, 1, true},
		{"<", 1, 1, false}, {"<", 0, 1, true},
		{"==", 1, 1, true}, {"==", 2, 1, false},
		{"!=", 2, 1, true}, {"!=", 1, 1, false},
		{"~=", 1, 1, false},
	} {
		m := Metric{Comparator: c.comparator, Value: c.value, Threshold: c.threshold}
		if m.OK() != c.want {
			t.Errorf("%v %s %v = %v, want %v", c.value, c.comparator, c.threshold, m.OK(), c.want)
		}
	}
}

// A harness prints two metrics, one below its threshold: every command exits
// 0 and the task still fails, with one `eval` record per metric.
func TestEvalMetricBelowThresholdFailsTheRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := repo(t)
	os.MkdirAll(filepath.Join(dir, "eval/golden"), 0o755)
	os.WriteFile(filepath.Join(dir, "eval/golden/manifest.json"), []byte(`{"cases":42}`), 0o644)
	harness := filepath.Join(dir, "harness.sh")
	os.WriteFile(harness, []byte(`#!/bin/sh
echo "harness: 42 cases"
echo '{"metric":"triage_top1","value":0.87,"threshold":0.85,"comparator":">=","dataset":{"id":"triage-v3","manifest":"eval/golden/manifest.json"}}'
echo '{"metric":"p95_latency_ms","value":410,"threshold":300,"comparator":"<=","unit":"ms"}'
exit 0
`), 0o755)
	commit(t, dir)

	r, _ := New(dir)
	r.Driver = driver.Echo{}
	tk, _ := r.Store.Create("Quality harness", func(x *task.Task) {
		x.Status = task.Ready
		x.Acceptance = []string{"triage stays above 0.85"}
		x.Checks = []string{"sh harness.sh"}
	})
	res, err := r.Run(context.Background(), tk.ID)
	if err == nil {
		t.Fatal("a metric below its threshold must fail the run")
	}
	if res.Passed || res.Status != task.Failed {
		t.Fatalf("res = %+v", res)
	}
	var evals []task.Evidence
	for _, e := range res.Evidence {
		if e.Kind == "eval" {
			evals = append(evals, e)
		}
	}
	if len(evals) != 2 {
		t.Fatalf("got %d eval records of %d evidence", len(evals), len(res.Evidence))
	}
	if !evals[0].Passed || evals[0].Meta["metric"] != "triage_top1" || evals[0].Meta["comparator"] != ">=" {
		t.Fatalf("first eval = %+v", evals[0])
	}
	// The manifest is hashed; the data never is.
	if evals[0].Meta["dataset"] != "triage-v3" || len(evals[0].Meta["dataset_sha256"]) != 64 {
		t.Fatalf("dataset evidence = %+v", evals[0].Meta)
	}
	if evals[1].Passed || evals[1].Meta["metric"] != "p95_latency_ms" || evals[1].Meta["unit"] != "ms" {
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
	want := "triage_top1=0.87 >= 0.85 pass; p95_latency_ms=410 <= 300 fail"
	if run.Meta["metrics"] != want {
		t.Fatalf("metrics = %q, want %q", run.Meta["metrics"], want)
	}
}

// A metric that is met leaves the run passing, and a manifest the harness
// names but does not ship is reported as unreadable rather than silently
// dropped.
func TestEvalMetricMetPassesAndReportsMissingManifest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := repo(t)
	os.WriteFile(filepath.Join(dir, "harness.sh"), []byte(`#!/bin/sh
echo '{"metric":"a11y_violations","value":0,"threshold":0,"comparator":"==","dataset":{"id":"pages","manifest":"eval/missing.json"}}'
`), 0o755)
	commit(t, dir)

	r, _ := New(dir)
	r.Driver = driver.Echo{}
	tk, _ := r.Store.Create("Accessibility", func(x *task.Task) {
		x.Status = task.Ready
		x.Acceptance = []string{"no violations"}
		x.Checks = []string{"sh harness.sh"}
	})
	res, err := r.Run(context.Background(), tk.ID)
	if err != nil || !res.Passed || res.Status != task.Review {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	for _, e := range res.Evidence {
		if e.Kind != "eval" {
			continue
		}
		if !e.Passed {
			t.Fatalf("eval = %+v", e)
		}
		if got := e.Meta["dataset_sha256"]; got == "" || len(got) == 64 {
			t.Fatalf("missing manifest should say so, got %q", got)
		}
		return
	}
	t.Fatal("no eval record")
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
