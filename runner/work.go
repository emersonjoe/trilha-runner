package runner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-runner/worktree"
	"github.com/emersonjoe/trilha-spec/execution"
	"github.com/emersonjoe/trilha-spec/spec"
	"github.com/emersonjoe/trilha-spec/task"
)

// Work is what the control plane asked a run for, beyond the task: the
// kind of run, the variant, the review comments to answer, and whether the
// branch comes from a fork. The zero Work is an implement run, as always.
type Work struct {
	Kind execution.Kind
	// ReviewOf is the run whose branch a review run reviews; ReviewMode is
	// report (read-only, the default) or fix.
	ReviewOf   string
	ReviewMode execution.ReviewMode
	// HuntSeed points a hunt at an area and a style.
	HuntSeed *execution.HuntSeed
	// VariantIndex ≥ 1 puts the run on trilha/task-nnn-v<N>.
	VariantGroup string
	VariantIndex int
	// ReviewComments are people's comments a repair run answers.
	ReviewComments []execution.ReviewComment
	// ForeignCode is a branch head from a fork: no secret reaches the agent
	// or the checks, the Docker sandbox is required, and project.verify is
	// read from BaseBranch.
	ForeignCode bool
	// BaseBranch is the project's default branch: the base a review or hunt
	// compares against. Empty: origin's HEAD, else main.
	BaseBranch string
}

// readOnly answers whether the run may change nothing: a hunt, or a review
// in report mode.
func (w Work) readOnly() bool {
	return w.Kind == execution.KindHunt || (w.Kind == execution.KindReview && w.ReviewMode != execution.ReviewFix)
}

func (w Work) review() bool { return w.Kind == execution.KindReview }

// failurePolicy is the failure class of a run refused by the runner's own
// rules: a read-only run that changed something, foreign code without the
// Docker sandbox.
const failurePolicy = "policy"

// errorReadOnly and errorSandboxRequired are the error codes next to it.
const (
	errorReadOnly        = "read_only_violated"
	errorSandboxRequired = "sandbox_required"
)

// target answers where the run works and how it gets its branch. A
// read-only run gets a detached checkout of its own, keyed by the run, so
// parallel hunters never share one; a variant works in TASK-NNN-v<N> on its
// own branch; a fix-mode review or a repair run answering comments needs the
// task's existing branch.
func (r *Runner) target(taskID string) worktree.Target {
	w := r.Work
	branch := worktree.Branch(taskID)
	key := taskID
	if w.VariantIndex > 0 {
		// A review of a variant names the variant's branch: the claim carries
		// the variant's index (the cloud copies it from the run reviewed).
		branch = worktree.VariantBranch(taskID, w.VariantIndex)
		key = fmt.Sprintf("%s-v%d", taskID, w.VariantIndex)
	}
	switch {
	case w.readOnly():
		key = taskID + "-" + string(w.Kind)
		if execution.ValidRunID(r.RunID) {
			key += "-" + r.RunID
		}
		return worktree.Target{Task: taskID, Key: key, Branch: branch, Mode: worktree.Detached}
	case w.review() || len(w.ReviewComments) > 0:
		return worktree.Target{Task: taskID, Key: key, Branch: branch, Mode: worktree.Existing}
	case w.VariantIndex > 0:
		return worktree.Target{Task: taskID, Key: key, Branch: branch, Mode: worktree.Variant}
	}
	return worktree.Target{Task: taskID, Key: key, Branch: branch, Mode: worktree.Own}
}

// variantMemory keeps from the task's memory only what people wrote: a
// variant must not read what a sibling variant's agent noted.
func variantMemory(memory *execution.TaskContext) *execution.TaskContext {
	if memory == nil {
		return nil
	}
	kept := &execution.TaskContext{}
	for _, note := range memory.Notes {
		if note.Source != "ai" {
			kept.Notes = append(kept.Notes, note)
		}
	}
	return kept
}

// baseVerify reads project.verify as the default branch has it, for a run on
// foreign code: a fork's branch never chooses the commands the worker runs.
func baseVerify(ctx context.Context, layout spec.Layout, manager worktree.Manager, base string) ([]string, error) {
	ref := manager.BaseRef(ctx, base)
	if ref == "" {
		return nil, errors.New("foreign code: no default branch to read project.verify from")
	}
	rel, err := filepath.Rel(layout.Root, layout.Project())
	if err != nil {
		return nil, err
	}
	content, err := manager.Show(ctx, ref, rel)
	if err != nil {
		// No project file on the default branch: no project checks.
		return nil, nil
	}
	project, err := spec.ParseProject(content)
	if err != nil {
		return nil, fmt.Errorf("foreign code: project.md on %s: %w", ref, err)
	}
	return project.Verify, nil
}

// scope answers what a review or hunt prompt says about the branch.
func scopeOf(ctx context.Context, manager worktree.Manager, tree worktree.Worktree, base string) inspectionScope {
	scope := inspectionScope{Branch: tree.Branch, Head: tree.Head}
	if scope.Head == "" {
		scope.Head, _ = manager.Head(ctx, tree)
	}
	scope.BaseRef = manager.BaseRef(ctx, base)
	if scope.BaseRef == "" {
		return scope
	}
	if mergeBase, err := manager.MergeBase(ctx, tree, scope.BaseRef); err == nil {
		scope.MergeBase = mergeBase
		scope.Changed, _ = manager.ChangedFiles(ctx, tree, mergeBase)
	}
	return scope
}

