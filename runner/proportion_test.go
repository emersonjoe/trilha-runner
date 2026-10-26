package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-spec/execution"
	"github.com/emersonjoe/trilha-spec/task"
)

// readyTask is a ready task whose check always passes.
func readyTask(t *testing.T, r *Runner) *task.Task {
	t.Helper()
	tk, err := r.Store.Create("Add three lines to the README", func(x *task.Task) {
		x.Status = task.Ready
		x.Acceptance = []string{"README.md lists the three steps"}
		x.Checks = []string{`sh -c "test -f README.md"`}
	})
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

// Proportional changes: every implement run is told to make the smallest
// change and to explain (note) or ask before a larger one; a review is told
// to report every change the task does not need; a hunt gets neither.
func TestImplementPromptAsksForTheSmallestChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	implementRules := []string{"## Keep the change small", "Make the smallest change that meets the acceptance criteria and passes the checks", "Do not refactor, rename, reformat or touch files the task does not need", "Add no new abstraction or dependency unless the task asks for one", "`trilha-runner note \"<why>\"`", "`trilha-runner ask \"<question>\"`"}
	reviewRules := []string{"4. Proportion", "severity `medium`", "`Unneeded change:`", "refactor, a rename, reformatting, a new abstraction or dependency, an unrelated file, dead code", "the verdict is `changes`"}

	t.Run("implement", func(t *testing.T) {
		r, err := New(repo(t))
		if err != nil {
			t.Fatal(err)
		}
		r.Log = func(string) {}
		agent := &actor{t: t}
		r.Driver = agent
		if _, err := r.Run(context.Background(), readyTask(t, r).ID); err != nil {
			t.Fatal(err)
		}
		for _, want := range implementRules {
			if !strings.Contains(agent.prompts[0], want) {
				t.Errorf("implement prompt misses %q", want)
			}
		}
		if strings.Contains(agent.prompts[0], "Unneeded change:") {
			t.Error("implement prompt carries the review's proportion check")
		}
	})
	for _, kind := range []string{"review report", "review fix", "hunt"} {
		t.Run(kind, func(t *testing.T) {
			r, tk, _ := branchedTask(t)
			switch kind {
			case "review report":
				r.Work = Work{Kind: execution.KindReview, BaseBranch: "main"}
			case "review fix":
				r.Work = Work{Kind: execution.KindReview, ReviewMode: execution.ReviewFix, BaseBranch: "main"}
			case "hunt":
				r.Work = Work{Kind: execution.KindHunt, HuntSeed: &execution.HuntSeed{Seed: 3}, BaseBranch: "main"}
			}
			agent := &actor{t: t}
			r.Driver = agent
			if _, err := r.Run(context.Background(), tk.ID); err != nil {
				t.Fatal(err)
			}
			prompt := agent.prompts[0]
			if strings.Contains(prompt, "## Keep the change small") {
				t.Error("a review or hunt is told to keep its change small as if it implemented the task")
			}
			for _, want := range reviewRules {
				if got := strings.Contains(prompt, want); got != strings.HasPrefix(kind, "review") {
					t.Errorf("%s: %q in prompt = %v", kind, want, got)
				}
			}
		})
	}
}

// Proportional changes: diff_stats of an implement run counts the lines the
// task's branch changed against the commit it was cut from — a three-line
// addition is 3 lines, a reformat of a 40-line file is 40 in and 40 out.
func TestDiffStatsCountWhatTheTaskBranchChanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	var original strings.Builder
	for line := 1; line <= 40; line++ {
		fmt.Fprintf(&original, "func step%02d() {}\n", line)
	}
	cases := []struct {
		name string
		edit func(dir string) error
		want execution.DiffStats
	}{
		{"three new lines", func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "README.md"), []byte("demo\none\ntwo\nthree\n"), 0o644)
		}, execution.DiffStats{Files: 1, Insertions: 3}},
		{"reformat everything", func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "steps.go"), []byte(strings.ReplaceAll(original.String(), "func ", "\tfunc ")), 0o644)
		}, execution.DiffStats{Files: 1, Insertions: 40, Deletions: 40}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := repo(t)
			os.WriteFile(filepath.Join(dir, "steps.go"), []byte(original.String()), 0o644)
			gitIn(t, dir, "add", "steps.go")
			gitIn(t, dir, "commit", "-q", "-m", "steps")
			r, err := New(dir)
			if err != nil {
				t.Fatal(err)
			}
			r.Log = func(string) {}
			r.Driver = &actor{t: t, act: func(t *testing.T, job driver.Job) {
				if err := c.edit(job.Dir); err != nil {
					t.Error(err)
				}
			}}
			res, err := r.Run(context.Background(), readyTask(t, r).ID)
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != task.Review || res.Commit == "" || res.DiffStats == nil || *res.DiffStats != c.want {
				t.Fatalf("status %s commit %q diff %+v, want %+v", res.Status, res.Commit, res.DiffStats, c.want)
			}
		})
	}
}
