package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersonjoe/trilha-spec/agent"
	"github.com/emersonjoe/trilha-spec/task"
)

// Spec is the sandbox an agent manifest declares. A product whose checks
// need services — an API test suite that needs Postgres, a worker that needs
// a gateway — says so here, and the runner is what starts them:
//
//	sandbox: {"image":"golang:1.22","services":[{"name":"postgres",
//	          "image":"pgvector/pgvector:pg16","env":{"POSTGRES_PASSWORD":"x"},
//	          "ready":["pg_isready","-U","postgres"]}]}
//
// The manifest may also name a JSON file instead, relative to the repository
// root: `sandbox: .trilha/sandboxes/api.json`. Front matter carries scalars
// and lists of scalars, so a nested declaration is JSON either way.
//
// What the manifest may not say is how much of the machine it gets: the
// limits below are the runner's, because a manifest that can raise its own
// limits is not a limit.
type Spec struct {
	// Image is what the agent and the checks run in.
	Image string `json:"image"`
	// Services are started first, on a network of their own, and reachable
	// from the agent's container by name.
	Services []Service `json:"services,omitempty"`
	// Env is added to the agent's container.
	Env map[string]string `json:"env,omitempty"`
}

// Service is one container the checks need.
type Service struct {
	Name  string            `json:"name"`
	Image string            `json:"image"`
	Env   map[string]string `json:"env,omitempty"`
	// Ready is argv run inside the service until it succeeds: the runner
	// does not start the agent before the services answer.
	Ready []string `json:"ready,omitempty"`
	// ReadyTimeoutSeconds bounds that wait (default 60).
	ReadyTimeoutSeconds int `json:"ready_timeout_seconds,omitempty"`
}

// SpecField is the agent manifest key that declares a sandbox.
const SpecField = "sandbox"

// ErrNoSpec is a manifest that declares no sandbox.
var ErrNoSpec = errors.New("sandbox: the agent manifest declares no `sandbox:`")

// FromAgent reads the sandbox an agent manifest declares. root resolves a
// manifest that names a file instead of writing the JSON inline.
func FromAgent(man *agent.Agent, root string) (*Spec, error) {
	if man == nil {
		return nil, ErrNoSpec
	}
	value := strings.TrimSpace(man.Fields.Get(SpecField))
	if value == "" {
		return nil, ErrNoSpec
	}
	data := []byte(value)
	if !strings.HasPrefix(value, "{") {
		path := filepath.Join(root, filepath.FromSlash(value))
		read, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("sandbox: %s: %w", value, err)
		}
		data = read
	}
	var spec Spec
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return nil, fmt.Errorf("sandbox: %s: %w", SpecField, err)
	}
	if err := spec.validate(); err != nil {
		return nil, err
	}
	return &spec, nil
}

func (s Spec) validate() error {
	if strings.TrimSpace(s.Image) == "" {
		return errors.New("sandbox: image is required")
	}
	seen := map[string]bool{}
	for _, service := range s.Services {
		if !validName(service.Name) {
			return fmt.Errorf("sandbox: service name %q must be lowercase words joined by -", service.Name)
		}
		if seen[service.Name] {
			return fmt.Errorf("sandbox: two services are called %q", service.Name)
		}
		seen[service.Name] = true
		if strings.TrimSpace(service.Image) == "" {
			return fmt.Errorf("sandbox: service %q has no image", service.Name)
		}
	}
	return nil
}

func validName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(name)-1:
		default:
			return false
		}
	}
	return true
}

// Limits are the runner's, not the manifest's. A sandbox exists to bound
// what an agent can do to the machine it runs on; a declaration that can
// raise its own ceiling bounds nothing.
type Limits struct {
	CPUs   string
	Memory string
	PIDs   int
}

// DefaultLimits are what a run gets unless the operator changed them for the
// whole worker.
var DefaultLimits = Limits{CPUs: "2", Memory: "4g", PIDs: 512}

// WorkDir is where the worktree is mounted inside the containers.
const WorkDir = "/workspace"

// KeepAlive is what the agent's container runs so it stays up for the
// commands the runner will send it. `sleep infinity` reads better but it is a
// GNU coreutils spelling: busybox — Alpine, and most small base images —
// refuses it and the container exits before the first command. So the seconds
// are spelled out, the largest a 32-bit sleep accepts, about 68 years.
var KeepAlive = []string{"sleep", "2147483647"}

// Docker runs the agent and the checks in a container, with the services the
// manifest declared on a network of their own.
//
// The worktree is the only writable path that survives the run: the root
// filesystem is read-only and /tmp is a tmpfs that dies with the container.
// Nothing here mounts the Docker socket — a sandbox that can talk to the
// daemon is not a sandbox — so this exists only where the worker runs, on a
// host the operator labelled for it, never in a control plane.
type Docker struct {
	// Binary is the client to call; "docker" by default.
	Binary string
	// Limits override DefaultLimits.
	Limits Limits
	// Log receives progress lines; nil discards them.
	Log func(string)
}

