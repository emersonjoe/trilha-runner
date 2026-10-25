package runner

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

func classifyRetry(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	value := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled", false
	case isEnvironmentFailure(value):
		// A program the run needs is not installed. Asking the agent again
		// installs nothing, so this never retries (#13, #14).
		return "environment", false
	case errors.Is(err, context.DeadlineExceeded), strings.Contains(value, "timeout"), strings.Contains(value, "connection reset"):
		return "provider_transient", true
	case strings.Contains(value, "quota"), strings.Contains(value, "401"), strings.Contains(value, "403"), strings.Contains(value, "unauthorized"), strings.Contains(value, "permission"):
		return "provider_permanent", false
	case containsAny(value, "429", "500", "502", "503", "504"):
		return "provider_transient", true
	default:
		return "execution", false
	}
}

func (r *Runner) retryDelay(attempt int, err error) time.Duration {
	if seconds := retryAfter(err); seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	base := r.RetryBase
	if base <= 0 {
		base = time.Second
	}
	return base*time.Duration(1<<min(attempt-1, 4)) + time.Duration(time.Now().UnixNano()%250)*time.Millisecond
}

func retryAfter(err error) int {
	if err == nil {
		return 0
	}
	value := strings.ToLower(err.Error())
	index := strings.Index(value, "retry-after")
	if index < 0 {
		return 0
	}
	for _, field := range strings.Fields(value[index+len("retry-after"):]) {
		field = strings.Trim(field, ":=,;s")
		if seconds, parseErr := strconv.Atoi(field); parseErr == nil && seconds > 0 && seconds <= 300 {
			return seconds
		}
	}
	return 0
}

func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func fieldInt(value string, fallback int) int {
	number, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || number <= 0 {
		return fallback
	}
	return number
}

func estimateCost(tokens int) float64 {
	rate, _ := strconv.ParseFloat(os.Getenv("TRILHA_AI_COST_PER_MILLION_TOKENS"), 64)
	if rate <= 0 || tokens <= 0 {
		return 0
	}
	return float64(tokens) / 1_000_000 * rate
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

// Failure classes and error codes the runner emits beyond the provider ones.
const (
	// failureEnvironment is a worker that lacks something the run needs: a
	// tool a check calls, the agent's own binary. It is never retried.
	failureEnvironment = "environment"
	// errorMissingTool is the error_code next to failureEnvironment when the
	// missing thing is a program — the vocabulary of protocol spec 014.
	errorMissingTool = "environment_missing_tool"
)

// isEnvironmentFailure answers whether an error, lower-cased, says a program
// is not installed: exit status 127 (the shell's "command not found"), the
// words the shells use for it, or Go's own when exec cannot find a binary.
func isEnvironmentFailure(value string) bool {
	return containsAny(value, "exit status 127", "exited 127", "command not found", "executable file not found")
}

// errorCodeFor answers the error_code that accompanies a failure class, or
// "" when the class carries no finer detail.
func errorCodeFor(failureClass string) string {
	if failureClass == failureEnvironment {
		return errorMissingTool
	}
	return ""
}
