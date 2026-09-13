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

type Profile struct {
	Deploy         []string `json:"deploy"`
	Rollback       []string `json:"rollback"`
	HealthURL      string   `json:"health_url,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
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
	command := profile.Deploy
	if work.Deployment.Action == "rollback" {
		command = profile.Rollback
	}
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return Result{Error: "deployment profile has no command for this action"}
	}
	timeout := time.Duration(profile.TimeoutSeconds) * time.Second
	if timeout <= 0 || timeout > 30*time.Minute {
		timeout = 10 * time.Minute
	}
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	process := exec.CommandContext(commandContext, command[0], command[1:]...)
	process.Env = append(safeEnvironment(),
		"TRILHA_DEPLOYMENT_ID="+work.Deployment.ID,
		"TRILHA_PROJECT="+work.Deployment.Project,
		"TRILHA_ENVIRONMENT="+work.Deployment.Environment,
		"TRILHA_DEPLOY_ACTION="+work.Deployment.Action,
		"TRILHA_REVISION="+work.Deployment.Revision,
	)
	for name, value := range work.Secrets {
		if !environmentVariable.MatchString(name) || strings.ContainsRune(value, 0) {
			return Result{Error: "deployment contains an invalid secret"}
		}
		process.Env = append(process.Env, name+"="+value)
	}
	var output bytes.Buffer
	process.Stdout = &output
	process.Stderr = &output
	err := process.Run()
	result := Result{Log: limit(redact(output.String(), work.Secrets), 1<<20)}
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if profile.HealthURL != "" {
		request, err := http.NewRequestWithContext(commandContext, http.MethodGet, profile.HealthURL, nil)
		if err != nil {
			result.Error = err.Error()
			return result
		}
		response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
		if err != nil {
			result.Error = err.Error()
			return result
		}
		response.Body.Close()
		result.Health = response.Status
		if response.StatusCode < 200 || response.StatusCode >= 400 {
			result.Error = "health check failed: " + response.Status
			return result
		}
	}
	result.Passed = true
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
