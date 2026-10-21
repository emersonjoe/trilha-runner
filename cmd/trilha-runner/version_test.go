package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// reRelease is a released section heading of the changelog: `## 0.3.2 — …`.
// An "Unreleased" heading deliberately does not match, so work in progress
// does not have to move the constant.
var reRelease = regexp.MustCompile(`(?m)^## (\d+\.\d+\.\d+)`)

// The version the binary reports is the version that was released.
//
// It drifted once and quietly: 0.3.1 shipped with the constant still saying
// 0.3.0, so `trilha-runner version`, the usage banner and — worse — the
// `RunnerVersion` and `DriverVersions` every worker announces on its
// heartbeat and its claim all named a version that was not running. A fleet
// that misreports itself is a fleet nobody can route or debug by version, so
// the two are tied together here rather than by remembering.
func TestVersionMatchesTheNewestChangelogEntry(t *testing.T) {
	changelog, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	newest := reRelease.FindSubmatch(changelog)
	if newest == nil {
		t.Fatal("CHANGELOG.md has no released section like `## 0.3.2`")
	}
	if got := string(newest[1]); got != version {
		t.Fatalf("const version is %q but the newest CHANGELOG entry is %q: bump one of them", version, got)
	}
}

// reSemver is the shape of a released version: `0.4.0`, never `v0.4.0`.
var reSemver = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// TestVersionMatchesTag is the release gate. release.yml runs it with
// RELEASE_TAG set to the tag that triggered it, before building anything: a
// tag `v0.5.0` on a tree whose binary says `0.4.0` would publish four
// binaries that misreport themselves to every fleet they join, so the
// workflow stops here instead. Without RELEASE_TAG — every ordinary run — the
// test only checks the version has the shape a tag can carry, so the suite
// never depends on how it was invoked.
func TestVersionMatchesTag(t *testing.T) {
	if !reSemver.MatchString(version) {
		t.Fatalf("version %q is not MAJOR.MINOR.PATCH", version)
	}
	tag := os.Getenv("RELEASE_TAG")
	if tag == "" {
		return
	}
	want, ok := strings.CutPrefix(tag, "v")
	if !ok || want != version {
		t.Fatalf("RELEASE_TAG is %q but the binary says %q: tag v%s, or bump version in cmd/trilha-runner/main.go", tag, version, version)
	}
}
