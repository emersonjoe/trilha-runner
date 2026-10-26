package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-spec/execution"
	"github.com/emersonjoe/trilha-spec/task"
)

func (l *attemptLoop) captureOutput(logPath string, output driver.Output) error {
	l.result.Output = output.Text
	l.result.AgentReport = output.Text
	l.result.Model = output.Meta["model"]
	if id := output.Meta["session_id"]; driver.ValidSessionID(id) {
		l.result.Session = &execution.Session{Driver: l.driver.Name(), SessionID: id}
	}
	l.result.InputTokens += fieldInt(output.Meta["input_tokens"], 0)
	l.result.OutputTokens += fieldInt(output.Meta["output_tokens"], 0)
	tokens := fieldInt(output.Meta["tokens"], 0)
	l.result.TotalTokens += tokens
	l.result.EstimatedCost += outputCost(output, tokens)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(logPath, []byte(output.Text), 0o644)
}

// recordExecutionFailure leaves the run record of an attempt that did not
// get to verification: the stage, the class and, when there is one, the
// error_code and any extra detail (the missing tool, the check that needs it).
func (l *attemptLoop) recordExecutionFailure(attemptNumber int, stage string, executionErr error, failureClass, errorCode string, extra map[string]string) error {
	meta := map[string]string{
		"driver":        l.driver.Name(),
		"stage":         stage,
		"attempt":       strconv.Itoa(attemptNumber),
		"failure_class": failureClass,
	}
	if errorCode != "" {
		meta["error_code"] = errorCode
	}
	for key, value := range extra {
		meta[key] = value
	}
	evidence, _, err := task.Record(l.runner.Layout, task.Evidence{
		Task: l.item.ID,
		Kind: "run",
		By:   l.runner.By,
		Note: fmt.Sprintf("%s attempt %d: %v", stage, attemptNumber, executionErr),
		Meta: meta,
	})
	if err != nil {
		return err
	}
	l.result.Evidence = append(l.result.Evidence, evidence)
	return nil
}

func (l *attemptLoop) recordVerificationEvidence(attemptNumber int, output driver.Output, logPath string, checkpoints checkpointStore, verification *task.Verification) (string, task.Evidence, error) {
	meta := map[string]string{
		"driver":  l.driver.Name(),
		"branch":  l.tree.Branch,
		"commit":  l.result.Commit,
		"elapsed": time.Since(l.started).Round(time.Millisecond).String(),
		"attempt": strconv.Itoa(attemptNumber),
	}
	for key, value := range output.Meta {
		meta[key] = value
	}
	if check := missingTool(verification); check != "" {
		meta["failure_class"], meta["error_code"] = failureEnvironment, errorMissingTool
		meta["check"] = check
	}
	stat, _ := l.manager.DiffStat(l.ctx, l.tree)
	if stat != "" {
		meta["diffstat"] = stat
		l.result.DiffSummary = stat
	}
	checkpoint := checkpoints.save(attemptNumber, output, nil, verification, stat)
	note := strings.TrimSpace(firstLines(output.Text, 20))
	var metrics []string
	for _, evidence := range verification.Evidence {
		if evidence.Kind == "eval" {
			metrics = append(metrics, evidence.Meta["metric"]+"="+evidence.Meta["value"])
		}
	}
	if len(metrics) > 0 {
		meta["metrics"] = strings.Join(metrics, ",")
		if note != "" {
			note += "\n"
		}
		note += "metrics: " + strings.Join(metrics, ", ")
	}
	evidence, _, err := task.Record(l.runner.Layout, task.Evidence{
		Task:   l.item.ID,
		Kind:   "run",
		By:     l.runner.By,
		Passed: verification.Passed,
		Note:   note,
		Files:  []string{logPath, checkpoints.path},
		Meta:   meta,
	})
	return checkpoint, evidence, err
}

func firstLines(value string, count int) string {
	lines := strings.Split(value, "\n")
	if len(lines) > count {
		lines = lines[:count]
	}
	return strings.Join(lines, "\n")
}
