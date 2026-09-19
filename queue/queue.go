// Package queue is where the next task comes from. Local reads the graph in
// .trilha/ — the protocol is its own queue for one machine — and Remote asks
// trilha-cloud, which is how a fleet shares work. Both answer the same Item,
// so the runner does not know which one it is talking to.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emersonjoe/trilha-runner/internal/taskcompat"
	"github.com/emersonjoe/trilha-spec/task"
)

// Item is one unit of work handed to a runner.
type Item struct {
	// ID is the queue's own id; "" for the local queue.
	ID string `json:"id,omitempty"`
	// Project names the project on the remote side.
	Project   string   `json:"project,omitempty"`
	TaskID    string   `json:"task_id"`
	DependsOn []string `json:"depends_on,omitempty"`
	AI        *AI      `json:"ai,omitempty"`
	// Waiting carries an unresolved cross-repository dependency when a
	// control plane returns the item for observability without claiming it.
	Waiting string `json:"waiting,omitempty"`
}

// AI is the project-scoped provider configuration disclosed only with a
// claimed item. Credential must never be persisted or returned to the cloud.
type AI struct {
	Provider     string   `json:"provider,omitempty"`
	BaseURL      string   `json:"base_url,omitempty"`
	Model        string   `json:"model,omitempty"`
	Credential   string   `json:"credential,omitempty"`
	AllowedHosts []string `json:"allowed_hosts,omitempty"`
}

// Capabilities are sent on heartbeat and claim so the control plane can
// route work without guessing what a worker can execute.
type Capabilities struct {
	Labels         []string          `json:"labels,omitempty"`
	Capacity       int               `json:"capacity"`
	Running        int               `json:"running"`
	RunnerVersion  string            `json:"runner_version,omitempty"`
	DriverVersions map[string]string `json:"driver_versions,omitempty"`
}

// Bundle is the versioned Trilha Spec context returned by the control plane.
// The runner materializes it in its local checkout; source code never travels
// in this payload.
type Bundle struct {
	Version       int           `json:"version"`
	RunID         string        `json:"run_id"`
	Project       string        `json:"project"`
	Repository    string        `json:"repository,omitempty"`
	DefaultBranch string        `json:"default_branch"`
	Specification Specification `json:"specification"`
	Round         Round         `json:"round"`
	Task          WorkTask      `json:"task"`
}

