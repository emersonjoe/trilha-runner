package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/emersonjoe/trilha-spec/task"
)

// Metric is one line a check printed to say what it measured. A quality
// harness — triage accuracy on a labelled set, a translation score, a p95
// latency, accessibility violations — proves its result with a number
// against a threshold, and a number buried in the output of a command is
// invisible to the reviewer and to the control plane. So the harness prints
// one line of JSON per number and the runner turns it into evidence:
//
//	{"metric":"triage_top1","value":0.87,"threshold":0.85,"comparator":">="}
//
// The optional `dataset` says what was measured against. The runner hashes
// the manifest the harness names, never the data itself: a golden set can be
// large, private, or both, and its manifest is what has to be pinned.
//
//	{"metric":"triage_top1","value":0.87,"threshold":0.85,"comparator":">=",
//	 "dataset":{"id":"triage-v3","manifest":"eval/golden/manifest.json"}}
type Metric struct {
	Metric string  `json:"metric"`
	Value  float64 `json:"value"`
	// Threshold is what Value is compared against with Comparator.
	Threshold  float64  `json:"threshold"`
	Comparator string   `json:"comparator"`
	Unit       string   `json:"unit,omitempty"`
	Dataset    *Dataset `json:"dataset,omitempty"`
}

// Dataset names what a metric was measured against.
type Dataset struct {
	ID string `json:"id,omitempty"`
	// Manifest is a path, relative to the worktree, of the file that lists
	// the data. The runner hashes it and records the hash.
	Manifest string `json:"manifest,omitempty"`
}

// Comparators are the comparisons a metric line may ask for.
var Comparators = []string{">=", "<=", "==", "!=", ">", "<"}

// OK answers whether the measured value satisfies the threshold.
func (m Metric) OK() bool {
	switch m.Comparator {
	case ">=":
		return m.Value >= m.Threshold
	case "<=":
		return m.Value <= m.Threshold
	case ">":
		return m.Value > m.Threshold
	case "<":
		return m.Value < m.Threshold
	case "==":
		return m.Value == m.Threshold
	case "!=":
		return m.Value != m.Threshold
	}
	return false
}

// String renders the comparison the way the evidence reads it.
func (m Metric) String() string {
	return fmt.Sprintf("%s=%s %s %s", m.Metric, num(m.Value), m.Comparator, num(m.Threshold))
}

func num(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

// validComparator keeps an unrelated line of JSON from being read as a
// measurement: a metric line is one that names a metric and a comparison.
func validComparator(c string) bool {
	for _, k := range Comparators {
		if k == c {
			return true
		}
	}
	return false
}

// ScanMetrics answers the metric lines in a command's output, in the order
// they were printed. A line that is not a complete JSON object, or that does
// not name both a metric and one of the comparators, is ordinary output and
// is ignored — a check may print whatever else it likes.
func ScanMetrics(output string) []Metric {
	var out []Metric
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
			continue
		}
		var m Metric
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if strings.TrimSpace(m.Metric) == "" || !validComparator(m.Comparator) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// hashManifest answers the sha256 of the dataset manifest named by the
// metric, resolved inside dir. A manifest that is missing or outside the
// worktree answers the reason instead, so the evidence says why it could
// not be pinned rather than staying silent.
func hashManifest(dir string, d *Dataset) string {
	if d == nil || strings.TrimSpace(d.Manifest) == "" {
		return ""
	}
	p := filepath.Join(dir, filepath.FromSlash(d.Manifest))
	if rel, err := filepath.Rel(dir, p); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "outside the worktree"
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "unreadable: " + err.Error()
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// recordEvals turns the metric lines a verification's checks printed into
// `eval` evidence, one record per line, beside the `check` record that
// carried them. A metric that misses its threshold fails the verification
// even when the command exited 0: the exit code says the harness ran, the
// metric says whether the result is good enough.
func (r *Runner) recordEvals(v *task.Verification, id, dir string) ([]task.Evidence, error) {
	var out []task.Evidence
	for _, check := range v.Evidence {
		if check.Kind != "check" {
			continue
		}
		for _, m := range ScanMetrics(check.Output) {
			meta := map[string]string{
				"metric":     m.Metric,
				"value":      num(m.Value),
				"threshold":  num(m.Threshold),
				"comparator": m.Comparator,
			}
			if m.Unit != "" {
				meta["unit"] = m.Unit
			}
			if m.Dataset != nil {
				if m.Dataset.ID != "" {
					meta["dataset"] = m.Dataset.ID
				}
				if m.Dataset.Manifest != "" {
					meta["dataset_manifest"] = m.Dataset.Manifest
					meta["dataset_sha256"] = hashManifest(dir, m.Dataset)
				}
			}
			note := m.String()
			if !m.OK() {
				note += " — below threshold"
			}
			e, _, err := task.Record(r.Layout, task.Evidence{
				Task: id, Kind: "eval", By: r.By, Command: check.Command, Dir: dir,
				Passed: m.OK(), Note: note, Meta: meta,
			})
			if err != nil {
				return nil, err
			}
			out = append(out, e)
			r.logf("eval %s (passed=%v)", m.String(), m.OK())
		}
	}
	return out, nil
}

// summarize renders the metrics for the run record's meta, so the summary a
// reviewer reads first already carries the numbers.
func summarize(evals []task.Evidence) string {
	parts := make([]string, 0, len(evals))
	for _, e := range evals {
		mark := "fail"
		if e.Passed {
			mark = "pass"
		}
		parts = append(parts, fmt.Sprintf("%s=%s %s %s %s", e.Meta["metric"], e.Meta["value"], e.Meta["comparator"], e.Meta["threshold"], mark))
	}
	return strings.Join(parts, "; ")
}
