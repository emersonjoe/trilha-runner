package runner

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/emersonjoe/trilha-spec/spec"
	"github.com/emersonjoe/trilha-spec/task"
)

// evalLine is the part of a metric line the protocol does not carry: the
// manifest path the runner hashes into `dataset.sha256`. The metric itself is
// read by task.ParseMetricLine.
type evalLine struct {
	Dataset *struct {
		Manifest string `json:"manifest"`
	} `json:"dataset,omitempty"`
}

func runVerification(ctx context.Context, layout spec.Layout, item *task.Task, dir, by string, checks, prefix []string) (*task.Verification, error) {
	verification := &task.Verification{Task: item.ID, Passed: true}
	if len(checks) == 0 {
		verification.Passed = false
		evidence, path, err := task.Record(layout, task.Evidence{Task: item.ID, Kind: "note", By: by, Dir: dir, Note: "no checks to run: add `checks:` to the task or `verify:` to project.md"})
		if err != nil {
			return nil, err
		}
		verification.Evidence = append(verification.Evidence, evidence)
		verification.Paths = append(verification.Paths, path)
		return verification, nil
	}
	for _, command := range checks {
		check := runCheck(ctx, item.ID, command, dir, by, prefix)
		recorded, path, err := task.Record(layout, check)
		if err != nil {
			return nil, err
		}
		verification.Evidence = append(verification.Evidence, recorded)
		verification.Paths = append(verification.Paths, path)
		if !recorded.Passed {
			verification.Passed = false
		}
		evaluations, err := parseEvaluations(dir, item.ID, by, check.Output)
		if err != nil {
			return nil, err
		}
		for _, evaluation := range evaluations {
			recorded, path, err := task.Record(layout, evaluation)
			if err != nil {
				return nil, err
			}
			verification.Evidence = append(verification.Evidence, recorded)
			verification.Paths = append(verification.Paths, path)
			if !recorded.Passed {
				verification.Passed = false
			}
		}
	}
	return verification, nil
}

func runCheck(ctx context.Context, taskID, command, dir, by string, prefix []string) task.Evidence {
	evidence := task.Evidence{Task: taskID, Kind: "check", By: by, Command: command, Dir: dir}
	args := task.SplitCommand(command)
	if len(args) == 0 {
		evidence.ExitCode = -1
		evidence.Note = "empty command"
		return evidence
	}
	checkContext, cancel := context.WithTimeout(ctx, task.CheckTimeout)
	defer cancel()
	program, commandArgs := args[0], args[1:]
	if len(prefix) > 0 {
		program = prefix[0]
		commandArgs = append(append([]string(nil), prefix[1:]...), args...)
	}
	process := exec.CommandContext(checkContext, program, commandArgs...)
	process.Dir = dir
	var output bytes.Buffer
	process.Stdout = &output
	process.Stderr = &output
	err := process.Run()
	evidence.Output = output.String()
	switch {
	case err == nil:
		evidence.Passed = true
		evidence.ExitCode = 0
	case checkContext.Err() != nil:
		evidence.ExitCode = -1
		evidence.Note = "timed out after " + task.CheckTimeout.String()
	default:
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			evidence.ExitCode = exitError.ExitCode()
		} else {
			evidence.ExitCode = -1
			evidence.Note = err.Error()
		}
	}
	return evidence
}

func parseEvaluations(root, taskID, by, output string) ([]task.Evidence, error) {
	var evidence []task.Evidence
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		metric, reason, ok := task.ParseMetricLine(line)
		if !ok || !task.ValidMetric(metric.Metric) {
			continue
		}
		var extra evalLine
		_ = json.Unmarshal([]byte(line), &extra)
		if metric.Dataset != nil && strings.TrimSpace(metric.Dataset.ID) == "" {
			metric.Dataset = nil
		}
		record := task.Eval(taskID, by, metric)
		note := reason
		switch {
		case reason != "" && !task.ValidComparator(metric.Comparator):
			// A gate whose comparator the protocol does not know cannot pass.
			record.Comparator, record.Threshold, record.Passed = "", nil, false
		case metric.Threshold != nil:
			note = fmt.Sprintf("%s %s %s", formatFloat(metric.Value), metric.Comparator, formatFloat(*metric.Threshold))
		}
		if extra.Dataset != nil && extra.Dataset.Manifest != "" {
			hash, err := hashManifest(root, extra.Dataset.Manifest)
			if err != nil {
				record.Passed = false
				note = "dataset manifest: " + err.Error()
			} else if record.Dataset != nil {
				record.Dataset.SHA256 = hash
			}
		}
		record.Note = note
		record.Meta = evalMeta(record, extra)
		evidence = append(evidence, record)
	}
	return evidence, scanner.Err()
}

// evalMeta mirrors the eval fields as strings for the readers that still
// consume Meta (result.json, the cloud client, the run summary).
func evalMeta(e task.Evidence, extra evalLine) map[string]string {
	meta := map[string]string{"metric": e.Metric, "value": formatFloat(*e.Value)}
	if e.Threshold != nil {
		meta["threshold"] = formatFloat(*e.Threshold)
	}
	if e.Comparator != "" {
		meta["comparator"] = e.Comparator
	}
	if e.Unit != "" {
		meta["unit"] = e.Unit
	}
	if e.Dataset != nil {
		meta["dataset_id"] = e.Dataset.ID
		if e.Dataset.SHA256 != "" {
			meta["dataset_sha256"] = e.Dataset.SHA256
		}
	}
	if extra.Dataset != nil && extra.Dataset.Manifest != "" {
		meta["dataset_manifest"] = extra.Dataset.Manifest
	}
	return meta
}

func formatFloat(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func hashManifest(root, relative string) (string, error) {
	if strings.TrimSpace(relative) == "" {
		return "", errors.New("path is required")
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path leaves worktree")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:]), nil
}
