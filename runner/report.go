package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/emersonjoe/trilha-runner/internal/agentlink"
	"github.com/emersonjoe/trilha-runner/internal/redact"
	"github.com/emersonjoe/trilha-spec/execution"
)

// What a review, hunt or repair run reports besides its branch: findings, a
// verdict, replies to review comments and artifacts. Everything here leaves
// the machine, so it is checked once, when the agent hands it over:
// findings are validated and secret-redacted; replies are redacted and only
// answer comments the claim carried; an artifact is copied out of the
// agent's reach and refused — not redacted — when its text holds a secret.

// Artifact is a report the agent published: the metadata the control plane
// receives and the copy the worker uploads.
type Artifact struct {
	execution.Artifact
	// Path is the runner's own copy, under .trilha/runs/<run>/artifacts/.
	Path string `json:"path"`
}

// agentReport is the part of an agentSession that a review, hunt or repair
// run fills.
type agentReport struct {
	findings []execution.Finding
	verdict  string
	replies  map[string]string
	// comments are the ids a reply may answer, in the claim's order.
	comments  []string
	artifacts []Artifact
	// artifactDir is where accepted artifacts are copied; sources are the
	// directories a published file may come from.
	artifactDir string
	sources     []string
	full        bool
}

// artifactTypes maps an extension to the content type the control plane
// accepts, with the bytes an image must start with: a ".png" that is text
// would otherwise skip the secret scan.
var artifactTypes = map[string]struct {
	contentType string
	magic       []byte
}{
	".html": {"text/html", nil},
	".md":   {"text/markdown", nil},
	".png":  {"image/png", []byte("\x89PNG\r\n\x1a\n")},
	".jpg":  {"image/jpeg", []byte{0xff, 0xd8, 0xff}},
	".jpeg": {"image/jpeg", []byte{0xff, 0xd8, 0xff}},
}

func (a *agentSession) handleReport(message agentlink.Message) {
	switch message.Type {
	case agentlink.TypeFinding:
		a.addFinding(message.Finding)
	case agentlink.TypeVerdict:
		if message.Text == "pass" || message.Text == "changes" {
			a.mu.Lock()
			a.report.verdict = message.Text
			a.mu.Unlock()
		}
	case agentlink.TypeReply:
		a.addReply(message.ID, message.Text)
	case agentlink.TypeArtifact:
		if err := a.addArtifact(message.Name, message.Path, message.Content); err != nil {
			a.log("artifact %q refused: %v", message.Name, err)
		}
	}
}

func (a *agentSession) log(format string, args ...any) {
	if a.logf != nil {
		a.logf(format, args...)
	}
}

func (a *agentSession) addFinding(finding *execution.Finding) {
	if finding == nil {
		return
	}
	f := *finding
	f.Title, _ = redact.Scan(strings.TrimSpace(f.Title), a.secrets...)
	f.Why, _ = redact.Scan(strings.TrimSpace(f.Why), a.secrets...)
	f.Repro, _ = redact.Scan(strings.TrimSpace(f.Repro), a.secrets...)
	f.Path = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(f.Path)), "./")
	f.Title = execution.Truncate(f.Title, execution.MaxFindingTitle)
	f.Why = execution.Truncate(f.Why, execution.MaxFindingText)
	f.Repro = execution.Truncate(f.Repro, execution.MaxFindingText)
	// Area, style and who found it are the runner's and the cloud's to say.
	f.Area, f.Style, f.FoundBy = "", "", nil
	if err := f.Validate(); err != nil {
		a.log("finding refused: %v", err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.report.findings) >= execution.MaxFindings {
		if !a.report.full {
			a.report.full = true
			a.log("more than %d findings: the rest are dropped", execution.MaxFindings)
		}
		return
	}
	a.report.findings = append(a.report.findings, f)
}

