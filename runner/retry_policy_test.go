package runner

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestClassifyRetry(t *testing.T) {
	cases := []struct {
		err       error
		class     string
		retryable bool
	}{
		{nil, "", false},
		{context.Canceled, "cancelled", false},
		{fmt.Errorf("model: %w", context.DeadlineExceeded), "provider_transient", true},
		{errors.New("read tcp: connection reset by peer"), "provider_transient", true},
		{errors.New("HTTP 429 Too Many Requests"), "provider_transient", true},
		{errors.New("upstream 503"), "provider_transient", true},
		{errors.New("HTTP 401 Unauthorized"), "provider_permanent", false},
		{errors.New("insufficient quota"), "provider_permanent", false},
		{errors.New("permission denied"), "provider_permanent", false},
		{errors.New("exit status 9"), "execution", false},
		// A program that is not installed: exit 127, or the shell's and Go's
		// own words for it. Asking the agent again installs nothing (#13, #14).
		{errors.New("exec: python exited 127"), "environment", false},
		{errors.New("sh: 1: python: not found (exit status 127)"), "environment", false},
		{errors.New("bash: python: command not found"), "environment", false},
		{errors.New(`exec: exec: "claude": executable file not found in $PATH`), "environment", false},
	}
	for _, c := range cases {
		class, retryable := classifyRetry(c.err)
		if class != c.class || retryable != c.retryable {
			t.Errorf("classifyRetry(%v) = %q, %v; want %q, %v", c.err, class, retryable, c.class, c.retryable)
		}
	}
}

func TestRetryDelay(t *testing.T) {
	r := &Runner{RetryBase: 100 * time.Millisecond}
	if got := r.retryDelay(1, errors.New("429; Retry-After: 7")); got != 7*time.Second {
		t.Fatalf("Retry-After honoured: %s", got)
	}
	// Out-of-range or absent Retry-After falls back to exponential backoff
	// with under 250ms of jitter, capped at 16× the base.
	for attempt, base := range map[int]time.Duration{1: 100, 2: 200, 3: 400, 5: 1600, 9: 1600} {
		got := r.retryDelay(attempt, errors.New("503 retry-after 9999"))
		low := base * time.Millisecond
		if got < low || got >= low+250*time.Millisecond {
			t.Errorf("attempt %d: delay %s outside [%s, %s)", attempt, got, low, low+250*time.Millisecond)
		}
	}
	if got := (&Runner{}).retryDelay(1, nil); got < time.Second {
		t.Fatalf("default base: %s", got)
	}
}

func TestWaitRetryStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitRetry(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
