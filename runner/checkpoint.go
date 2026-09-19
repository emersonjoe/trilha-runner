package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/emersonjoe/trilha-runner/driver"
	"github.com/emersonjoe/trilha-spec/task"
)

type checkpointStore struct{ path string }

type checkpoint struct {
	Attempt      int                `json:"attempt"`
	At           time.Time          `json:"at"`
	Output       string             `json:"output,omitempty"`
	Meta         map[string]string  `json:"meta,omitempty"`
	Recovery     json.RawMessage    `json:"recovery,omitempty"`
	Diff         string             `json:"diff,omitempty"`
	Error        string             `json:"error,omitempty"`
	Verification *task.Verification `json:"verification,omitempty"`
}

func (s checkpointStore) load() string {
	saved, err := os.ReadFile(s.path)
	if err != nil {
		return ""
	}
	return string(saved)
}

func (s checkpointStore) save(attempt int, output driver.Output, runErr error, verification *task.Verification, diff string) string {
	state := checkpoint{Attempt: attempt, At: time.Now().UTC(), Output: output.Text, Meta: output.Meta, Recovery: output.Recovery, Diff: diff, Verification: verification}
	if runErr != nil {
		state.Error = runErr.Error()
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return ""
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	_ = os.WriteFile(s.path, append(encoded, '\n'), 0o600)
	return string(encoded)
}
