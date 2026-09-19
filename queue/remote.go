package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNoBundle means the queued task predates Cloud-managed specifications.
var ErrNoBundle = errors.New("queue: run has no work bundle")

// Remote is a client of trilha-cloud's run queue. The contract is small on
// purpose, so another control plane can implement it:
//
//	POST /api/runs/next            ← {worker, project}   → 200 Item | 204 nothing (a claim: it mutates)
//	POST /api/runs/{id}/result     ← Result
//	POST /api/workers/heartbeat    ← {name, project, status}
//
// with `Authorization: Bearer TOKEN` on every call.
type Remote struct {
	BaseURL string
	Token   string
	Worker  string
	Project string
	Capabilities
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
	payload := struct {
		Worker  string `json:"worker"`
		Project string `json:"project"`
		Capabilities
	}{Worker: r.Worker, Project: r.Project, Capabilities: r.normalizedCapabilities()}
	resp, err := r.do(ctx, http.MethodPost, "/api/runs/next", payload)
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
		for _, dependency := range it.DependsOn {
			alias, id, remote := strings.Cut(dependency, ":")
			if !remote {
				continue
			}
			status, err := r.DependencyStatus(ctx, alias, id)
			if err != nil || status != "done" {
				it.Waiting = "waiting:" + dependency
				break
			}
		}
		return it, nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return Item{}, fmt.Errorf("queue: %s: %s", resp.Status, bytes.TrimSpace(b))
}

// DependencyStatus asks the control plane for one task in another project.
func (r Remote) DependencyStatus(ctx context.Context, project, id string) (string, error) {
	path := "/api/projects/" + url.PathEscape(project) + "/tasks/" + url.PathEscape(id) + "?requesting_project=" + url.QueryEscape(r.Project)
	resp, err := r.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("queue: dependency: %s: %s", resp.Status, bytes.TrimSpace(body))
	}
	var result struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.Status == "" {
		return "", errors.New("queue: dependency status is empty")
	}
	return result.Status, nil
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

// Heartbeat tells the control plane this worker is alive and what it is doing.
func (r Remote) Heartbeat(ctx context.Context, status string) error {
	payload := struct {
		Name    string `json:"name"`
		Project string `json:"project"`
		Status  string `json:"status"`
		Capabilities
	}{Name: r.Worker, Project: r.Project, Status: status, Capabilities: r.normalizedCapabilities()}
	resp, err := r.do(ctx, http.MethodPost, "/api/workers/heartbeat", payload)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("queue: heartbeat: %s", resp.Status)
	}
	return nil
}

func (r Remote) normalizedCapabilities() Capabilities {
	capabilities := r.Capabilities
	if capabilities.Capacity <= 0 {
		capabilities.Capacity = 1
	}
	if capabilities.Running < 0 {
		capabilities.Running = 0
	}
	return capabilities
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
