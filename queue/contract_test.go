package queue

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/emersonjoe/trilha-spec/execution"
)

// The execution-v1 fixture is trilha-spec's canonical run, byte for byte
// (make conformance). Reading it into the wire types and writing it back must
// lose nothing the control plane will count: the attempts, the result and, on
// an attempt, the cost under the protocol's names (§10) — provider, tokens_in,
// tokens_out, cost, currency.
func TestExecutionV1FixtureMatchesRemoteResult(t *testing.T) {
	data, err := os.ReadFile("../testdata/contracts/execution-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	type envelope struct {
		ProtocolVersion string    `json:"protocol_version"`
		Attempts        []Attempt `json:"attempts"`
		Result          Result    `json:"result"`
	}
	var run envelope
	if err := json.Unmarshal(data, &run); err != nil {
		t.Fatal(err)
	}
	if run.ProtocolVersion != "trilha.execution/v1" || !run.Result.Passed || run.Result.Status != "review" {
		t.Fatalf("incompatible execution result: %+v", run)
	}
	if len(run.Attempts) != 2 || run.Result.TotalTokens != 4200 || len(run.Result.FilesChanged) != 1 {
		t.Fatalf("execution metadata was lost: %+v", run)
	}
	if run.Attempts[0].ErrorCode != "AI_PROVIDER_TEMPORARY_FAILURE" || run.Attempts[0].FailureClass != "provider_transient" {
		t.Fatalf("attempt 1 lost its failure: %+v", run.Attempts[0])
	}
	cost := run.Attempts[1]
	if cost.Provider != "openai" || cost.TokensIn != 2400 || cost.TokensOut != 600 || cost.Cost != 0.0421 || cost.Currency != "USD" {
		t.Fatalf("attempt 2 lost its cost: %+v", cost)
	}

	// Round trip: what the runner writes is what it read, field for field,
	// and the cost travels under the fixture's own keys.
	encoded, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	var again envelope
	if err := json.Unmarshal(encoded, &again); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(run, again) {
		t.Fatalf("round trip changed the run:\n%+v\n%+v", run, again)
	}
	var wire struct {
		Attempts []map[string]any `json:"attempts"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"provider", "tokens_in", "tokens_out", "cost", "currency"} {
		if _, ok := wire.Attempts[1][key]; !ok {
			t.Errorf("attempt 2 on the wire has no %q: %v", key, wire.Attempts[1])
		}
	}
}

// The protocol 0.4 fixture: a run that stopped to ask. The claim side of it
// (kind, context) decodes into Item and the result side (question, session,
// overview, notes, diff_stats) into Result, and nothing is lost on the way
// back out — this is the shape the worker sends and the cloud stores.
func TestExecutionV1NeedsInputFixtureDecodes(t *testing.T) {
	data, err := os.ReadFile("../testdata/contracts/execution-v1-needs-input.json")
	if err != nil {
		t.Fatal(err)
	}
	var claim Item
	if err := json.Unmarshal(data, &claim); err != nil {
		t.Fatal(err)
	}
	if claim.ID != "run-000007" || claim.TaskID != "TASK-002" || claim.Kind != "implement" {
		t.Fatalf("claim = %+v", claim)
	}
	if claim.Context == nil || claim.Context.Overview == "" || len(claim.Context.Notes) != 1 || claim.Context.Notes[0].Source != "operator" {
		t.Fatalf("context = %+v", claim.Context)
	}
	var run struct {
		Status string `json:"status"`
		Result Result `json:"result"`
	}
	if err := json.Unmarshal(data, &run); err != nil {
		t.Fatal(err)
	}
	result := run.Result
	if run.Status != "needs_input" || result.Status != "needs_input" || result.Passed {
		t.Fatalf("status = %q / %+v", run.Status, result)
	}
	if result.Question == nil || result.Question.Text == "" || result.Question.AskedAt.IsZero() {
		t.Fatalf("question = %+v", result.Question)
	}
	if result.Session == nil || result.Session.Driver != "claude-code" || result.Session.SessionID == "" {
		t.Fatalf("session = %+v", result.Session)
	}
	if result.Overview == "" || len(result.Notes) != 1 || result.Notes[0].Source != "ai" {
		t.Fatalf("memory = %q %+v", result.Overview, result.Notes)
	}
	if result.DiffStats == nil || *result.DiffStats != (execution.DiffStats{Files: 3, Insertions: 42, Deletions: 5}) {
		t.Fatalf("diff_stats = %+v", result.DiffStats)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var again Result
	if err := json.Unmarshal(encoded, &again); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, again) {
		t.Fatalf("round trip changed the result:\n%+v\n%+v", result, again)
	}
	var wire map[string]any
	_ = json.Unmarshal(encoded, &wire)
	for _, key := range []string{"question", "session", "overview", "notes", "diff_stats"} {
		if _, ok := wire[key]; !ok {
			t.Errorf("result on the wire has no %q", key)
		}
	}
}
