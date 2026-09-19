// Package deployer runs locally allow-listed delivery profiles.
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

type Step struct {
	Name   string   `json:"name"`
	Argv   []string `json:"argv"`
	OnFail string   `json:"on_fail,omitempty"`
}

type Migration struct {
	Argv       []string `json:"argv"`
	Downgrade  []string `json:"downgrade,omitempty"`
	Reversible bool     `json:"reversible"`
}

type HealthCheck struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type Profile struct {
	Deploy         []string      `json:"deploy,omitempty"`
	Rollback       []string      `json:"rollback,omitempty"`
	Steps          []Step        `json:"steps,omitempty"`
	Migrate        *Migration    `json:"migrate,omitempty"`
	Health         []HealthCheck `json:"health,omitempty"`
	HealthURL      string        `json:"health_url,omitempty"`
	TimeoutSeconds int           `json:"timeout_seconds,omitempty"`
}

type Result struct {
	Passed bool
	Log    string
	Health string
	Error  string
}

var environmentVariable = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

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
	return config, nil
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
	timeout := time.Duration(profile.TimeoutSeconds) * time.Second
	if timeout <= 0 || timeout > 30*time.Minute {
		timeout = 10 * time.Minute
	}
	operationContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var log strings.Builder
	if work.Deployment.Action == "rollback" {
		if err := executeRollback(operationContext, &log, profile, environment, work.Secrets); err != nil {
			return deployResult(log.String(), "", err, work.Secrets)
		}
		return deployResult(log.String(), "", nil, work.Secrets)
	}
	steps := append([]Step(nil), profile.Steps...)
	if len(steps) == 0 && len(profile.Deploy) > 0 {
		steps = []Step{{Name: "deploy", Argv: profile.Deploy, OnFail: "abort"}}
	}
	if len(steps) == 0 {
		return Result{Error: "deployment profile has no deploy steps"}
	}
	migrationIndex := migrationPosition(steps)
	migrationRan := false
	for index, step := range steps {
		if profile.Migrate != nil && index == migrationIndex {
			if err := runLogged(operationContext, &log, "migrate", profile.Migrate.Argv, environment, work.Secrets); err != nil {
				return deployResult(log.String(), "", fmt.Errorf("migration failed; switch aborted: %w", err), work.Secrets)
			}
			migrationRan = true
		}
		if strings.TrimSpace(step.Name) == "" {
			step.Name = fmt.Sprintf("step-%d", index+1)
		}
		if step.OnFail != "" && step.OnFail != "abort" {
			return deployResult(log.String(), "", fmt.Errorf("step %s has unsupported on_fail %q", step.Name, step.OnFail), work.Secrets)
		}
		if err := runLogged(operationContext, &log, step.Name, step.Argv, environment, work.Secrets); err != nil {
			return deployResult(log.String(), "", err, work.Secrets)
		}
	}
	if profile.Migrate != nil && !migrationRan {
		if err := runLogged(operationContext, &log, "migrate", profile.Migrate.Argv, environment, work.Secrets); err != nil {
			return deployResult(log.String(), "", fmt.Errorf("migration failed: %w", err), work.Secrets)
		}
	}
	health, err := checkHealth(operationContext, &log, profile)
	if err != nil {
		log.WriteString("health failed; starting rollback\n")
		rollbackErr := executeRollback(operationContext, &log, profile, environment, work.Secrets)
		if rollbackErr != nil {
			err = fmt.Errorf("%v; rollback failed: %w", err, rollbackErr)
		}
		return deployResult(log.String(), health, err, work.Secrets)
	}
	return deployResult(log.String(), health, nil, work.Secrets)
}

func migrationPosition(steps []Step) int {
	for index, step := range steps {
		if strings.EqualFold(strings.TrimSpace(step.Name), "switch") {
			return index
		}
	}
	if len(steps) <= 1 {
		return 0
	}
	return len(steps) - 1
}

