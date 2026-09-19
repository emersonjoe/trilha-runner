// Package sandbox confines execution. None uses the task worktree directly;
// Docker mounts that worktree as the only writable host path and starts any
// declared services on a private per-run network.
//
// A run's environment is handed to Prepare rather than to each command,
// because a secret belongs in the container and not on a command line: an
// argument of `docker exec` is an argument of a host process, and `ps` shows
// it to every user on the machine.
package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/emersonjoe/trilha-spec/agent"
)

// Env is what a prepared sandbox hands the driver: the host worktree, extra
// environment variables and an argv prefix used to enter the sandbox.
type Env struct {
	Dir    string
	Env    []string
	Prefix []string
}

// Sandbox prepares and releases an execution environment. env is the run's
// environment as KEY=VALUE, including any credential the queue supplied: a
// sandbox that moves execution elsewhere must carry it there without putting
// it on a command line.
type Sandbox interface {
	Name() string
	Prepare(ctx context.Context, dir string, env []string) (Env, func() error, error)
}

// None runs in place: the worktree is the sandbox.
type None struct{}

func (None) Name() string { return "none" }

// Prepare answers the directory as is. The environment travels with the
// process, as it always did, so it is handed straight back.
func (None) Prepare(ctx context.Context, dir string, env []string) (Env, func() error, error) {
	return Env{Dir: dir, Env: env}, func() error { return nil }, nil
}

// Service is a private dependency container declared by the agent manifest.
type Service struct {
	Name  string            `json:"name"`
	Image string            `json:"image"`
	Env   map[string]string `json:"env,omitempty"`
	Ready []string          `json:"ready,omitempty"`
}

// Docker runs an exec driver and checks in a locked-down container. Services
// share a private bridge network but no Docker socket or host paths.
type Docker struct {
	Image    string
	Services []Service
	Binary   string
}

func (Docker) Name() string { return "docker" }

var safeName = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// FromAgent reads the runner-specific one-level sandbox map. services is a
// JSON array because Trilha front matter deliberately supports only scalars,
// scalar lists and one-level maps.
func FromAgent(manifest *agent.Agent) (Sandbox, bool, error) {
	if manifest == nil {
		return nil, false, nil
	}
	rawFields, _ := manifest.Fields.Map()["sandbox"].(map[string]string)
	fields := rawFields
	if len(fields) == 0 {
		return nil, false, nil
	}
	kind := strings.ToLower(strings.TrimSpace(fields["type"]))
	if kind == "" {
		kind = "docker"
	}
	if kind != "docker" {
		return nil, false, fmt.Errorf("sandbox: unsupported type %q", kind)
	}
	config := Docker{Image: strings.TrimSpace(fields["image"])}
	if config.Image == "" {
		return nil, false, errors.New("sandbox: docker image is required")
	}
	if raw := strings.TrimSpace(fields["services"]); raw != "" {
		if err := json.Unmarshal([]byte(raw), &config.Services); err != nil {
			return nil, false, fmt.Errorf("sandbox: services: %w", err)
		}
	}
	for _, service := range config.Services {
		if strings.TrimSpace(service.Name) == "" || strings.TrimSpace(service.Image) == "" {
			return nil, false, errors.New("sandbox: every service needs name and image")
		}
	}
	return config, true, nil
}

func (d Docker) command(ctx context.Context, args ...string) *exec.Cmd {
	binary := d.Binary
	if binary == "" {
		binary = "docker"
	}
	return exec.CommandContext(ctx, binary, args...)
}