// Name is "docker".
func (Docker) Name() string { return "docker" }

func (d Docker) binary() string {
	if d.Binary != "" {
		return d.Binary
	}
	return "docker"
}

func (d Docker) limits() Limits {
	l := d.Limits
	if l.CPUs == "" {
		l.CPUs = DefaultLimits.CPUs
	}
	if l.Memory == "" {
		l.Memory = DefaultLimits.Memory
	}
	if l.PIDs <= 0 {
		l.PIDs = DefaultLimits.PIDs
	}
	return l
}

func (d Docker) logf(format string, args ...any) {
	if d.Log != nil {
		d.Log(fmt.Sprintf(format, args...))
	}
}

// Available answers whether this machine can run the sandbox at all.
func (d Docker) Available(ctx context.Context) error {
	if _, err := exec.LookPath(d.binary()); err != nil {
		return fmt.Errorf("sandbox: %s is not installed: %w", d.binary(), err)
	}
	if _, err := d.run(ctx, "info", "--format", "{{.ServerVersion}}"); err != nil {
		return fmt.Errorf("sandbox: %s is installed but the daemon is not reachable: %w", d.binary(), err)
	}
	return nil
}

// run calls the client and answers what it printed.
func (d Docker) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, d.binary(), args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	text := strings.TrimSpace(out.String())
	if err != nil {
		return text, fmt.Errorf("%s %s: %w: %s", d.binary(), strings.Join(args, " "), err, text)
	}
	return text, nil
}

// slug is the name this run's objects share.
func slug(taskID string) string {
	return "trilha-" + strings.ToLower(strings.ReplaceAll(taskID, "_", "-"))
}

// Prepare starts the services, then the container the agent and the checks
// run in. Whatever it created is removed by the release it answers, even
// when it fails partway through.
func (d Docker) Prepare(ctx context.Context, req Request) (Env, func() error, error) {
	if req.Spec == nil {
		return Env{}, func() error { return nil }, ErrNoSpec
	}
	if !task.ValidID(req.Task) {
		return Env{}, func() error { return nil }, fmt.Errorf("sandbox: %q is not a task id", req.Task)
	}
	if err := req.Spec.validate(); err != nil {
		return Env{}, func() error { return nil }, err
	}
	name := slug(req.Task)
	created := &teardown{docker: d}
	release := created.release

	if _, err := d.run(ctx, "network", "create", name); err != nil {
		return Env{}, release, err
	}
	created.network = name

	for _, service := range req.Spec.Services {
		container := name + "-" + service.Name
		args := []string{"run", "--detach", "--name", container,
			"--network", name, "--network-alias", service.Name,
			"--security-opt", "no-new-privileges", "--pids-limit", strconv.Itoa(d.limits().PIDs),
			"--memory", d.limits().Memory, "--cpus", d.limits().CPUs}
		for _, key := range sortedKeys(service.Env) {
			args = append(args, "--env", key+"="+service.Env[key])
		}
		args = append(args, service.Image)
		if _, err := d.run(ctx, args...); err != nil {
			return Env{}, release, err
		}
		created.containers = append(created.containers, container)
		if err := d.ensureUp(ctx, container, "service "+service.Name); err != nil {
			return Env{}, release, err
		}
		d.logf("sandbox: service %s started", service.Name)
		if err := d.wait(ctx, container, service); err != nil {
			return Env{}, release, err
		}
	}

	// The run's environment goes in through a file, never on a command line:
	// a credential in argv is a credential in `ps`.
	envFile, err := writeEnvFile(req)
	if err != nil {
		return Env{}, release, err
	}
	created.files = append(created.files, envFile)

	args := []string{"run", "--detach", "--name", name, "--network", name,
		"--volume", req.Dir + ":" + WorkDir, "--workdir", WorkDir,
		// The worktree is the only writable path that survives: the root is
		// read-only and /tmp dies with the container.
		"--read-only", "--tmpfs", "/tmp:rw,size=256m",
		"--security-opt", "no-new-privileges", "--cap-drop", "ALL",
		"--pids-limit", strconv.Itoa(d.limits().PIDs),
		"--memory", d.limits().Memory, "--cpus", d.limits().CPUs,
		"--env-file", envFile,
		"--entrypoint", KeepAlive[0]}
	// The agent runs as the user that owns the worktree, and two reasons point
	// the same way. With every capability dropped, root inside the container
	// no longer bypasses file permission checks — CAP_DAC_OVERRIDE is gone —
	// so a worktree owned by the operator would not be writable by a root
	// agent at all. And an agent that is not root is a smaller blast radius
	// for the one path it can write. A system with no uid answers -1 and the
	// flag is left out.
	if uid := os.Getuid(); uid >= 0 {
		args = append(args, "--user", strconv.Itoa(uid)+":"+strconv.Itoa(os.Getgid()))
	}
	args = append(args, req.Spec.Image)
	args = append(args, KeepAlive[1:]...)
	if _, err := d.run(ctx, args...); err != nil {
		return Env{}, release, err
	}
	created.containers = append(created.containers, name)
	if err := d.ensureUp(ctx, name, "the sandbox of "+req.Task); err != nil {
		return Env{}, release, err
	}
	d.logf("sandbox: %s running %s", name, req.Spec.Image)

	return Env{
		Dir:     req.Dir,
		WorkDir: WorkDir,
		Prefix:  []string{d.binary(), "exec", "--workdir", WorkDir, name},
	}, release, nil
}