func deploymentEnvironment(work queue.DeploymentWork) ([]string, error) {
	environment := append(safeEnvironment(),
		"TRILHA_DEPLOYMENT_ID="+work.Deployment.ID,
		"TRILHA_PROJECT="+work.Deployment.Project,
		"TRILHA_ENVIRONMENT="+work.Deployment.Environment,
		"TRILHA_DEPLOY_ACTION="+work.Deployment.Action,
		"TRILHA_REVISION="+work.Deployment.Revision,
		"TRILHA_PREVIOUS_REVISION="+work.Deployment.PreviousRevision,
	)
	for name, value := range work.Secrets {
		if !environmentVariable.MatchString(name) || strings.ContainsRune(value, 0) {
			return nil, errors.New("deployment contains an invalid secret")
		}
		environment = append(environment, name+"="+value)
	}
	return environment, nil
}

func executeRollback(ctx context.Context, log *strings.Builder, profile Profile, environment []string, secrets map[string]string) error {
	if len(profile.Rollback) == 0 {
		return errors.New("deployment profile has no rollback command")
	}
	environment = replaceEnvironment(environment, "TRILHA_DEPLOY_ACTION", "rollback")
	if err := runLogged(ctx, log, "rollback-image", profile.Rollback, environment, secrets); err != nil {
		return err
	}
	if profile.Migrate == nil || len(profile.Migrate.Downgrade) == 0 {
		return nil
	}
	if !profile.Migrate.Reversible {
		log.WriteString("rollback-migration skipped: migration is not reversible; image-only rollback\n")
		return nil
	}
	return runLogged(ctx, log, "rollback-migration", profile.Migrate.Downgrade, environment, secrets)
}

func replaceEnvironment(environment []string, name, value string) []string {
	prefix := name + "="
	result := append([]string(nil), environment...)
	for index, entry := range result {
		if strings.HasPrefix(entry, prefix) {
			result[index] = prefix + value
			return result
		}
	}
	return append(result, prefix+value)
}

func runLogged(ctx context.Context, log *strings.Builder, name string, argv, environment []string, secrets map[string]string) error {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return fmt.Errorf("%s has no argv", name)
	}
	started := time.Now()
	process := exec.CommandContext(ctx, argv[0], argv[1:]...)
	process.Env = environment
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
	fmt.Fprintf(log, "%s duration=%s exit=%d\n", name, time.Since(started).Round(time.Millisecond), exitCode)
	if output.Len() > 0 {
		log.WriteString(redact(output.String(), secrets))
		if !strings.HasSuffix(output.String(), "\n") {
			log.WriteByte('\n')
		}
	}
	if err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func checkHealth(ctx context.Context, log *strings.Builder, profile Profile) (string, error) {
	checks := append([]HealthCheck(nil), profile.Health...)
	if len(checks) == 0 && profile.HealthURL != "" {
		checks = []HealthCheck{{Name: "health", URL: profile.HealthURL, TimeoutSeconds: profile.TimeoutSeconds}}
	}
	var statuses []string
	for _, check := range checks {
		name := check.Name
		if name == "" {
			name = "health"
		}
		timeout := time.Duration(check.TimeoutSeconds) * time.Second
		if timeout <= 0 || timeout > 5*time.Minute {
			timeout = 30 * time.Second
		}
		checkContext, cancel := context.WithTimeout(ctx, timeout)
		request, err := http.NewRequestWithContext(checkContext, http.MethodGet, check.URL, nil)
		if err != nil {
			cancel()
			return strings.Join(statuses, ", "), err
		}
		started := time.Now()
		response, err := (&http.Client{Timeout: timeout}).Do(request)
		if err != nil {
			cancel()
			fmt.Fprintf(log, "health:%s duration=%s error=%s\n", name, time.Since(started).Round(time.Millisecond), err)
			return strings.Join(statuses, ", "), fmt.Errorf("health %s failed: %w", name, err)
		}
		response.Body.Close()
		cancel()
		status := name + "=" + response.Status
		statuses = append(statuses, status)
		fmt.Fprintf(log, "health:%s duration=%s status=%s\n", name, time.Since(started).Round(time.Millisecond), response.Status)
		if response.StatusCode < 200 || response.StatusCode >= 400 {
			return strings.Join(statuses, ", "), errors.New("health check failed: " + status)
		}
	}
	return strings.Join(statuses, ", "), nil
}

func deployResult(log, health string, err error, secrets map[string]string) Result {
	result := Result{Passed: err == nil, Log: limit(redact(log, secrets), 1<<20), Health: health}
	if err != nil {
		result.Error = redact(err.Error(), secrets)
	}
	return result
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
