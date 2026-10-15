package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/emersonjoe/trilha-runner/queue"
)

// A bundle's task may depend on a task of another project. The worker asks
// the control plane and runs only when every such dependency is done.
func TestRemoteDependencyBlocker(t *testing.T) {
	statuses := map[string]string{"/api/projects/trilha/tasks/TASK-001": "done", "/api/projects/trilha/tasks/TASK-002": "running"}
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if r.URL.Query().Get("requesting_project") != "app" {
			http.Error(w, "missing requesting_project", http.StatusBadRequest)
			return
		}
		status, ok := statuses[r.URL.Path]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": status})
	}))
	defer server.Close()
	remote := queue.Remote{BaseURL: server.URL, Token: "tok", Project: "app"}
	ctx := context.Background()

	cases := []struct {
		name         string
		dependencies []string
		want         string
		calls        int
	}{
		{"none", nil, "", 0},
		{"local only is not asked", []string{"TASK-009"}, "", 0},
		{"remote done", []string{"TASK-009", "trilha:TASK-001"}, "", 1},
		{"stops at the first not done", []string{"trilha:TASK-002", "trilha:TASK-001"}, "waiting:trilha:TASK-002", 1},
		{"remote unknown", []string{"spec:TASK-404"}, "waiting:spec:TASK-404", 1},
	}
	for _, c := range cases {
		asked = nil
		if got := remoteDependencyBlocker(ctx, remote, c.dependencies); got != c.want || len(asked) != c.calls {
			t.Errorf("%s: got %q after %v, want %q after %d calls", c.name, got, asked, c.want, c.calls)
		}
	}

	// The control plane being down is a wait, not a failed run.
	server.Close()
	if got := remoteDependencyBlocker(ctx, remote, []string{"trilha:TASK-001"}); got != "waiting:trilha:TASK-001" {
		t.Fatalf("cloud down: %q", got)
	}
}