func (d Docker) Prepare(ctx context.Context, dir string, env []string) (Env, func() error, error) {
	if _, err := exec.LookPath(first(d.Binary, "docker")); err != nil {
		return Env{}, nil, fmt.Errorf("sandbox: docker unavailable: %w", err)
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return Env{}, nil, err
	}
	id := fmt.Sprintf("trilha-%d", time.Now().UnixNano())
	network := id + "-net"
	mainContainer := id + "-agent"
	containers := []string{}
	cleanup := func() error {
		var failures []string
		for index := len(containers) - 1; index >= 0; index-- {
			if output, err := d.command(context.Background(), "rm", "-f", containers[index]).CombinedOutput(); err != nil && !strings.Contains(string(output), "No such container") {
				failures = append(failures, strings.TrimSpace(string(output)))
			}
		}
		if output, err := d.command(context.Background(), "network", "rm", network).CombinedOutput(); err != nil && !strings.Contains(string(output), "not found") {
			failures = append(failures, strings.TrimSpace(string(output)))
		}
		if len(failures) > 0 {
			return errors.New(strings.Join(failures, "; "))
		}
		return nil
	}
	// On failure whatever was created is removed here and now, and the
	// release answered is a no-op rather than nil: a caller that defers it
	// without checking the error should not panic for being careless.
	fail := func(cause error) (Env, func() error, error) {
		_ = cleanup()
		return Env{}, func() error { return nil }, cause
	}
	if output, err := d.command(ctx, "network", "create", "--label", "trilha.run="+id, network).CombinedOutput(); err != nil {
		return fail(fmt.Errorf("sandbox: create network: %w: %s", err, strings.TrimSpace(string(output))))
	}
	if err := d.ensureImage(ctx, d.Image, absolute); err != nil {
		return fail(err)
	}
	for _, service := range d.Services {
		if err := d.ensureImage(ctx, service.Image, absolute); err != nil {
			return fail(err)
		}
		name := id + "-" + safeName.ReplaceAllString(service.Name, "-")
		args := []string{"run", "-d", "--name", name, "--label", "trilha.run=" + id, "--network", network, "--network-alias", service.Name}
		args = append(args, resourceLimits()...)
		args = append(args, "--tmpfs", "/tmp:rw,nosuid,nodev", "--tmpfs", "/run:rw,nosuid,nodev", "--tmpfs", "/var/lib/postgresql/data:rw,nosuid,nodev")
		for key, value := range service.Env {
			args = append(args, "--env", key+"="+value)
		}
		args = append(args, service.Image)
		if output, err := d.command(ctx, args...).CombinedOutput(); err != nil {
			return fail(fmt.Errorf("sandbox: start service %s: %w: %s", service.Name, err, strings.TrimSpace(string(output))))
		}
		containers = append(containers, name)
		if err := d.ensureUp(ctx, name, "service "+service.Name); err != nil {
			return fail(err)
		}
		if len(service.Ready) > 0 {
			if err := d.waitReady(ctx, name, service.Ready); err != nil {
				return fail(fmt.Errorf("sandbox: service %s: %w", service.Name, err))
			}
		}
	}
	// The run's environment enters the container once, through a file only
	// this user can read, so no secret is ever an argument of a host process.
	environmentFile, err := writeEnvironmentFile(env)
	if environmentFile != "" {
		previous := cleanup
		cleanup = func() error { defer os.Remove(environmentFile); return previous() }
	}
	if err != nil {
		return fail(err)
	}
	args := []string{"run", "-d", "--name", mainContainer, "--label", "trilha.run=" + id, "--network", network}
	args = append(args, resourceLimits()...)
	args = append(args, agentPrivileges()...)
	args = append(args,
		"--tmpfs", "/tmp:rw,nosuid,nodev",
		"--tmpfs", "/run:rw,nosuid,nodev",
		"--env-file", environmentFile,
		"--mount", "type=bind,src="+absolute+",dst=/workspace",
		"--workdir", "/workspace", d.Image, "tail", "-f", "/dev/null",
	)
	if output, err := d.command(ctx, args...).CombinedOutput(); err != nil {
		return fail(fmt.Errorf("sandbox: start agent: %w: %s", err, strings.TrimSpace(string(output))))
	}
	containers = append(containers, mainContainer)
	if err := d.ensureUp(ctx, mainContainer, "the agent container"); err != nil {
		return fail(err)
	}
	// No `env` on the prefix: the container already holds the environment.
	return Env{Dir: absolute, Prefix: []string{first(d.Binary, "docker"), "exec", "-i", "--workdir", "/workspace", mainContainer}}, cleanup, nil
}

