package main

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// versionTimeout bounds one `--version` call at startup: a wedged binary must
// not keep the worker from its first heartbeat.
const versionTimeout = 3 * time.Second

// agentBinaries are the command-line agents a fleet routes on, by the driver
// that starts them. `exec` runs whatever the manifest names, so the two the
// fleet knows are reported under their own names as well.
var agentBinaries = map[string]string{"claude-code": "claude", "codex": "codex"}

// driverVersions answers what each driver's binary says about itself, once,
// at startup. `echo` and `ai` are the runner itself and report its version;
// `claude-code` reports `claude --version`; `claude` and `codex` are listed
// under their own names for the fleet panel. A binary that is not installed,
// says nothing or does not answer in time is "unknown" — before this the
// runner repeated its own version for every driver, so the fleet could not
// tell which `claude` each worker had (roadmap RM-008).
func driverVersions(ctx context.Context, names []string, timeout time.Duration) map[string]string {
	versions := map[string]string{}
	for _, name := range names {
		versions[name] = version
	}
	for driverName, binary := range agentBinaries {
		found := toolVersion(ctx, binary, timeout)
		versions[binary] = found
		if _, isDriver := versions[driverName]; isDriver {
			versions[driverName] = found
		}
	}
	return versions
}

// toolVersion runs `binary --version` and answers its first line, or
// "unknown". The output is text from a program on the worker, headed for a
// heartbeat: one line, trimmed, at most 80 bytes.
func toolVersion(ctx context.Context, binary string, timeout time.Duration) string {
	if _, err := exec.LookPath(binary); err != nil {
		return "unknown"
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "--version")
	// A killed binary may leave a child holding stdout; do not wait on it.
	command.WaitDelay = 500 * time.Millisecond
	output, err := command.Output()
	if err != nil {
		return "unknown"
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return "unknown"
	}
	if len(line) > 80 {
		line = line[:80]
	}
	return line
}
