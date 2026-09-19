package runner

import (
	"fmt"
	"time"

	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-runner/internal/taskcompat"
	"github.com/emersonjoe/trilha-spec/task"
)

func (l *attemptLoop) verify(attemptNumber int, attemptStart time.Time, output driver.Output, logPath string, checkpoints checkpointStore) (bool, string, error) {
	sha, err := l.manager.Commit(l.ctx, l.tree, fmt.Sprintf("%s: %s (attempt %d)", l.item.ID, l.item.Title, attemptNumber))
	if err != nil {
		return false, "", err
	}
	if sha != "" {
		l.result.Commit = sha
		l.runner.logf("committed %s", sha[:12])
	} else {
		l.runner.logf("nothing to commit")
	}
	if err := taskcompat.Move(l.runner.Store, l.item, task.Verify); err != nil {
		return false, "", err
	}
	verification, err := runVerification(l.ctx, l.runner.Layout, l.item, l.dir, l.runner.By, l.checks, l.prefix)
	if err != nil {
		return false, "", err
	}
	l.result.Evidence = append(l.result.Evidence, verification.Evidence...)
	attempt := attemptFromOutput(attemptNumber, "passed", "", attemptStart, output)
	if !verification.Passed {
		attempt.Status = "failed"
		attempt.FailureClass = "checks_failed"
	}
	l.result.Attempts = append(l.result.Attempts, attempt)
	checkpoint, runEvidence, err := l.recordVerificationEvidence(attemptNumber, output, logPath, checkpoints, verification)
	if err != nil {
		return false, checkpoint, err
	}
	l.result.Evidence = append(l.result.Evidence, runEvidence)
	return verification.Passed, checkpoint, nil
}
