package taskcompat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersonjoe/trilha-spec/spec"
	"github.com/emersonjoe/trilha-spec/task"
)

const crossRepository = "---\nid: TASK-002\ntitle: app\nstatus: ready\ndepends_on:\n  - TASK-001\n  - \"trilha:TASK-007\"\nacceptance:\n  - done\n---\n"

func store(t *testing.T) *task.Store {
	t.Helper()
	layout, _, err := spec.Init(t.TempDir(), spec.InitOptions{Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	return &task.Store{Layout: layout}
}

func TestParseKeepsCrossRepositoryDependencies(t *testing.T) {
	item, err := Parse([]byte(crossRepository))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(item.DependsOn, ",") != "TASK-001,trilha:TASK-007" {
		t.Fatalf("depends_on = %v", item.DependsOn)
	}
	if got := item.Fields.GetList("depends_on"); len(got) != 2 {
		t.Fatalf("fields lost the remote dependency: %v", got)
	}
	if got := localDependencies(item.DependsOn); strings.Join(got, ",") != "TASK-001" {
		t.Fatalf("local = %v", got)
	}
}

func TestParseLocalOnlyIsTheProtocolParse(t *testing.T) {
	content := []byte("---\nid: TASK-001\ntitle: a\nstatus: ready\nacceptance:\n  - ok\n---\n")
	item, err := Parse(content)
	if err != nil || item.ID != "TASK-001" || item.Status != task.Ready {
		t.Fatalf("item=%+v err=%v", item, err)
	}
	if _, err := Parse([]byte("no front matter")); err == nil {
		t.Fatal("content without front matter was accepted")
	}
}

func TestGetAndList(t *testing.T) {
	st := store(t)
	if _, err := Get(st, "TASK-404"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing task: %v", err)
	}
	if _, err := st.Create("framework", func(x *task.Task) { x.Status = task.Done }); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(st.Layout.TaskFile("TASK-002"), []byte(crossRepository), 0o644)
	os.WriteFile(filepath.Join(st.Layout.Tasks(), "README.txt"), []byte("not a task"), 0o644)

	item, err := Get(st, "TASK-002")
	if err != nil || len(item.DependsOn) != 2 {
		t.Fatalf("get: %+v %v", item, err)
	}
	all, err := List(st)
	if err != nil || len(all) != 2 || all[0].ID != "TASK-001" || all[1].ID != "TASK-002" {
		t.Fatalf("list: %+v %v", all, err)
	}

	// A file whose name disagrees with its id is refused, not guessed.
	os.WriteFile(st.Layout.TaskFile("TASK-003"), []byte(crossRepository), 0o644)
	if _, err := List(st); err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Fatalf("mismatched id: %v", err)
	}
}

func TestMoveWithCrossRepositoryDependencyKeepsIt(t *testing.T) {
	st := store(t)
	os.WriteFile(st.Layout.TaskFile("TASK-002"), []byte(crossRepository), 0o644)
	item, err := Get(st, "TASK-002")
	if err != nil {
		t.Fatal(err)
	}
	if err := Move(st, item, task.Running); err != nil {
		t.Fatal(err)
	}
	saved, err := Get(st, "TASK-002")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != task.Running || strings.Join(saved.DependsOn, ",") != "TASK-001,trilha:TASK-007" {
		t.Fatalf("saved = status %s depends_on %v", saved.Status, saved.DependsOn)
	}
	// The protocol's transitions still apply.
	if err := Move(st, saved, task.Done); err == nil {
		t.Fatal("running → done skipped review")
	}
}
