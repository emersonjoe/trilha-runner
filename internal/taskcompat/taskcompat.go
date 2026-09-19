package taskcompat

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/emersonjoe/trilha-spec/spec"
	"github.com/emersonjoe/trilha-spec/task"
)

func Get(store *task.Store, id string) (*task.Task, error) {
	content, err := os.ReadFile(store.Layout.TaskFile(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("task %s: not found", id)
		}
		return nil, err
	}
	return Parse(content)
}

func List(store *task.Store) ([]*task.Task, error) {
	entries, err := os.ReadDir(store.Layout.Tasks())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var tasks []*task.Task
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(store.Layout.Tasks(), entry.Name()))
		if err != nil {
			return nil, err
		}
		item, err := Parse(content)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		if strings.TrimSuffix(entry.Name(), ".md") != item.ID {
			return nil, fmt.Errorf("%s: file name and id %q disagree", entry.Name(), item.ID)
		}
		tasks = append(tasks, item)
	}
	sort.Slice(tasks, func(left, right int) bool { return tasks[left].ID < tasks[right].ID })
	return tasks, nil
}

func Parse(content []byte) (*task.Task, error) {
	original, err := spec.Parse(content)
	if err != nil {
		return nil, err
	}
	dependencies := original.Fields.GetList("depends_on")
	if !hasRemote(dependencies) {
		return task.Parse(content)
	}
	sanitized, err := spec.Parse(content)
	if err != nil {
		return nil, err
	}
	var local []string
	for _, dependency := range dependencies {
		if !strings.Contains(dependency, ":") {
			local = append(local, dependency)
		}
	}
	sanitized.Fields.SetList("depends_on", local)
	item, err := task.Parse(sanitized.Bytes())
	if err != nil {
		return nil, err
	}
	item.DependsOn = dependencies
	item.Fields = original.Fields
	return item, nil
}

func Save(store *task.Store, item *task.Task) error {
	return os.WriteFile(store.Layout.TaskFile(item.ID), item.Bytes(), 0o644)
}

func Move(store *task.Store, item *task.Task, to task.Status) error {
	if !hasRemote(item.DependsOn) {
		_, err := store.Move(item.ID, to)
		return err
	}
	copy := *item
	copy.DependsOn = localDependencies(item.DependsOn)
	if err := copy.Move(to); err != nil {
		return err
	}
	item.Status = copy.Status
	item.Updated = copy.Updated
	return Save(store, item)
}

func hasRemote(dependencies []string) bool {
	for _, dependency := range dependencies {
		if strings.Contains(dependency, ":") {
			return true
		}
	}
	return false
}

func localDependencies(dependencies []string) []string {
	var local []string
	for _, dependency := range dependencies {
		if !strings.Contains(dependency, ":") {
			local = append(local, dependency)
		}
	}
	return local
}
