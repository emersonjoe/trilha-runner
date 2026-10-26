package redact

import (
	"strings"
	"testing"
)

func TestScanRemovesKnownAndCredentialShapedSecrets(t *testing.T) {
	cases := []struct {
		in    string
		known []string
		found bool
	}{
		{"Should the CSV include archived rows?", nil, false},
		{"use key sk-FAKE0123456789abcdefghij please", nil, true},
		{"token ghp_FAKE0123456789abcdefghij0123", nil, true},
		{"aws AKIAIOSFODNN7EXAMPLE here", nil, true},
		{"-----BEGIN OPENSSH PRIVATE KEY----- …", nil, true},
		{"the project key is project-SECRET-42", []string{"project-SECRET-42"}, true},
		{"a short known value abc stays", []string{"abc"}, false},
	}
	for _, c := range cases {
		out, found := Scan(c.in, c.known...)
		if found != c.found {
			t.Errorf("Scan(%q) found = %v, want %v", c.in, found, c.found)
		}
		if found && (!strings.Contains(out, "[REDACTED]") || strings.Contains(out, "FAKE0123") || strings.Contains(out, "SECRET-42")) {
			t.Errorf("Scan(%q) = %q", c.in, out)
		}
		if !found && out != c.in {
			t.Errorf("Scan changed clean text: %q → %q", c.in, out)
		}
	}
}