// wait polls a service's readiness command until it succeeds. A service with
// no readiness command is taken at its word. A service that has exited is
// reported at once, with its own output: waiting a minute for a container
// that is already gone tells the operator nothing.
func (d Docker) wait(parent context.Context, container string, service Service) error {
	if len(service.Ready) == 0 {
		return nil
	}
	timeout := time.Duration(service.ReadyTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var last error
	for {
		args := append([]string{"exec", container}, service.Ready...)
		_, err := d.run(ctx, args...)
		if err == nil {
			d.logf("sandbox: service %s ready", service.Name)
			return nil
		}
		last = err
		// It is not coming up if it is no longer running.
		if upErr := d.ensureUp(parent, container, "service "+service.Name); upErr != nil {
			return upErr
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("sandbox: service %s was not ready in %s: %w", service.Name, timeout, last)
		case <-time.After(readyInterval):
		}
	}
}

// up answers whether a container is still running.
func (d Docker) up(ctx context.Context, container string) (bool, error) {
	out, err := d.run(ctx, "inspect", "--format", "{{.State.Running}}", container)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "true", nil
}

// ensureUp fails with the container's own output when it did not stay up — an
// image whose `sleep` refuses the argument, a service whose entrypoint exited
// — instead of leaving the operator to infer it from a later `docker exec`.
func (d Docker) ensureUp(ctx context.Context, container, what string) error {
	running, err := d.up(ctx, container)
	if err != nil {
		return err
	}
	if running {
		return nil
	}
	message := fmt.Sprintf("sandbox: %s exited instead of staying up (container %s)", what, container)
	if logs, _ := d.run(ctx, "logs", "--tail", "20", container); logs != "" {
		message += ": " + logs
	}
	return errors.New(message)
}

// readyInterval is how often a service is asked whether it is up.
var readyInterval = time.Second

// teardown removes what Prepare created, in reverse, whatever went wrong.
type teardown struct {
	docker     Docker
	network    string
	containers []string
	files      []string
	done       bool
}

func (t *teardown) release() error {
	if t.done {
		return nil
	}
	t.done = true
	// The context of the run may be cancelled by then; cleanup gets its own.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var errs []string
	for i := len(t.containers) - 1; i >= 0; i-- {
		if _, err := t.docker.run(ctx, "rm", "--force", "--volumes", t.containers[i]); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if t.network != "" {
		if _, err := t.docker.run(ctx, "network", "rm", t.network); err != nil {
			errs = append(errs, err.Error())
		}
	}
	for _, file := range t.files {
		os.Remove(file)
	}
	if len(errs) > 0 {
		return errors.New("sandbox: teardown: " + strings.Join(errs, "; "))
	}
	return nil
}

// writeEnvFile puts the run's environment where only this user can read it.
func writeEnvFile(req Request) (string, error) {
	file, err := os.CreateTemp("", "trilha-sandbox-*.env")
	if err != nil {
		return "", err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return file.Name(), err
	}
	// HOME first, so anything the run or the manifest sets wins over it: a
	// plain uid has no home directory of its own and most toolchains want
	// one, and the tmpfs is the writable path that is not the worktree.
	lines := append([]string{"HOME=/tmp", "TRILHA_TASK=" + req.Task, "TRILHA_WORKTREE=" + WorkDir}, req.Env...)
	if req.Spec != nil {
		for _, key := range sortedKeys(req.Spec.Env) {
			lines = append(lines, key+"="+req.Spec.Env[key])
		}
	}
	for _, line := range lines {
		// An env file is one KEY=VALUE per line: a value with a newline in it
		// would smuggle another variable.
		if strings.ContainsAny(line, "\n\x00") {
			return file.Name(), errors.New("sandbox: an environment value contains a newline")
		}
		if _, err := fmt.Fprintln(file, line); err != nil {
			return file.Name(), err
		}
	}
	return file.Name(), nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
