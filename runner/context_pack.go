package runner

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/emersonjoe/trilha-runner/internal/taskcompat"
	"github.com/emersonjoe/trilha-spec/agent"
	"github.com/emersonjoe/trilha-spec/ai"
	"github.com/emersonjoe/trilha-spec/spec"
	"github.com/emersonjoe/trilha-spec/task"
)

func buildContextPack(layout spec.Layout, item *task.Task, project *spec.Project, manifest *agent.Agent) (*ai.Pack, error) {
	pack := &ai.Pack{Task: item, Project: project, Agent: manifest}
	var err error
	if pack.Constitution, err = layout.LoadConstitution(); err != nil {
		return nil, err
	}
	if item.Spec != "" {
		if specification, loadErr := layout.LoadSpec(item.Spec); loadErr == nil {
			pack.Spec = specification
		}
	}
	store := &task.Store{Layout: layout}
	for _, dependency := range item.DependsOn {
		if strings.Contains(dependency, ":") {
			continue
		}
		if current, loadErr := taskcompat.Get(store, dependency); loadErr == nil {
			pack.Dependencies = append(pack.Dependencies, current)
		}
	}
	evidence, err := task.ListEvidence(layout, item.ID)
	if err != nil {
		return nil, err
	}
	if len(evidence) > 0 {
		keys, err := task.ProjectKeys(layout)
		if err != nil {
			return nil, err
		}
		pack.Evidence = keys.CheckAll(evidence)
	}
	if entries, err := os.ReadDir(layout.Context()); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			content, err := os.ReadFile(filepath.Join(layout.Context(), entry.Name()))
			if err != nil {
				return nil, err
			}
			if pack.Context == nil {
				pack.Context = map[string]string{}
			}
			pack.Context[entry.Name()] = string(content)
		}
	}
	return pack, nil
}
