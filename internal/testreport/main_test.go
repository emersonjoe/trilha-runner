package main

import (
	"strings"
	"testing"
)

const stream = `{"Action":"run","Package":"github.com/emersonjoe/trilha-runner/runner","Test":"TestRunPipeline"}
{"Action":"pass","Package":"github.com/emersonjoe/trilha-runner/runner","Test":"TestRunPipeline","Elapsed":0.40}
{"Action":"run","Package":"github.com/emersonjoe/trilha-runner/runner","Test":"TestRunPipeline/sub"}
{"Action":"fail","Package":"github.com/emersonjoe/trilha-runner/runner","Test":"TestRunPipeline/sub","Elapsed":0.01}
{"Action":"pass","Package":"github.com/emersonjoe/trilha-runner/worktree","Test":"TestIsRepo","Elapsed":2.50}
{"Action":"fail","Package":"github.com/emersonjoe/trilha-runner/queue","Test":"TestRemote","Elapsed":0.10}
{"Action":"fail","Package":"github.com/emersonjoe/trilha-runner/queue","Elapsed":0.20}
{"Action":"output","Package":"github.com/emersonjoe/trilha-runner/sandbox","Test":"TestDockerPostgresAndTeardown","Output":"=== RUN   TestDockerPostgresAndTeardown\n"}
{"Action":"output","Package":"github.com/emersonjoe/trilha-runner/sandbox","Test":"TestDockerPostgresAndTeardown","Output":"    docker_test.go:33: Docker is unavailable\n"}
{"Action":"output","Package":"github.com/emersonjoe/trilha-runner/sandbox","Test":"TestDockerPostgresAndTeardown","Output":"--- SKIP: TestDockerPostgresAndTeardown (0.00s)\n"}
{"Action":"skip","Package":"github.com/emersonjoe/trilha-runner/sandbox","Test":"TestDockerPostgresAndTeardown"}
{"Action":"fail","Package":"github.com/emersonjoe/trilha-runner/syncer","Elapsed":0}
`

const profile = `mode: set
github.com/emersonjoe/trilha-runner/runner/runner.go:10.1,12.2 3 1
github.com/emersonjoe/trilha-runner/runner/runner.go:14.1,16.2 1 0
github.com/emersonjoe/trilha-runner/runner/runner.go:14.1,16.2 1 1
github.com/emersonjoe/trilha-runner/worktree/worktree.go:5.1,9.2 4 0
`

func TestReportSummarizesARun(t *testing.T) {
	s, err := summarize(strings.NewReader("not json from a build line\n" + stream))
	if err != nil {
		t.Fatal(err)
	}
	// Subtests belong to their parent; a package failing with a test
	// already failed is not counted twice; a package failing alone (a build
	// error) is.
	if s.Passed != 2 || s.Failed != 2 || s.Skipped != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if strings.Join(s.Failures, ",") != "queue.TestRemote,syncer (package)" {
		t.Fatalf("failures = %v", s.Failures)
	}
	if len(s.Skips) != 1 || s.Skips[0] != "sandbox.TestDockerPostgresAndTeardown: Docker is unavailable" {
		t.Fatalf("skips = %v", s.Skips)
	}
	if s.Slowest[0].Name != "worktree.TestIsRepo" {
		t.Fatalf("slowest = %v", s.Slowest)
	}
	byPackage, total, _ := coverageByPackage(strings.NewReader(profile))
	report, ok := render(s, byPackage, total, 0, 1)
	if ok {
		t.Fatal("a run with failures was accepted")
	}
	for _, want := range []string{"| 2 | 2 | 1 |", "`queue.TestRemote`", "Docker is unavailable", "| `worktree.TestIsRepo` | 2.50 |", "| `runner` | 4 | 100.0% |"} {
		if !strings.Contains(report, want) {
			t.Errorf("report misses %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "TestRunPipeline` | 0.40") {
		t.Error("-top 1 listed more than one test")
	}
}

func TestCoverageByPackage(t *testing.T) {
	byPackage, total, err := coverageByPackage(strings.NewReader(profile))
	if err != nil {
		t.Fatal(err)
	}
	// The block listed twice counts once, covered.
	if c := byPackage["github.com/emersonjoe/trilha-runner/runner"]; c.Statements != 4 || c.Covered != 4 {
		t.Fatalf("runner = %+v", c)
	}
	if c := byPackage["github.com/emersonjoe/trilha-runner/worktree"]; c.Statements != 4 || c.Covered != 0 {
		t.Fatalf("worktree = %+v", c)
	}
	if total.Statements != 8 || total.Percent() != 50 {
		t.Fatalf("total = %+v (%.1f%%)", total, total.Percent())
	}
	if _, _, err := coverageByPackage(strings.NewReader("mode: set\nnot a profile line\n")); err == nil {
		t.Fatal("a malformed profile was accepted")
	}
}

func TestReportFailsBelowTheFloor(t *testing.T) {
	floor, err := readFloor(strings.NewReader("# minimum total coverage\n71.0\n"))
	if err != nil || floor != 71 {
		t.Fatalf("floor = %v %v", floor, err)
	}
	if _, err := readFloor(strings.NewReader("# nothing\n")); err == nil {
		t.Fatal("a floor file without a number was accepted")
	}
	passing := summary{Passed: 3}
	at := func(percent int) coverage { return coverage{Statements: 1000, Covered: percent * 10} }

	if report, ok := render(passing, nil, at(70), floor, 10); ok || !strings.Contains(report, "below the floor") {
		t.Fatalf("70%% under a 71%% floor: ok=%v\n%s", ok, report)
	}
	if report, ok := render(passing, nil, at(71), floor, 10); !ok || strings.Contains(report, "raise") {
		t.Fatalf("at the floor: ok=%v\n%s", ok, report)
	}
	if report, ok := render(passing, nil, at(73), floor, 10); !ok || !strings.Contains(report, "raise `testdata/coverage-floor.txt` to 73.0") {
		t.Fatalf("two points above: ok=%v\n%s", ok, report)
	}
}