// finishReadOnly ends a read-only run: the worktree must be clean, HEAD must
// be the commit it started at and the branch under review must not have
// moved. Anything else fails the run with failure_class policy, and what the
// agent reported is discarded by the caller.
func (l *attemptLoop) finishReadOnly(attemptNumber int, attemptStart time.Time, output driver.Output) (*Result, error) {
	var problems []string
	changes, err := l.manager.Changes(l.ctx, l.tree)
	if err != nil {
		problems = append(problems, "cannot read the worktree's status: "+err.Error())
	}
	if len(changes) > 0 {
		shown := changes
		if len(shown) > 10 {
			shown = append(shown[:10:10], fmt.Sprintf("… and %d more", len(changes)-10))
		}
		problems = append(problems, "the worktree changed: "+strings.Join(shown, ", "))
	}
	if head, err := l.manager.Head(l.ctx, l.tree); err != nil || head != l.tree.Head {
		problems = append(problems, fmt.Sprintf("HEAD moved from %s to %s", short(l.tree.Head), short(head)))
	}
	if tip, err := l.manager.SHA(l.ctx, l.tree.Ref); err != nil || tip != l.tree.Head {
		problems = append(problems, fmt.Sprintf("%s moved from %s to %s", l.tree.Branch, short(l.tree.Head), short(tip)))
	}
	meta := map[string]string{"driver": l.driver.Name(), "kind": string(l.runner.Work.Kind), "attempt": strconv.Itoa(attemptNumber), "branch": l.tree.Branch, "commit": l.tree.Head}
	for key, value := range output.Meta {
		if _, taken := meta[key]; !taken {
			meta[key] = value
		}
	}
	attempt := attemptFromOutput(attemptNumber, "passed", "", attemptStart, output)
	l.result.Commit = l.tree.Head
	if len(problems) > 0 {
		cause := errors.New("policy: a read-only run changed the repository: " + strings.Join(problems, "; "))
		attempt.Status, attempt.FailureClass, attempt.ErrorCode = "failed", failurePolicy, errorReadOnly
		l.result.Attempts = append(l.result.Attempts, attempt)
		meta["stage"], meta["failure_class"], meta["error_code"] = "policy", failurePolicy, errorReadOnly
		evidence, _, recordErr := task.Record(l.runner.Layout, task.Evidence{Task: l.item.ID, Kind: "run", By: l.runner.By, Note: cause.Error() + " — its findings are discarded", Meta: meta})
		if recordErr == nil {
			l.result.Evidence = append(l.result.Evidence, evidence)
		}
		l.result.Status, l.result.Passed = task.Failed, false
		l.result.FailureClass, l.result.ErrorCode = failurePolicy, errorReadOnly
		l.result.Elapsed = time.Since(l.started)
		l.runner.logf("%v", cause)
		l.runner.progress(Event{Phase: "integrating", Attempt: attemptNumber, MaxAttempts: l.maxAttempts, AttemptStatus: "failed", FailureClass: failurePolicy, ErrorCode: errorReadOnly})
		return l.result, cause
	}
	l.result.Attempts = append(l.result.Attempts, attempt)
	meta["stage"] = "read_only_verified"
	evidence, _, err := task.Record(l.runner.Layout, task.Evidence{Task: l.item.ID, Kind: "run", By: l.runner.By, Passed: true, Note: fmt.Sprintf("%s run on %s at %s: the worktree is clean and HEAD did not move", l.runner.Work.Kind, l.tree.Branch, short(l.tree.Head)), Meta: meta})
	if err != nil {
		return l.fail("evidence", err)
	}
	l.result.Evidence = append(l.result.Evidence, evidence)
	l.result.Status, l.result.Passed = task.Done, true
	l.result.Elapsed = time.Since(l.started)
	l.runner.progress(Event{Phase: "integrating", Attempt: attemptNumber, MaxAttempts: l.maxAttempts, AttemptStatus: "passed"})
	l.runner.logf("%s %s run verified read-only on %s", l.item.ID, l.runner.Work.Kind, l.tree.Branch)
	return l.result, nil
}

// attachReport puts what the agent reported into the result: a review's
// verdict and findings, a hunt's findings (with its area and style), the
// replies to review comments and the artifacts. A read-only run refused by
// policy reports nothing of it.
func (r *Runner) attachReport(res *Result, session *agentSession) {
	if res == nil {
		return
	}
	findings, verdict, replies, artifacts := session.reportOf()
	res.ReviewReplies = replies
	if res.FailureClass == failurePolicy && r.Work.readOnly() {
		if len(findings) > 0 || len(artifacts) > 0 {
			r.logf("discarded %d findings and %d artifacts of a run refused by policy", len(findings), len(artifacts))
		}
		res.ReviewReplies = nil
		return
	}
	res.Artifacts = artifacts
	switch r.Work.Kind {
	case execution.KindReview:
		if verdict == "" {
			verdict = "pass"
			if len(findings) > 0 {
				verdict = "changes"
			}
		}
		res.Review = &execution.ReviewVerdict{Verdict: verdict, Findings: findings}
	case execution.KindHunt:
		seed := execution.HuntSeed{}
		if r.Work.HuntSeed != nil {
			seed = *r.Work.HuntSeed
		}
		if seed.Area == "" || seed.Style == "" {
			derived := execution.NewHuntSeed(seed.Seed)
			seed.Area, seed.Style = derived.Area, derived.Style
		}
		for index := range findings {
			findings[index].Area, findings[index].Style = seed.Area, seed.Style
		}
		res.Findings = findings
	default:
		if len(findings) > 0 {
			r.logf("an implement run reported %d findings; only review and hunt runs report findings", len(findings))
		}
	}
}
