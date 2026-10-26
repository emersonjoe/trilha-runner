package queue

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/emersonjoe/trilha-spec/execution"
)

// DV-05b..DV-08, DV-13c: a claim carries what the run is for under the
// names of execution.Run, plus the queue's foreign_code flag; the result
// carries the verdict, the findings, the replies and the accepted artifacts
// under the names of execution.Outcome.
func TestClaimAndResultCarryReviewHuntVariantAndArtifacts(t *testing.T) {
	claim := `{"id":"run-000010","project":"app","task_id":"TASK-001","kind":"hunt","review_of":"run-000009","review_mode":"report",
"hunt_seed":{"seed":7,"area":"error paths","style":"trace follower"},"variant_group":"vg-1","variant_index":2,
"review_comments":[{"id":"c1","path":"main.go","line_start":3,"line_end":4,"side":"RIGHT","body":"off by one","author":"reviewer"}],
"foreign_code":true}`
	var item Item
	if err := json.Unmarshal([]byte(claim), &item); err != nil {
		t.Fatal(err)
	}
	if item.Kind != execution.KindHunt || item.ReviewOf != "run-000009" || item.ReviewMode != execution.ReviewReport || item.HuntSeed == nil || item.HuntSeed.Area != "error paths" ||
		item.VariantGroup != "vg-1" || item.VariantIndex != 2 || len(item.ReviewComments) != 1 || item.ReviewComments[0].LineEnd != 4 || !item.ForeignCode {
		t.Fatalf("claim lost fields: %+v", item)
	}
	result := Result{Status: "done", Passed: true,
		Review:        &execution.ReviewVerdict{Verdict: "changes", Findings: []execution.Finding{{Severity: "high", Path: "main.go", LineStart: 3, LineEnd: 4, Title: "off by one"}}},
		Findings:      []execution.Finding{{Severity: "medium", Path: "a.go", LineStart: 1, LineEnd: 1, Title: "t"}},
		ReviewReplies: []execution.ReviewReply{{ID: "c1", Reply: "fixed"}},
		Artifacts:     []execution.Artifact{{Name: "report.md", ContentType: "text/markdown", Size: 3, SHA256: "ab"}}}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	json.Unmarshal(encoded, &wire)
	for _, key := range []string{"review", "findings", "review_replies", "artifacts"} {
		if wire[key] == nil {
			t.Errorf("result has no %q: %s", key, encoded)
		}
	}
	if review := wire["review"].(map[string]any); review["verdict"] != "changes" {
		t.Errorf("review = %v", review)
	}
}

// B5: an artifact goes up as raw bytes with its type and digest, with the
// worker's key; 403 ARTIFACTS_DISABLED is its own error, every other refusal
// a StatusError that is not retryable.
func TestUploadArtifactSendsBytesTypeAndDigest(t *testing.T) {
	var got struct {
		body, contentType, digest string
	}
	remote := cloud(t, map[string]http.HandlerFunc{
		"PUT /api/runs/run-000010/artifacts/report.md": func(w http.ResponseWriter, r *http.Request) {
			data, _ := io.ReadAll(r.Body)
			got.body, got.contentType, got.digest = string(data), r.Header.Get("Content-Type"), r.Header.Get("X-Artifact-SHA256")
			w.WriteHeader(http.StatusCreated)
		},
		"PUT /api/runs/run-000010/artifacts/off.md":   reply(http.StatusForbidden, `{"code":"ARTIFACTS_DISABLED","error":"artifacts are off"}`),
		"PUT /api/runs/run-000010/artifacts/big.png":  reply(http.StatusRequestEntityTooLarge, `{"code":"TOO_LARGE"}`),
		"PUT /api/runs/run-000010/artifacts/bad.html": reply(http.StatusUnprocessableEntity, `{"code":"INVALID"}`),
		"PUT /api/runs/run-000010/artifacts/role.md":  reply(http.StatusForbidden, `{"code":"FORBIDDEN"}`),
	})
	item := Item{ID: "run-000010"}
	if err := remote.UploadArtifact(context.Background(), item, "report.md", "text/markdown", "abc123", []byte("# Report\n")); err != nil {
		t.Fatal(err)
	}
	if got.body != "# Report\n" || got.contentType != "text/markdown" || got.digest != "abc123" {
		t.Fatalf("upload = %+v", got)
	}
	if err := remote.UploadArtifact(context.Background(), item, "off.md", "text/markdown", "x", []byte("x")); !errors.Is(err, ErrArtifactsDisabled) {
		t.Fatalf("disabled = %v", err)
	}
	for name, code := range map[string]int{"big.png": 413, "bad.html": 422, "role.md": 403} {
		err := remote.UploadArtifact(context.Background(), item, name, "image/png", "x", []byte("x"))
		var status *StatusError
		if !errors.As(err, &status) || status.Code != code || status.Retryable() || errors.Is(err, ErrArtifactsDisabled) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
