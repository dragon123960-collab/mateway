package skill

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed defaults/workspace/**
var defaultSkillsFS embed.FS

func EnsureWorkspaceLayout(home, workspace string) error {
	dirs := []string{
		filepath.Join(home, "config"),
		filepath.Join(home, "logs"),
		filepath.Join(home, "run"),
		filepath.Join(home, "trace"),
		workspace,
		filepath.Join(workspace, "skills"),
		filepath.Join(workspace, "agents"),
		filepath.Join(workspace, "agents", "main"),
		filepath.Join(workspace, "agents", "main", "skills"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create dir %s: %w", dir, err)
		}
	}
	return ensureEmbeddedDefaults(workspace)
}

func ensureEmbeddedDefaults(workspace string) error {
	root := "defaults/workspace"
	return copyEmbeddedDir(defaultSkillsFS, root, workspace)
}

func LoadRegistry(workspace, agentID string) (*Registry, error) {
	reg := NewRegistry()
	for _, root := range discoveryRoots(workspace, agentID) {
		if err := loadDirIntoRegistry(reg, root); err != nil {
			return nil, err
		}
	}
	if len(reg.Names()) == 0 {
		return NewBuiltinRegistry(), nil
	}
	return reg, nil
}

func discoveryRoots(workspace, agentID string) []string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		agentID = "main"
	}
	return []string{
		filepath.Join(workspace, "skills"),
		filepath.Join(workspace, "agents", agentID, "skills"),
	}
}

func loadDirIntoRegistry(reg *Registry, root string) error {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read skill dir %s: %w", root, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		def, err := LoadDefinition(filepath.Join(root, entry.Name()))
		if err != nil {
			return err
		}
		if def.Name == "" {
			continue
		}
		reg.Register(def)
	}
	return nil
}

func LoadDefinition(dir string) (Definition, error) {
	skillPath := filepath.Join(dir, "SKILL.md")
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return Definition{}, fmt.Errorf("read skill %s: %w", skillPath, err)
	}
	meta, body := parseSkillMarkdown(string(data))
	return Definition{
		Name:             meta.Name,
		Description:      meta.Description,
		Tags:             meta.Tags,
		Priority:         meta.Priority,
		Stage:            meta.Stage,
		Scope:            meta.Scope,
		WhenContains:     meta.WhenContains,
		WhenResultKinds:  meta.WhenResultKinds,
		WhenUserLanguage: meta.WhenUserLanguage,
		UseFor:           meta.UseFor,
		Produces:         meta.Produces,
		AcceptanceMode:   meta.AcceptanceMode,
		ParallelMode:     meta.ParallelMode,
		AcceptancePrompt: meta.AcceptancePrompt,
		Instruction:      strings.TrimSpace(body),
		Dir:              dir,
	}, nil
}
