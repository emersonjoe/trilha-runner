// Package redact is the runner's secret scan for text an agent writes that
// leaves the machine: a question, an overview, a note, an attention reason.
// It removes the run's own credentials and anything shaped like a well-known
// credential, and says whether it found one.
package redact

import (
	"regexp"
	"strings"
)

// Notice is what reaches the cloud instead of a question that held a secret.
const Notice = "The agent asked a question that contained a secret, so it is not shown here; read it on the runner (.trilha/runs/<task>/question.txt)."

const mask = "[REDACTED]"

var patterns = []*regexp.Regexp{
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bghp_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
}

// Scan answers text with every known secret and credential-shaped string
// replaced by [REDACTED], and whether there was any. Known secrets shorter
// than six bytes are ignored: they would match ordinary words.
func Scan(text string, known ...string) (string, bool) {
	found := false
	for _, secret := range known {
		if len(secret) >= 6 && strings.Contains(text, secret) {
			text = strings.ReplaceAll(text, secret, mask)
			found = true
		}
	}
	for _, pattern := range patterns {
		if pattern.MatchString(text) {
			text = pattern.ReplaceAllString(text, mask)
			found = true
		}
	}
	return text, found
}
