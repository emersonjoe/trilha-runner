package driver

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Access is the model access one run may use: which provider, at which
// endpoint, with which credential, and which hosts the credential may be
// spent on. It is per project, sealed by the control plane and delivered on
// the claim — the way deployment secrets already travel in
// DeploymentWork.Secrets — so one worker can serve projects that must not
// share a key, a provider, or a country.
//
// The worker never writes it down: it reaches the agent as process
// environment and nowhere else, and Redact keeps it out of the output a
// driver captures.
type Access struct {
	// Provider picks the environment the driver builds: "anthropic" and
	// "claude-code" speak Anthropic's variables, everything else speaks the
	// OpenAI ones the framework already reads.
	Provider string `json:"provider,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	Model    string `json:"model,omitempty"`
	// Credential is the key or token. Never logged, never persisted.
	Credential string `json:"credential,omitempty"`
	// CredentialEnv overrides the variable the credential is exported as.
	CredentialEnv string `json:"credential_env,omitempty"`
	// AllowedHosts is data residency: when it is not empty, the driver
	// refuses a base URL whose host is not in it. A project whose data must
	// stay in the country says so here, and the runner enforces it rather
	// than trusting the worker's configuration.
	AllowedHosts []string `json:"allowed_hosts,omitempty"`
}

// ErrPolicy is a refusal to execute, not a failure to execute: the run never
// started because policy forbade it. The runner records it as `run` evidence
// with `stage: policy` and moves the task to failed.
var ErrPolicy = errors.New("driver: refused by policy")

// envName is the shape of an environment variable the runner is willing to
// set: the same rule the deployer applies to a deployment's secrets.
var envName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

// anthropic answers whether the provider speaks Anthropic's variables.
func (a *Access) anthropic() bool {
	switch strings.ToLower(a.Provider) {
	case "anthropic", "claude", "claude-code":
		return true
	}
	return false
}

// credentialEnv is the variable the credential is exported as.
func (a *Access) credentialEnv() string {
	if a.CredentialEnv != "" {
		return a.CredentialEnv
	}
	switch strings.ToLower(a.Provider) {
	case "claude-code":
		return "CLAUDE_CODE_OAUTH_TOKEN"
	case "anthropic", "claude":
		return "ANTHROPIC_API_KEY"
	}
	return "OPENAI_API_KEY"
}

// Env answers the environment this access adds to the agent's process. It is
// the only place the credential goes.
func (a *Access) Env() ([]string, error) {
	if a == nil {
		return nil, nil
	}
	if err := a.Allows(a.BaseURL); err != nil {
		return nil, err
	}
	name := a.credentialEnv()
	if !envName.MatchString(name) {
		return nil, fmt.Errorf("%w: %q is not an environment variable name", ErrPolicy, name)
	}
	if strings.ContainsRune(a.Credential, 0) {
		return nil, fmt.Errorf("%w: credential contains a NUL byte", ErrPolicy)
	}
	var out []string
	if a.Credential != "" {
		out = append(out, name+"="+a.Credential)
	}
	if a.anthropic() {
		if a.BaseURL != "" {
			out = append(out, "ANTHROPIC_BASE_URL="+a.BaseURL)
		}
		if a.Model != "" {
			out = append(out, "ANTHROPIC_MODEL="+a.Model, "TRILHA_AI_MODEL="+a.Model)
		}
		return out, nil
	}
	if a.BaseURL != "" {
		out = append(out, "OPENAI_BASE_URL="+a.BaseURL)
	}
	if a.Model != "" {
		out = append(out, "OPENAI_MODEL="+a.Model, "TRILHA_AI_MODEL="+a.Model)
	}
	return out, nil
}

// Allows enforces residency: a base URL whose host is not on the list is
// refused. An empty list allows anything — the control plane said nothing,
// so the worker's own configuration decides, as before.
func (a *Access) Allows(rawURL string) error {
	if a == nil || len(a.AllowedHosts) == 0 || rawURL == "" {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: %q is not a URL", ErrPolicy, rawURL)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("%w: %q has no host", ErrPolicy, rawURL)
	}
	for _, allowed := range a.AllowedHosts {
		if strings.ToLower(strings.TrimSpace(allowed)) == host {
			return nil
		}
	}
	return fmt.Errorf("%w: host %s is not among the allowed hosts (%s)", ErrPolicy, host, strings.Join(a.AllowedHosts, ", "))
}

// Secrets answers what must not appear in captured output.
func (a *Access) Secrets() []string {
	if a == nil || a.Credential == "" {
		return nil
	}
	return []string{a.Credential}
}

// Redact removes the credentials from text a driver is about to keep. It is
// belt and braces — an agent should not print its key — and it is cheap.
func Redact(text string, secrets []string) string {
	for _, s := range secrets {
		// A short value would redact half the output; a credential is long.
		if len(s) < 8 {
			continue
		}
		text = strings.ReplaceAll(text, s, "[REDACTED]")
	}
	return text
}

// ClaudeCodeArgv is the argv of the claude-code preset. It is fixed by the
// runner, not by a manifest: a preset whose command can be rewritten is just
// the exec driver with extra steps.
var ClaudeCodeArgv = []string{"claude", "-p", "-", "--permission-mode", "acceptEdits"}

// ClaudeCode is the exec driver with the argv fixed and the credential taken
// from the run's Access. The worktree is still the only writable path and
// TRILHA_TASK / TRILHA_WORKTREE are still set, so a manifest that names this
// driver needs to say nothing else.
type ClaudeCode struct{}

// Name is "claude-code".
func (ClaudeCode) Name() string { return "claude-code" }

// Execute runs the fixed argv through the exec driver.
func (ClaudeCode) Execute(ctx context.Context, job Job) (Output, error) {
	job.Command = strings.Join(ClaudeCodeArgv, " ")
	out, err := Exec{}.Execute(ctx, job)
	if out.Meta == nil {
		out.Meta = map[string]string{}
	}
	out.Meta["driver"] = "claude-code"
	out.Meta["preset"] = "claude-code"
	return out, err
}

// policyMeta is what a refusal should leave in the evidence: enough to see
// which endpoint was refused and against which list, and never the
// credential.
func policyMeta(a *Access) map[string]string {
	meta := map[string]string{"stage": "policy"}
	if a == nil {
		return meta
	}
	if a.Provider != "" {
		meta["provider"] = a.Provider
	}
	if a.BaseURL != "" {
		meta["base_url"] = a.BaseURL
	}
	if len(a.AllowedHosts) > 0 {
		meta["allowed_hosts"] = strings.Join(a.AllowedHosts, ", ")
	}
	return meta
}
