package queue

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
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
