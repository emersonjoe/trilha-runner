package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrNoBundle means the queued task predates Cloud-managed specifications.
var ErrNoBundle = errors.New("queue: run has no work bundle")

// Remote is a client of trilha-cloud's run queue. The contract is small on
// purpose, so another control plane can implement it:
//
//	POST /api/runs/next            ← {worker, project, …capabilities}  → 200 Item | 204 nothing (a claim: it mutates)
//	POST /api/runs/{id}/result     ← Result
//	POST /api/workers/heartbeat    ← {name, project, status, …capabilities}
//
// with `Authorization: Bearer TOKEN` on every call.
//
// Both the claim and the heartbeat carry the worker's Capabilities —
// labels, capacity, how many runs are in flight, the runner version and the
// drivers it has — so the control plane can route a run that needs Postgres
// for its checks to the host that has Docker, and keep a project whose data
// must not leave the country on a worker in that region. A run the control
// plane still hands over with requirements this worker does not meet is
// refused here with ErrUnmet rather than executed.
type Remote struct {
	BaseURL string
	Token   string
	Worker  string
	Project string
	// Capabilities travel with every claim and heartbeat.
	Capabilities Capabilities
	// HTTPClient defaults to one with a 30-second timeout.
	HTTPClient *http.Client
}

// capacity is the declared capacity, never below one.
func (r Remote) capacity() int {
	if r.Capabilities.Capacity < 1 {
		return 1
	}
	return r.Capabilities.Capacity
}

// claim is the body of a claim or a heartbeat: who is asking, for what
// project, and what it can do.
type claim struct {
	Worker  string `json:"worker,omitempty"`
	Name    string `json:"name,omitempty"`
	Project string `json:"project"`
	Status  string `json:"status,omitempty"`
	Capabilities
}

func (r Remote) capabilities(running int) Capabilities {
	c := r.Capabilities
	c.Capacity = r.capacity()
	c.Running = running
	return c
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

// Next asks the control plane for work. It is a claim, so it carries the
// same capabilities as the heartbeat: the control plane filters on them.
func (r Remote) Next(ctx context.Context) (Item, error) { return r.NextRunning(ctx, 0) }

// NextRunning is Next for a worker with more than one slot: running says how
// many runs are already in flight, so the control plane sees the real load.
func (r Remote) NextRunning(ctx context.Context, running int) (Item, error) {
	resp, err := r.do(ctx, http.MethodPost, "/api/runs/next", claim{Worker: r.Worker, Project: r.Project, Capabilities: r.capabilities(running)})
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
		if ok, missing := r.Capabilities.Meets(it.Requires); !ok {
			return it, fmt.Errorf("%w: %s", ErrUnmet, strings.Join(missing, ", "))
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

// Bundle returns the protocol materialization payload for a claimed run.
func (r Remote) Bundle(ctx context.Context, item Item) (Bundle, error) {
	resp, err := r.do(ctx, http.MethodGet, "/api/runs/"+item.ID+"/bundle", nil)
	if err != nil {
		return Bundle{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Bundle{}, ErrNoBundle
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Bundle{}, fmt.Errorf("queue: bundle: %s: %s", resp.Status, bytes.TrimSpace(body))
	}
	var bundle Bundle
	if err := json.NewDecoder(resp.Body).Decode(&bundle); err != nil {
		return Bundle{}, err
	}
	if bundle.Version != 1 || bundle.RunID != item.ID || bundle.Project != item.Project || bundle.Task.ID != item.TaskID {
		return Bundle{}, errors.New("queue: invalid work bundle")
	}
	return bundle, nil
}

// Heartbeat tells the control plane this worker is alive, what it is doing,
// and what it can do.
func (r Remote) Heartbeat(ctx context.Context, status string) error {
	return r.HeartbeatRunning(ctx, status, 0)
}

// HeartbeatRunning is Heartbeat with the number of runs in flight.
func (r Remote) HeartbeatRunning(ctx context.Context, status string, running int) error {
	resp, err := r.do(ctx, http.MethodPost, "/api/workers/heartbeat", claim{Name: r.Worker, Project: r.Project, Status: status, Capabilities: r.capabilities(running)})
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("queue: heartbeat: %s", resp.Status)
	}
	return nil
}

// NextDeployment claims the oldest queued deployment for this runner's project.
func (r Remote) NextDeployment(ctx context.Context) (DeploymentWork, error) {
	resp, err := r.do(ctx, http.MethodPost, "/api/deployments/next", map[string]string{"worker": r.Worker, "project": r.Project})
	if err != nil {
		return DeploymentWork{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return DeploymentWork{}, ErrEmpty
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return DeploymentWork{}, fmt.Errorf("queue: deployment: %s: %s", resp.Status, bytes.TrimSpace(body))
	}
	var work DeploymentWork
	if err := json.NewDecoder(resp.Body).Decode(&work); err != nil {
		return DeploymentWork{}, err
	}
	if work.Deployment.ID == "" || work.Deployment.Project != r.Project || work.Deployment.Profile == "" {
		return DeploymentWork{}, errors.New("queue: invalid deployment work")
	}
	return work, nil
}

// DoneDeployment reports a deployment result without sending secrets back.
func (r Remote) DoneDeployment(ctx context.Context, work DeploymentWork, result DeploymentResult) error {
	result.Worker = r.Worker
	result.Project = r.Project
	resp, err := r.do(ctx, http.MethodPost, "/api/deployments/"+work.Deployment.ID+"/result", result)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("queue: deployment result: %s: %s", resp.Status, bytes.TrimSpace(body))
	}
	return nil
}