func (a *agentSession) addReply(id, text string) {
	id, text = strings.TrimSpace(id), strings.TrimSpace(text)
	a.mu.Lock()
	defer a.mu.Unlock()
	known := false
	for _, comment := range a.report.comments {
		known = known || comment == id
	}
	if !known || text == "" {
		a.log("reply to %q refused: not a review comment of this run", id)
		return
	}
	clean, _ := redact.Scan(text, a.secrets...)
	if a.report.replies == nil {
		a.report.replies = map[string]string{}
	}
	a.report.replies[id] = execution.Truncate(clean, execution.MaxReviewReply)
}

// addArtifact validates and copies one artifact into the run's directory.
func (a *agentSession) addArtifact(name, source, content string) error {
	name = strings.TrimSpace(name)
	if name == "" && source != "" {
		name = filepath.Base(source)
	}
	kind, ok := artifactTypes[strings.ToLower(filepath.Ext(name))]
	if !ok {
		return errors.New("the type must be .html, .md, .png, .jpg or .jpeg")
	}
	var data []byte
	var err error
	if source != "" {
		data, err = a.readSource(source)
	} else {
		data = []byte(content)
	}
	if err != nil {
		return err
	}
	if len(data) == 0 || len(data) > execution.MaxArtifactBytes {
		return fmt.Errorf("size must be between 1 and %d bytes", execution.MaxArtifactBytes)
	}
	if kind.magic != nil && !bytes.HasPrefix(data, kind.magic) {
		return fmt.Errorf("the file is not a %s", kind.contentType)
	}
	if kind.magic == nil {
		if _, found := redact.Scan(string(data), a.secrets...); found {
			return errors.New("it contains a secret, so it stays off the cloud (the runner does not redact reports)")
		}
	}
	sum := sha256.Sum256(data)
	artifact := execution.Artifact{Name: name, ContentType: kind.contentType, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	if err := artifact.Validate(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	replace := -1
	for index, existing := range a.report.artifacts {
		if existing.Name == name {
			replace = index
		}
	}
	if replace < 0 && len(a.report.artifacts) >= execution.MaxArtifacts {
		return fmt.Errorf("a run publishes at most %d artifacts", execution.MaxArtifacts)
	}
	if a.report.artifactDir == "" {
		return errors.New("this run has no artifact directory")
	}
	if err := os.MkdirAll(a.report.artifactDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(a.report.artifactDir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	entry := Artifact{Artifact: artifact, Path: path}
	if replace >= 0 {
		a.report.artifacts[replace] = entry
	} else {
		a.report.artifacts = append(a.report.artifacts, entry)
	}
	a.log("artifact %s accepted (%d bytes, %s)", name, len(data), kind.contentType)
	return nil
}

// readSource reads a file the agent named, only from the worktree or the
// run's artifact directory, and only a regular file of at most
// MaxArtifactBytes: an agent cannot publish the worker's home through it.
func (a *agentSession) readSource(source string) ([]byte, error) {
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return nil, err
	}
	allowed := false
	for _, root := range a.report.sources {
		if root == "" {
			continue
		}
		if real, err := filepath.EvalSymlinks(root); err == nil {
			root = real
		}
		if rel, err := filepath.Rel(root, resolved); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			allowed = true
		}
	}
	if !allowed {
		return nil, errors.New("the file must be in the worktree or in $" + agentlink.EnvArtifactDir)
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	return io.ReadAll(io.LimitReader(file, execution.MaxArtifactBytes+1))
}

// reportOf answers what the agent reported, replies in the claim's order.
func (a *agentSession) reportOf() (findings []execution.Finding, verdict string, replies []execution.ReviewReply, artifacts []Artifact) {
	if a == nil {
		return nil, "", nil, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range a.report.comments {
		if reply, ok := a.report.replies[id]; ok {
			replies = append(replies, execution.ReviewReply{ID: id, Reply: reply})
		}
	}
	return append([]execution.Finding(nil), a.report.findings...), a.report.verdict, replies, append([]Artifact(nil), a.report.artifacts...)
}
