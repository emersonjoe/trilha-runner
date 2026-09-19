package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "trilha-runner-e2e")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "trilha-runner")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func sh(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

func TestRunEcho(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := t.TempDir()
	sh(t, dir, "git", "init", "-q", "-b", "main")
	sh(t, dir, "git", "config", "user.email", "t@t")
	sh(t, dir, "git", "config", "user.name", "t")
	os.MkdirAll(filepath.Join(dir, ".trilha", "tasks"), 0o755)
	os.WriteFile(filepath.Join(dir, ".trilha", "project.md"), []byte("---\nname: demo\n---\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".trilha", "tasks", "TASK-001.md"), []byte("---\nid: TASK-001\ntitle: Echo\nstatus: ready\nacceptance:\n  - file exists\nchecks:\n  - \"sh -c \\\"test -f TRILHA_RUN.md\\\"\"\n---\n"), 0o644)
	sh(t, dir, "git", "add", "-A")
	sh(t, dir, "git", "commit", "-q", "-m", "init")
	out := sh(t, dir, bin, "next", "--driver", "echo")
	if !strings.Contains(out, "TASK-001 → review") || !strings.Contains(out, "branch trilha/task-001") {
		t.Fatalf("out:\n%s", out)
	}
	if out := sh(t, dir, bin, "worktree", "list"); !strings.Contains(out, "trilha/task-001") {
		t.Fatalf("list:\n%s", out)
	}
	if out := sh(t, dir, bin, "worktree", "clean", "TASK-001"); !strings.Contains(out, "removed") {
		t.Fatalf("clean:\n%s", out)
	}
	outside := exec.Command(bin, "run", "TASK-001")
	outside.Dir = t.TempDir()
	if out, err := outside.CombinedOutput(); err == nil || !strings.Contains(string(out), "no .trilha") {
		t.Fatalf("outside a project:\n%s", out)
	}
	if out := sh(t, dir, bin, "drivers"); !strings.Contains(out, "ai\nclaude-code\necho\nexec") {
		t.Fatalf("drivers:\n%s", out)
	}
}

func TestNextResolvesSiblingRepository(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dependency := t.TempDir()
	os.MkdirAll(filepath.Join(dependency, ".trilha", "tasks"), 0o755)
	os.WriteFile(filepath.Join(dependency, ".trilha", "project.md"), []byte("---\nname: trilha\n---\n"), 0o644)
	os.WriteFile(filepath.Join(dependency, ".trilha", "tasks", "TASK-001.md"), []byte("---\nid: TASK-001\ntitle: Framework\nstatus: done\n---\n"), 0o644)

	primary := t.TempDir()
	sh(t, primary, "git", "init", "-q", "-b", "main")
	sh(t, primary, "git", "config", "user.email", "t@t")
	sh(t, primary, "git", "config", "user.name", "t")
	os.MkdirAll(filepath.Join(primary, ".trilha", "tasks"), 0o755)
	os.WriteFile(filepath.Join(primary, ".trilha", "project.md"), []byte("---\nname: app\n---\n"), 0o644)
	os.WriteFile(filepath.Join(primary, ".trilha", "tasks", "TASK-001.md"), []byte("---\nid: TASK-001\ntitle: App\nstatus: ready\ndepends_on:\n  - \"trilha:TASK-001\"\nacceptance:\n  - file exists\nchecks:\n  - \"sh -c \\\"test -f TRILHA_RUN.md\\\"\"\n---\n"), 0o644)
	sh(t, primary, "git", "add", "-A")
	sh(t, primary, "git", "commit", "-q", "-m", "init")

	blocked := exec.Command(bin, "next", "--driver", "echo")
	blocked.Dir = primary
	if output, err := blocked.CombinedOutput(); err == nil || !strings.Contains(string(output), "waiting:trilha:TASK-001") {
		t.Fatalf("unresolved dependency: err=%v output=%s", err, output)
	}
	output := sh(t, primary, bin, "next", "--driver", "echo", "--repo", "trilha="+dependency)
	if !strings.Contains(output, "TASK-001 → review") {
		t.Fatalf("output:\n%s", output)
	}
}