// agentPrivileges drops what the agent does not need and runs it as the user
// that owns the worktree.
//
// The two go together and cannot be separated. Dropping every capability
// removes CAP_DAC_OVERRIDE, and root then stops bypassing file permission
// checks — so a worktree owned by the operator becomes unwritable by a root
// agent, and the sandbox quietly stops being able to do its job. Running as
// the owner is what keeps the one writable path writable, and it makes the
// agent something less than root while it is there.
func agentPrivileges() []string {
	out := []string{"--cap-drop", "ALL"}
	if uid := os.Getuid(); uid >= 0 {
		out = append(out, "--user", strconv.Itoa(uid)+":"+strconv.Itoa(os.Getgid()))
	}
	return out
}

// writeEnvironmentFile puts the run's environment where only this user can
// read it. HOME comes first so anything the run sets overrides it: a plain uid
// has no home of its own and most toolchains want one, and the tmpfs is the
// writable path that is not the worktree.
func writeEnvironmentFile(env []string) (string, error) {
	file, err := os.CreateTemp("", "trilha-sandbox-*.env")
	if err != nil {
		return "", err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return file.Name(), err
	}
	for _, line := range append([]string{"HOME=/tmp", "TRILHA_WORKTREE=/workspace"}, env...) {
		// An env file is one KEY=VALUE per line, so a value with a newline in
		// it would smuggle in a second variable.
		if strings.ContainsAny(line, "\n\x00") {
			return file.Name(), errors.New("sandbox: an environment value contains a newline")
		}
		if _, err := fmt.Fprintln(file, line); err != nil {
			return file.Name(), err
		}
	}
	return file.Name(), nil
}

// up answers whether a container is still running.
func (d Docker) up(ctx context.Context, container string) (bool, error) {
	output, err := d.command(ctx, "inspect", "--format", "{{.State.Running}}", container).Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(output)) == "true", nil
}

// ensureUp fails with the container's own output when it did not stay up — an
// image whose keep-alive command is missing, a service whose entrypoint exited
// — instead of leaving that to surface later as an unexplained `docker exec`
// failure, or as a readiness timeout spent waiting for something already gone.
func (d Docker) ensureUp(ctx context.Context, container, what string) error {
	running, err := d.up(ctx, container)
	if err != nil {
		return fmt.Errorf("sandbox: inspect %s: %w", container, err)
	}
	if running {
		return nil
	}
	message := fmt.Sprintf("sandbox: %s exited instead of staying up (container %s)", what, container)
	if logs, err := d.command(ctx, "logs", "--tail", "20", container).CombinedOutput(); err == nil {
		if text := strings.TrimSpace(string(logs)); text != "" {
			message += ": " + text
		}
	}
	return errors.New(message)
}

func resourceLimits() []string {
	return []string{"--read-only", "--security-opt", "no-new-privileges", "--pids-limit", "256", "--memory", "1g", "--cpus", "1"}
}

func (d Docker) ensureImage(ctx context.Context, image, buildDir string) error {
	if err := d.command(ctx, "image", "inspect", image).Run(); err == nil {
		return nil
	}
	if output, err := d.command(ctx, "pull", image).CombinedOutput(); err == nil {
		return nil
	} else if _, statErr := os.Stat(filepath.Join(buildDir, "Dockerfile")); statErr != nil {
		return fmt.Errorf("sandbox: pull image %s: %w: %s", image, err, strings.TrimSpace(string(output)))
	}
	if output, err := d.command(ctx, "build", "-t", image, buildDir).CombinedOutput(); err != nil {
		return fmt.Errorf("sandbox: build image %s: %w: %s", image, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (d Docker) waitReady(ctx context.Context, container string, ready []string) error {
	deadline := time.Now().Add(90 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		args := append([]string{"exec", container}, ready...)
		output, err := d.command(ctx, args...).CombinedOutput()
		last = strings.TrimSpace(string(output))
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("readiness timed out: %s", last)
}

func first(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
