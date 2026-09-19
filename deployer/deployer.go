// Package deployer runs locally allow-listed delivery profiles.
//
// A profile used to be one argv to deploy, one to roll back and one URL to
// poll. A product that is a compose stack with a database needs an ordered
// sequence with a gate in the middle — pull the images, run the schema
// migration *before* traffic moves and abort if it fails, then wait for each
// service to be healthy — and a rollback that knows whether the migration
// can be reversed. That logic used to hide inside a wrapper script the
// profile pointed at, which is exactly what the profile exists to make
// visible, so it lives here now:
//
//	steps[]   ordered argv, each with a name and what to do when it fails
//	migrate   run between the pull and the switch; aborts before the switch
//	health[]  one check per service, polled until its own timeout
//
// The flat `deploy` argv stays valid: it is a profile with a single step.
// Cloud never supplies a command; only what the operator allow-listed here
// may run.
package deployer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/emersonjoe/trilha-runner/queue"
)

type Config struct {
	Profiles map[string]Profile `json:"profiles"`
}

// Profile is one environment's delivery.
type Profile struct {
	// Deploy is the single-argv form, kept valid: one step named "deploy".
	Deploy []string `json:"deploy,omitempty"`
	// Steps is the ordered form. Deploy and Steps are not combined; Steps
	// wins when both are present.
	Steps []Step `json:"steps,omitempty"`
	// Migrate is the gate: it runs immediately before the step marked
	// `switch`, or after the last step when none is marked, and a failure
	// aborts the delivery before traffic moves.
	Migrate *Migrate `json:"migrate,omitempty"`
	// Rollback puts the previous image back.
	Rollback []string `json:"rollback,omitempty"`
	// Health is one check per service, each polled until its own timeout.
	Health []Health `json:"health,omitempty"`
	// HealthURL is the single-URL form, checked after Health.
	HealthURL string `json:"health_url,omitempty"`
	// TimeoutSeconds bounds the whole profile (default 10 minutes, max 30).
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// Step is one argv in a delivery, in order.
type Step struct {
	Name string   `json:"name,omitempty"`
	Argv []string `json:"argv"`
	// OnFail is "abort" (the default) or "continue".
	OnFail string `json:"on_fail,omitempty"`
	// Switch marks the step that puts the new revision in front of traffic.
	// The migration runs immediately before it.
	Switch bool `json:"switch,omitempty"`
	// TimeoutSeconds bounds this step inside the profile's budget.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// Migrate is the schema migration and whether it can be undone.
type Migrate struct {
	Argv []string `json:"argv"`
	// Downgrade reverses the migration. It runs on a rollback only when
	// Reversible is true; otherwise the rollback is image-only and says so.
	Downgrade      []string `json:"downgrade,omitempty"`
	Reversible     bool     `json:"reversible,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}

// Health is one service's readiness check.
type Health struct {
	Name string `json:"name,omitempty"`
	URL  string `json:"url"`
	// TimeoutSeconds is how long to wait for this service (default 60).
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

type Result struct {
	Passed bool
	Log    string
	Health string
	Error  string
	// Rollback says what a rollback did: "image and schema", "image only" —
	// with the reason — or empty when none ran.
	Rollback string
}

var environmentVariable = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

// healthInterval is how often a service is polled while it comes up.
var healthInterval = time.Second

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var config Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("deployer: config: %w", err)
	}
	if len(config.Profiles) == 0 {
		return Config{}, errors.New("deployer: no profiles configured")
	}
	for name, profile := range config.Profiles {
		if err := profile.validate(); err != nil {
			return Config{}, fmt.Errorf("deployer: profile %q: %w", name, err)
		}
	}
	return config, nil
}

func (profile Profile) validate() error {
	for index, step := range profile.steps() {
		if !runnable(step.Argv) {
			return fmt.Errorf("step %d (%s) has no command", index+1, step.Name)
		}
		switch step.OnFail {
		case "", "abort", "continue":
		default:
			return fmt.Errorf("step %q: on_fail is %q, not abort or continue", step.Name, step.OnFail)
		}
	}
	if profile.Migrate != nil && !runnable(profile.Migrate.Argv) {
		return errors.New("migrate has no command")
	}
	if profile.Migrate != nil && profile.Migrate.Reversible && !runnable(profile.Migrate.Downgrade) {
		return errors.New("migrate is reversible but has no downgrade command")
	}
	for _, check := range profile.Health {
		if strings.TrimSpace(check.URL) == "" {
			return fmt.Errorf("health check %q has no url", check.Name)
		}
	}
	return nil
}

func runnable(argv []string) bool {
	return len(argv) > 0 && strings.TrimSpace(argv[0]) != ""
}

// steps answers the profile's deploy sequence: the ordered form when it is
// there, otherwise the flat argv as a single step.
func (profile Profile) steps() []Step {
	if len(profile.Steps) > 0 {
		return profile.Steps
	}
	if runnable(profile.Deploy) {
		return []Step{{Name: "deploy", Argv: profile.Deploy, Switch: true}}
	}
	return nil
}

// checks answers every health check, the single-URL form included.
func (profile Profile) checks() []Health {
	out := append([]Health(nil), profile.Health...)
	if profile.HealthURL != "" {
		out = append(out, Health{Name: "service", URL: profile.HealthURL})
	}
	return out
}

func (profile Profile) budget() time.Duration {
	timeout := time.Duration(profile.TimeoutSeconds) * time.Second
	if timeout <= 0 || timeout > 30*time.Minute {
		timeout = 10 * time.Minute
	}
	return timeout
}

// run is one delivery: the log it is building, the environment every command
// gets and the secrets that must not appear in either.
type run struct {
	work    queue.DeploymentWork
	log     strings.Builder
	baseEnv []string
}

func (r *run) writef(format string, args ...any) { fmt.Fprintf(&r.log, format, args...) }

// exec runs one argv and records its exit and duration. It answers the error
// so the caller decides what a failure means.
func (r *run) exec(ctx context.Context, label string, argv []string, timeout time.Duration) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	start := time.Now()
	process := exec.CommandContext(ctx, argv[0], argv[1:]...)
	process.Env = append(append([]string(nil), r.baseEnv...), "TRILHA_STEP="+label)
	var output bytes.Buffer
	process.Stdout = &output
	process.Stderr = &output
	err := process.Run()
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			exitCode = exitError.ExitCode()
		}
	}
	r.writef("%s: exit %d in %s\n", label, exitCode, time.Since(start).Round(time.Millisecond))
	if text := strings.TrimRight(output.String(), "\n"); text != "" {
		r.writef("%s\n", text)
	}
	return err
}

func (config Config) Execute(ctx context.Context, work queue.DeploymentWork) Result {
	profile, ok := config.Profiles[work.Deployment.Profile]
	if !ok {
		return Result{Error: "deployment profile is not allow-listed"}
	}
	environment, err := deploymentEnvironment(work)
	if err != nil {
		return Result{Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, profile.budget())
	defer cancel()
	r := &run{work: work, baseEnv: environment}
	result := profile.execute(ctx, r)
	result.Log = limit(redact(r.log.String(), work.Secrets), 1<<20)
	return result
}

func (profile Profile) execute(ctx context.Context, r *run) Result {
	if r.work.Deployment.Action == "rollback" {
		return profile.rollback(ctx, r, "")
	}
	steps := profile.steps()
	if len(steps) == 0 {
		return Result{Error: "deployment profile has no command for this action"}
	}
	migrated := profile.Migrate == nil
	for _, step := range steps {
		// The gate: the schema moves before traffic does, and a migration
		// that fails aborts the delivery with the old revision still serving.
		if !migrated && step.Switch {
			migrated = true
			if err := profile.migrate(ctx, r); err != nil {
				r.writef("aborted before the switch: the migration failed\n")
				return Result{Error: "migration failed before the switch: " + err.Error()}
			}
		}
		label := step.Name
		if label == "" {
			label = "step"
		}
		if err := r.exec(ctx, label, step.Argv, time.Duration(step.TimeoutSeconds)*time.Second); err != nil {
			if step.OnFail == "continue" {
				r.writef("%s failed and the profile says continue\n", label)
				continue
			}
			return Result{Error: "step " + label + " failed: " + err.Error()}
		}
	}
	// A profile with no step marked `switch` runs the migration at the end,
	// after everything it declared.
	if !migrated {
		if err := profile.migrate(ctx, r); err != nil {
			return Result{Error: "migration failed: " + err.Error()}
		}
	}
	status, err := profile.health(ctx, r)
	if err != nil {
		// The new revision is in front of traffic and not healthy: put the
		// previous one back.
		result := profile.rollback(ctx, r, err.Error())
		rollbackError := result.Error
		result.Passed = false
		result.Health = status
		// The health failure is what went wrong; a rollback that also failed
		// is added to it, never in place of it.
		result.Error = err.Error()
		if rollbackError != "" {
			result.Error += "; " + rollbackError
		}
		return result
	}
	return Result{Passed: true, Health: status}
}

// migrate runs the schema migration.
func (profile Profile) migrate(ctx context.Context, r *run) error {
	if profile.Migrate == nil {
		return nil
	}
	label := "migrate"
	if profile.Migrate.Reversible {
		label = "migrate (reversible)"
	}
	return r.exec(ctx, label, profile.Migrate.Argv, time.Duration(profile.Migrate.TimeoutSeconds)*time.Second)
}

// rollback puts the previous image back and reverses the schema only when the
// migration said it can be reversed. A rollback that cannot undo the schema
// is reported as image-only, with the reason, rather than pretending.
func (profile Profile) rollback(ctx context.Context, r *run, because string) Result {
	if because != "" {
		r.writef("rolling back: %s\n", because)
	}
	if !runnable(profile.Rollback) {
		r.writef("rollback: the profile has no rollback command\n")
		return Result{Error: "deployment profile has no command for this action", Rollback: "none"}
	}
	result := Result{}
	if err := r.exec(ctx, "rollback", profile.Rollback, 0); err != nil {
		result.Error = "rollback failed: " + err.Error()
		result.Rollback = "failed"
		return result
	}
	switch {
	case profile.Migrate == nil:
		result.Rollback = "image only: the profile declares no migration"
	case profile.Migrate.Reversible:
		if err := r.exec(ctx, "downgrade", profile.Migrate.Downgrade, time.Duration(profile.Migrate.TimeoutSeconds)*time.Second); err != nil {
			result.Error = "downgrade failed: " + err.Error()
			result.Rollback = "image only: the downgrade failed"
			r.writef("rollback: image only — the downgrade failed\n")
			return result
		}
		result.Rollback = "image and schema"
	default:
		result.Rollback = "image only: the migration is not reversible"
		r.writef("rollback: image only — the migration is not reversible, the schema keeps the new shape\n")
	}
	if r.work.Deployment.Action == "rollback" {
		result.Passed = result.Error == ""
	}
	r.writef("rollback: %s\n", result.Rollback)
	return result
}

// health polls every declared service until it answers or its own timeout
// runs out, and answers the statuses it saw.
func (profile Profile) health(ctx context.Context, r *run) (string, error) {
	var statuses []string
	for _, check := range profile.checks() {
		name := check.Name
		if name == "" {
			name = "service"
		}
		timeout := time.Duration(check.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = time.Minute
		}
		start := time.Now()
		status, err := waitHealthy(ctx, check.URL, timeout)
		if status != "" {
			statuses = append(statuses, name+"="+status)
		}
		if err != nil {
			r.writef("health %s: %s after %s\n", name, firstLine(err.Error()), time.Since(start).Round(time.Millisecond))
			return strings.Join(statuses, " "), fmt.Errorf("health check %s failed: %w", name, err)
		}
		r.writef("health %s: %s\n", name, status)
	}
	return strings.Join(statuses, " "), nil
}

// waitHealthy polls one URL until it answers 2xx/3xx or the timeout expires.
func waitHealthy(ctx context.Context, url string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{Timeout: 15 * time.Second}
	var status string
	var last error
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return status, err
		}
		response, err := client.Do(request)
		switch {
		case err != nil:
			last = err
		default:
			response.Body.Close()
			status = response.Status
			if response.StatusCode >= 200 && response.StatusCode < 400 {
				return status, nil
			}
			last = errors.New(response.Status)
		}
		select {
		case <-ctx.Done():
			if last == nil {
				last = ctx.Err()
			}
			return status, last
		case <-time.After(healthInterval):
		}
	}
}

// deploymentEnvironment is what every command in a delivery gets: the
// allow-listed host variables, what the deployment is, and its secrets.
func deploymentEnvironment(work queue.DeploymentWork) ([]string, error) {
	environment := append(safeEnvironment(),
		"TRILHA_DEPLOYMENT_ID="+work.Deployment.ID,
		"TRILHA_PROJECT="+work.Deployment.Project,
		"TRILHA_ENVIRONMENT="+work.Deployment.Environment,
		"TRILHA_DEPLOY_ACTION="+work.Deployment.Action,
		"TRILHA_REVISION="+work.Deployment.Revision,
	)
	for name, value := range work.Secrets {
		if !environmentVariable.MatchString(name) || strings.ContainsRune(value, 0) {
			return nil, errors.New("deployment contains an invalid secret")
		}
		environment = append(environment, name+"="+value)
	}
	return environment, nil
}

func firstLine(value string) string {
	line, _, _ := strings.Cut(value, "\n")
	return line
}

func limit(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[len(value)-maximum:]
}

func safeEnvironment() []string {
	allowed := []string{"HOME", "LANG", "LC_ALL", "PATH", "TMPDIR"}
	result := make([]string, 0, len(allowed))
	for _, name := range allowed {
		if value, ok := os.LookupEnv(name); ok {
			result = append(result, name+"="+value)
		}
	}
	return result
}

func redact(value string, secrets map[string]string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}
