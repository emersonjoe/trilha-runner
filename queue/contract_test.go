package queue

import (
	"encoding/json"
	"os"
	"testing"
)

func TestExecutionV1FixtureMatchesRemoteResult(t *testing.T) {
	data, err := os.ReadFile("../testdata/contracts/execution-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		ProtocolVersion string    `json:"protocol_version"`
		Attempts        []Attempt `json:"attempts"`
		Result          Result    `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.ProtocolVersion != "trilha.execution/v1" || !envelope.Result.Passed || envelope.Result.Status != "review" {
		t.Fatalf("incompatible execution result: %+v", envelope)
	}
	if len(envelope.Attempts) != 2 || envelope.Result.TotalTokens != 4200 || len(envelope.Result.FilesChanged) != 1 {
		t.Fatalf("execution metadata was lost: %+v", envelope)
	}
}
