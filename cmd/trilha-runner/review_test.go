package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-runner/internal/agentlink"
	"github.com/emersonjoe/trilha-runner/queue"
	"github.com/emersonjoe/trilha-runner/runner"
	"github.com/emersonjoe/trilha-spec/execution"
)

// B2/B4/B5: the agent commands of review, hunt and repair runs parse what
// the agent typed and refuse, before anything is sent, what the control
// plane could never take.
func TestReportCommandsParseAndRefuse(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "report.md")
	os.WriteFile(report, []byte("# Report\n"), 0o644)
	ok := []struct {
		cmd  string
		args []string
		want agentlink.Message
	}{
		{"finding", []string{"--severity", "high", "--path", "./calc.go", "--lines", "5-7", "--title", "off by one", "--why", "len", "--repro", "Last([]int{1})"},
			agentlink.Message{Type: agentlink.TypeFinding, Finding: &execution.Finding{Severity: "high", Path: "calc.go", LineStart: 5, LineEnd: 7, Title: "off by one", Why: "len", Repro: "Last([]int{1})"}}},
		{"finding", []string{"--severity", "MEDIUM", "--path", "a.go", "--lines", "3", "--title", "t"},
			agentlink.Message{Type: agentlink.TypeFinding, Finding: &execution.Finding{Severity: "medium", Path: "a.go", LineStart: 3, LineEnd: 3, Title: "t"}}},
		{"verdict", []string{"changes"}, agentlink.Message{Type: agentlink.TypeVerdict, Text: "changes"}},
		{"reply", []string{"c1", "fixed", "the", "bound"}, agentlink.Message{Type: agentlink.TypeReply, ID: "c1", Text: "fixed the bound"}},
		{"artifact", []string{report, "--name", "hunt.md"}, agentlink.Message{Type: agentlink.TypeArtifact, Path: report, Name: "hunt.md"}},
		{"artifact", []string{report}, agentlink.Message{Type: agentlink.TypeArtifact, Path: report, Name: "report.md"}},
	}
	for _, c := range ok {
		got, err := agentMessage(context.Background(), c.cmd, c.args, nil)
		if err != nil {
			t.Fatalf("%s %v: %v", c.cmd, c.args, err)
		}
		if got.Type != c.want.Type || got.Text != c.want.Text || got.ID != c.want.ID || got.Path != c.want.Path || got.Name != c.want.Name ||
			(c.want.Finding != nil && (got.Finding == nil || !reflect.DeepEqual(*got.Finding, *c.want.Finding))) {
			t.Errorf("%s %v = %+v (finding %+v)", c.cmd, c.args, got, got.Finding)
		}
	}
	refused := []struct {
		cmd  string
		args []string
	}{
		{"finding", []string{"--severity", "low", "--path", "a.go", "--lines", "1", "--title", "t"}},
		{"finding", []string{"--severity", "high", "--path", "/etc/passwd", "--lines", "1", "--title", "t"}},
		{"finding", []string{"--severity", "high", "--path", "a.go", "--lines", "9-2", "--title", "t"}},
		{"finding", []string{"--severity", "high", "--path", "a.go", "--lines", "x", "--title", "t"}},
		{"finding", []string{"--severity", "high", "--path", "a.go", "--lines", "1", "--title", strings.Repeat("t", execution.MaxFindingTitle+1)}},
		{"verdict", []string{"maybe"}},
		{"reply", []string{"c1"}},
		{"artifact", []string{filepath.Join(dir, "missing.md")}},
		{"artifact", []string{dir}},
		{"artifact", nil},
	}
	for _, c := range refused {
		if _, err := agentMessage(context.Background(), c.cmd, c.args, nil); err == nil {
			t.Errorf("%s %v was accepted", c.cmd, c.args)
		}
	}
}