type Specification struct {
	ID          string `json:"id"`
	ProtocolID  string `json:"protocol_id,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Version     int    `json:"version"`
}

type Round struct {
	ID     string  `json:"id"`
	Number int     `json:"number"`
	Goal   string  `json:"goal,omitempty"`
	Stages []Stage `json:"stages"`
}

type Stage struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Position int        `json:"position"`
	Tasks    []WorkTask `json:"tasks"`
}

type WorkTask struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	Agent      string   `json:"agent,omitempty"`
	DependsOn  []string `json:"depends_on,omitempty"`
	Acceptance []string `json:"acceptance,omitempty"`
	Checks     []string `json:"checks,omitempty"`
}

// Result is what a runner reports back.
type Result struct {
	ProtocolVersion  string          `json:"protocol_version,omitempty"`
	Passed           bool            `json:"passed"`
	Status           string          `json:"status"`
	Branch           string          `json:"branch,omitempty"`
	Commit           string          `json:"commit,omitempty"`
	Evidence         []task.Evidence `json:"evidence,omitempty"`
	Log              string          `json:"log,omitempty"`
	Error            string          `json:"error,omitempty"`
	ErrorCode        string          `json:"error_code,omitempty"`
	TechnicalDetails string          `json:"technical_details,omitempty"`
	Attempt          int             `json:"attempt,omitempty"`
	RetryOf          string          `json:"retry_of,omitempty"`
	FailureClass     string          `json:"failure_class,omitempty"`
	RepairReason     string          `json:"repair_reason,omitempty"`
	Model            string          `json:"model,omitempty"`
	InputTokens      int             `json:"input_tokens,omitempty"`
	OutputTokens     int             `json:"output_tokens,omitempty"`
	TotalTokens      int             `json:"total_tokens,omitempty"`
	EstimatedCost    float64         `json:"estimated_cost,omitempty"`
	DurationMS       int64           `json:"duration_ms,omitempty"`
	FilesChanged     []string        `json:"files_changed,omitempty"`
	DiffSummary      string          `json:"diff_summary,omitempty"`
	AgentReport      string          `json:"agent_report,omitempty"`
	Attempts         []Attempt       `json:"attempts,omitempty"`
}

func (result Result) MarshalJSON() ([]byte, error) {
	type alias Result
	encoded, err := json.Marshal(alias(result))
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		return nil, err
	}
	items, _ := object["evidence"].([]any)
	for index, raw := range result.Evidence {
		if raw.Kind != "eval" || index >= len(items) {
			continue
		}
		record, _ := items[index].(map[string]any)
		record["metric"] = raw.Meta["metric"]
		if value, err := strconv.ParseFloat(raw.Meta["value"], 64); err == nil {
			record["value"] = value
		}
		if threshold, err := strconv.ParseFloat(raw.Meta["threshold"], 64); err == nil {
			record["threshold"] = threshold
		}
		record["comparator"] = raw.Meta["comparator"]
		if unit := raw.Meta["unit"]; unit != "" {
			record["unit"] = unit
		}
		if id := raw.Meta["dataset_id"]; id != "" {
			record["dataset"] = map[string]string{"id": id, "sha256": raw.Meta["dataset_sha256"]}
		}
	}
	return json.Marshal(object)
}

type Attempt struct {
	Number        int       `json:"number"`
	Status        string    `json:"status"`
	FailureClass  string    `json:"failure_class,omitempty"`
	ErrorCode     string    `json:"error_code,omitempty"`
	Message       string    `json:"message,omitempty"`
	RepairReason  string    `json:"repair_reason,omitempty"`
	Model         string    `json:"model,omitempty"`
	TotalTokens   int       `json:"total_tokens,omitempty"`
	EstimatedCost float64   `json:"estimated_cost,omitempty"`
	Started       time.Time `json:"started"`
	Finished      time.Time `json:"finished,omitempty"`
}

// DeploymentWork is a claimed operation. Secrets are disclosed only on the
// claim response to a project-bound runner and must never be logged.
type DeploymentWork struct {
	Deployment Deployment        `json:"deployment"`
	Secrets    map[string]string `json:"secrets,omitempty"`
}

type Deployment struct {
	ID               string `json:"id"`
	Project          string `json:"project"`
	Environment      string `json:"environment"`
	Profile          string `json:"profile"`
	Action           string `json:"action"`
	Revision         string `json:"revision"`
	PreviousRevision string `json:"previous_revision,omitempty"`
}

type DeploymentResult struct {
	Worker  string `json:"worker"`
	Project string `json:"project"`
	Passed  bool   `json:"passed"`
	Log     string `json:"log,omitempty"`
	Health  string `json:"health,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ErrEmpty is a queue with nothing to hand out right now.
var ErrEmpty = errors.New("queue: nothing to run")

type BlockedError struct {
	Task   string
	Reason string
}

func (e *BlockedError) Error() string { return fmt.Sprintf("%s blocked: %s", e.Task, e.Reason) }

// Queue hands out work and takes back results.
type Queue interface {
	Next(ctx context.Context) (Item, error)
	Done(ctx context.Context, item Item, res Result) error
}

// Local is the dependency graph of the store: the first ready task with
// every dependency done.
type Local struct {
	Store *task.Store
	// Repositories maps dependency aliases to sibling Trilha checkouts.
	Repositories map[string]string
}

// Next answers the first executable task.
func (l Local) Next(ctx context.Context) (Item, error) {
	tasks, err := taskcompat.List(l.Store)
	if err != nil {
		return Item{}, err
	}
	local := make(map[string]*task.Task, len(tasks))
	var firstBlocked *BlockedError
	for _, candidate := range tasks {
		local[candidate.ID] = candidate
	}
	for _, candidate := range tasks {
		if candidate.Status != task.Ready {
			continue
		}
		blocked, err := l.blocker(candidate, local)
		if err != nil {
			return Item{}, err
		}
		if blocked == "" {
			return Item{TaskID: candidate.ID}, nil
		}
		if strings.HasPrefix(blocked, "waiting:") && firstBlocked == nil {
			firstBlocked = &BlockedError{Task: candidate.ID, Reason: blocked}
		}
	}
	if firstBlocked != nil {
		return Item{}, firstBlocked
	}
	return Item{}, ErrEmpty
}

// Blocker answers the first dependency preventing a task from running. A
// remote dependency is reported as waiting:<alias>:TASK-NNN.
func (l Local) Blocker(candidate *task.Task) (string, error) {
	tasks, err := taskcompat.List(l.Store)
	if err != nil {
		return "", err
	}
	local := make(map[string]*task.Task, len(tasks))
	for _, current := range tasks {
		local[current.ID] = current
	}
	return l.blocker(candidate, local)
}

func (l Local) blocker(candidate *task.Task, local map[string]*task.Task) (string, error) {
	for _, dependency := range candidate.DependsOn {
		alias, id, remote := strings.Cut(dependency, ":")
		if !remote {
			if current := local[dependency]; current == nil || current.Status != task.Done {
				return dependency, nil
			}
			continue
		}
		root := l.Repositories[alias]
		if root == "" {
			return "waiting:" + dependency, nil
		}
		store, err := task.Open(root)
		if err != nil {
			return "waiting:" + dependency, nil
		}
		current, err := store.Get(id)
		if err != nil || current.Status != task.Done {
			return "waiting:" + dependency, nil
		}
	}
	return "", nil
}

// ParseRepository parses alias=path for the repeatable --repo flag.
func ParseRepository(value string) (string, string, error) {
	alias, path, ok := strings.Cut(value, "=")
	alias, path = strings.TrimSpace(alias), strings.TrimSpace(path)
	if !ok || alias == "" || path == "" || strings.Contains(alias, ":") {
		return "", "", fmt.Errorf("repository must be alias=path, got %q", value)
	}
	return alias, path, nil
}

// Done is a no-op: the runner already moved the task and wrote the evidence.
func (Local) Done(ctx context.Context, item Item, res Result) error { return nil }
