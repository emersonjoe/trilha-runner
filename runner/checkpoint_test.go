package runner

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/emersonjoe/trilha-runner/driver"
)

func TestCheckpointPreservesRecoveryStateAndDiff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	store := checkpointStore{path: path}
	recovery := json.RawMessage(`{"turns":3,"files_analyzed":["app/page.go"],"tool_calls":[{"tool":"read_file"}]}`)
	store.save(2, driver.Output{Text: "continue", Meta: map[string]string{"model": "gpt-5"}, Recovery: recovery}, errors.New("connection reset"), nil, "app/page.go | 2 +-")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved checkpoint
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Attempt != 2 || saved.Diff == "" || len(saved.Recovery) == 0 {
		t.Fatalf("checkpoint = %+v", saved)
	}
}