// B5: artifacts go up before the result; only the accepted ones are listed.
// A refused one (413) is dropped and not retried; a project with artifacts
// off gets none, at the first answer.
func TestUploadArtifactsListsOnlyWhatTheCloudAccepted(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) runner.Artifact {
		path := filepath.Join(dir, name)
		os.WriteFile(path, []byte("# "+name), 0o600)
		return runner.Artifact{Artifact: execution.Artifact{Name: name, ContentType: "text/markdown", Size: int64(len(name) + 2), SHA256: strings.Repeat("a", 64)}, Path: path}
	}
	calls := map[string]int{}
	off := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		calls[name]++
		switch {
		case off:
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"code":"ARTIFACTS_DISABLED"}`)
		case name == "big.md":
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		default:
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer server.Close()
	remote := queue.Remote{BaseURL: server.URL, Token: "tok"}
	item := queue.Item{ID: "run-000004"}
	var logged []string
	log := func(line string) { logged = append(logged, line) }
	accepted := uploadArtifacts(context.Background(), remote, item, []runner.Artifact{write("a.md"), write("big.md"), write("b.md")}, log)
	if len(accepted) != 2 || accepted[0].Name != "a.md" || accepted[1].Name != "b.md" || calls["big.md"] != 1 {
		t.Fatalf("accepted %+v, calls %v", accepted, calls)
	}
	off, logged = true, nil
	if accepted := uploadArtifacts(context.Background(), remote, item, []runner.Artifact{write("c.md"), write("d.md")}, log); accepted != nil || calls["d.md"] != 0 || len(logged) != 1 {
		t.Fatalf("artifacts off: accepted %+v, calls %v, log %v", accepted, calls, logged)
	}
}

// DV-07 and B5 end to end: a hunt claim runs an agent that reports through
// `trilha-runner finding` and `trilha-runner artifact` from its PATH; the
// cloud receives the report bytes before a done result with the finding
// (stamped with the seed's area and style) and the accepted artifact; the
// branch under review does not move. With artifacts off, the result lists
// none and the findings still arrive.
func TestHuntRunReportsFindingsAndUploadsArtifacts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	useBinary(t)
	dir := gitRepo(t)
	sh(t, dir, "git", "switch", "-q", "-c", "trilha/task-001")
	os.WriteFile(filepath.Join(dir, "calc.go"), []byte("package calc\n\n// Last answers the last element.\nfunc Last(values []int) int {\n\treturn values[len(values)]\n}\n"), 0o644)
	sh(t, dir, "git", "add", "-A")
	sh(t, dir, "git", "commit", "-q", "-m", "TASK-001: Last")
	tip := strings.TrimSpace(sh(t, dir, "git", "rev-parse", "HEAD"))
	sh(t, dir, "git", "switch", "-q", "main")
	agent := filepath.Join(t.TempDir(), "hunter.sh")
	os.WriteFile(agent, []byte(`#!/bin/sh
cat > /dev/null
trilha-runner finding --severity high --path calc.go --lines 5-5 --title "Last indexes one past the end" --why "len(values) is out of range" --repro "Last([]int{1}) panics"
trilha-runner finding --severity low --path calc.go --lines 5 --title "not a severity"
echo "# Hunt report" > "$TRILHA_ARTIFACT_DIR/report.md"
trilha-runner artifact "$TRILHA_ARTIFACT_DIR/report.md"
`), 0o700)
	for _, off := range []bool{false, true} {
		cloud := &fakeCloud{artifactsOff: off, items: []queue.Item{{ID: "run-000002", Project: "demo", TaskID: "TASK-001", Kind: execution.KindHunt, HuntSeed: &execution.HuntSeed{Seed: 1}}}}
		cloud.work(t, dir, "exec", "sh "+agent)
		cloud.mutex.Lock()
		result := cloud.results["run-000002"]
		uploaded := cloud.artifacts["run-000002/artifacts/report.md"]
		cloud.mutex.Unlock()
		if result.Status != "done" || !result.Passed || result.Commit != tip || result.Branch != "trilha/task-001" {
			t.Fatalf("artifacts off %v: result = %+v", off, result)
		}
		seed := execution.NewHuntSeed(1)
		if len(result.Findings) != 1 || result.Findings[0].Path != "calc.go" || result.Findings[0].Area != seed.Area || result.Findings[0].Style != seed.Style {
			t.Fatalf("findings = %+v", result.Findings)
		}
		if off {
			if len(result.Artifacts) != 0 || uploaded != nil {
				t.Fatalf("artifacts off: %+v %q", result.Artifacts, uploaded)
			}
		} else if len(result.Artifacts) != 1 || result.Artifacts[0].Name != "report.md" || string(uploaded) != "# Hunt report\n" {
			t.Fatalf("artifacts = %+v, uploaded %q", result.Artifacts, uploaded)
		}
		if got := strings.TrimSpace(sh(t, dir, "git", "rev-parse", "trilha/task-001")); got != tip {
			t.Fatal("the hunt moved the branch")
		}
	}
}
