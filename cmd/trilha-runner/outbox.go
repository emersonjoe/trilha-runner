package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/emersonjoe/trilha-runner/queue"
)

// The result outbox (RM-012). A run's result is written to
// .trilha/runs/<run-id>/result.json in the checkout that ran it before the
// cloud is asked to take it, and removed once the cloud has it. A worker that
// dies between the two — or a cloud that is down — leaves the file behind, and
// the next start delivers it. The control plane keeps one result per run and
// attempt, so a result delivered twice is still one result.

type outboxEntry struct {
	RunID  string       `json:"run_id"`
	TaskID string       `json:"task_id,omitempty"`
	Result queue.Result `json:"result"`
}

// safeRunID is a run id that can name a directory.
var safeRunID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

const (
	outboxFile         = "result.json"
	outboxRejected     = "result.rejected.json"
	defaultRetryBase   = time.Second
	maxDeliveryBackoff = 30 * time.Second
)

func outboxPath(root, runID string) string {
	if root == "" || !safeRunID.MatchString(runID) {
		return ""
	}
	return filepath.Join(root, ".trilha", "runs", runID, outboxFile)
}

// writeOutbox puts the entry on disk; "" when there is nowhere to put it.
func writeOutbox(root string, entry outboxEntry) (string, error) {
	path := outboxPath(root, entry.RunID)
	if path == "" {
		return "", nil
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return "", err
	}
	return path, os.Rename(temporary, path)
}

// pendingOutbox answers every result left in the outboxes under roots, each
// root a checkout, oldest first.
func pendingOutbox(roots []string) []string {
	var found []string
	seen := map[string]bool{}
	for _, root := range roots {
		if root == "" {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(root, ".trilha", "runs", "*", outboxFile))
		for _, match := range matches {
			if !seen[match] {
				seen[match] = true
				found = append(found, match)
			}
		}
	}
	sort.Slice(found, func(i, j int) bool {
		a, _ := os.Stat(found[i])
		b, _ := os.Stat(found[j])
		if a == nil || b == nil {
			return found[i] < found[j]
		}
		return a.ModTime().Before(b.ModTime())
	})
	return found
}

// outboxRoots are the checkouts a worker runs in: its own and one per
// project under --workspace-root.
func outboxRoots(baseRoot, workspaceRoot string) []string {
	roots := []string{baseRoot}
	if workspaceRoot != "" {
		entries, _ := os.ReadDir(workspaceRoot)
		for _, entry := range entries {
			if entry.IsDir() {
				roots = append(roots, filepath.Join(workspaceRoot, entry.Name()))
			}
		}
	}
	return roots
}

// deliver posts a result until the cloud has it. 2xx and 409 (the cloud
// already holds a result for this run) clear the outbox; any other 4xx is a
// refusal that will not change, so the entry is set aside as
// result.rejected.json and the worker carries on; anything else is retried
// with exponential backoff until ctx ends, leaving the entry for the next
// start.
func deliver(ctx context.Context, remote queue.Remote, entry outboxEntry, path string, retryBase time.Duration) error {
	if retryBase <= 0 {
		retryBase = defaultRetryBase
	}
	delay := retryBase
	for {
		err := remote.Done(ctx, queue.Item{ID: entry.RunID, Project: remote.Project, TaskID: entry.TaskID}, entry.Result)
		var status *queue.StatusError
		switch {
		case err == nil, errors.As(err, &status) && status.Code == http.StatusConflict:
			if path != "" {
				_ = os.Remove(path)
			}
			return nil
		case errors.As(err, &status) && !status.Retryable():
			fmt.Fprintf(os.Stderr, "· result of %s refused by the cloud: %v\n", entry.RunID, err)
			if path != "" {
				_ = os.Rename(path, filepath.Join(filepath.Dir(path), outboxRejected))
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fmt.Fprintf(os.Stderr, "· result of %s not delivered (%v); retrying in %s\n", entry.RunID, err, delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay *= 2; delay > maxDeliveryBackoff {
			delay = maxDeliveryBackoff
		}
	}
}

// flushOutbox delivers every pending result under roots.
func flushOutbox(ctx context.Context, remote queue.Remote, roots []string, retryBase time.Duration) error {
	for _, path := range pendingOutbox(roots) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var entry outboxEntry
		if err := json.Unmarshal(data, &entry); err != nil || !safeRunID.MatchString(entry.RunID) {
			_ = os.Rename(path, filepath.Join(filepath.Dir(path), outboxRejected))
			continue
		}
		if err := deliver(ctx, remote, entry, path, retryBase); err != nil {
			return err
		}
	}
	return nil
}
