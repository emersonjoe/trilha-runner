package driver

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-spec/agent"
	"github.com/emersonjoe/trilha-spec/task"
)

func job(t *testing.T) Job {
	t.Helper()
	return Job{Task: &task.Task{ID: "TASK-001", Title: "x"}, Prompt: "# Task TASK-001 — x\n\nbody", Dir: t.TempDir()}
}

func TestEcho(t *testing.T) {
	j := job(t)
	d, err := New("echo")
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Execute(context.Background(), j)
	if err != nil || !strings.Contains(out.Text, "TASK-001") {
		t.Fatalf("%+v %v", out, err)
	}
	if b, _ := os.ReadFile(filepath.Join(j.Dir, "TRILHA_RUN.md")); string(b) != "# Task TASK-001 — x\n" {
		t.Fatalf("file = %q", b)
	}
	if _, err := New("nope"); err == nil {
		t.Fatal("unknown driver accepted")
	}
}

func TestExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	j := job(t)
	j.Command = `sh -c "cat > prompt.txt; echo done $TRILHA_TASK"`
	out, err := Exec{}.Execute(context.Background(), j)
	if err != nil || strings.TrimSpace(out.Text) != "done TASK-001" || out.Meta["exit_code"] != "0" {
		t.Fatalf("%+v %v", out, err)
	}
	if b, _ := os.ReadFile(filepath.Join(j.Dir, "prompt.txt")); string(b) != j.Prompt {
		t.Fatalf("prompt = %q", b)
	}
	j.Command = `sh -c "exit 4"`
	if out, err := (Exec{}).Execute(context.Background(), j); err == nil || out.Meta["exit_code"] != "4" {
		t.Fatalf("%+v %v", out, err)
	}
	j.Command = ""
	j.Agent = &agent.Agent{Name: "a", Role: "r", Command: "true"}
	if _, err := (Exec{}).Execute(context.Background(), j); err != nil {
		t.Fatal(err)
	}
}

func TestToolsStayInside(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("hi"), 0o644)
	ctx := context.Background()
	if out, err := readFile(root).Func(ctx, []byte(`{"path":"a.txt"}`)); err != nil || out != "hi" {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := readFile(root).Func(ctx, []byte(`{"path":"../etc/passwd"}`)); err == nil {
		t.Fatal("escaped the worktree")
	}
	if _, err := writeFile(root).Func(ctx, []byte(`{"path":"sub/b.txt","content":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if out, _ := listFiles(root).Func(ctx, []byte(`{}`)); !strings.Contains(out, "sub/b.txt") {
		t.Fatalf("list = %q", out)
	}
	if runtime.GOOS != "windows" {
		if out, _ := runCommand(Job{Dir: root}).Func(ctx, []byte(`{"command":"sh -c \"exit 2\""}`)); !strings.Contains(out, `"exit_code":2`) {
			t.Fatalf("run = %q", out)
		}
	}
}
