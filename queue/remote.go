package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Remote is a client of trilha-cloud's run queue. The contract is small on
// purpose, so another control plane can implement it:
//
//	GET  /api/runs/next?worker=NAME&project=P   → 200 Item | 204 nothing
//	POST /api/runs/{id}/result                  ← Result
//	POST /api/workers/heartbeat                 ← {name, project, status}
//
// with `Authorization: Bearer TOKEN` on every call.
type Remote struct {
	BaseURL string
	Token   string
	Worker  string
	Project string
	// HTTPClient defaults to one with a 30-second timeout.
	HTTPClient *http.Client
}

func (r Remote) client() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (r Remote) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var buf io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		buf = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.BaseURL+path, buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return r.client().Do(req)
}

// Next asks the control plane for work.
func (r Remote) Next(ctx context.Context) (Item, error) {
	resp, err := r.do(ctx, http.MethodGet, "/api/runs/next?worker="+r.Worker+"&project="+r.Project, nil)
	if err != nil {
		return Item{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return Item{}, ErrEmpty
	case http.StatusOK:
		var it Item
		if err := json.NewDecoder(resp.Body).Decode(&it); err != nil {
			return Item{}, err
		}
		return it, nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return Item{}, fmt.Errorf("queue: %s: %s", resp.Status, bytes.TrimSpace(b))
}

// Done reports the result of an item.
func (r Remote) Done(ctx context.Context, item Item, res Result) error {
	resp, err := r.do(ctx, http.MethodPost, "/api/runs/"+item.ID+"/result", res)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("queue: %s: %s", resp.Status, bytes.TrimSpace(b))
	}
	return nil
}

// Heartbeat tells the control plane this worker is alive and what it is doing.
func (r Remote) Heartbeat(ctx context.Context, status string) error {
	resp, err := r.do(ctx, http.MethodPost, "/api/workers/heartbeat", map[string]string{"name": r.Worker, "project": r.Project, "status": status})
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("queue: heartbeat: %s", resp.Status)
	}
	return nil
}
