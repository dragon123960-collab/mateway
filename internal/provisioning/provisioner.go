package provisioning

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dongping/mateway/internal/config"
)

type WorkspaceProvisioner interface {
	CreateWorkspace(name string) (string, error)
	ListWorkspaces() ([]string, error)
	CreateAgent(workspacePath, name, description string) (string, error)
	ListAgents(workspacePath string) ([]string, error)
	CreateChannel(name, kind string) (string, error)
}

type Provisioner struct {
	Config config.Config
}

func (p Provisioner) CreateWorkspace(name string) (string, error) {
	name = sanitizeName(name)
	if name == "" {
		return "", fmt.Errorf("workspace name is required")
	}
	root := filepath.Join(p.Config.App.Home, "workspaces", name)
	for _, dir := range []string{
		root,
		filepath.Join(root, "agents"),
		filepath.Join(root, "skills"),
		filepath.Join(root, "memory", "sessions"),
		filepath.Join(root, "memory", "runs"),
		filepath.Join(root, "memory", "reflections"),
		filepath.Join(root, "memory", "summaries"),
		filepath.Join(root, "memory", "knowledge"),
		filepath.Join(root, "memory", "agents"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	files := map[string]string{
		filepath.Join(root, "SOUL.md"):  "# SOUL\n\nDescribe the workspace personality here.\n",
		filepath.Join(root, "AGENT.md"): "# AGENT\n\nDescribe the default execution behavior here.\n",
		filepath.Join(root, "USER.md"):  "# USER\n\nDescribe the primary user or audience here.\n",
	}
	for path, content := range files {
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return "", err
		}
	}
	return root, nil
}

func (p Provisioner) ListWorkspaces() ([]string, error) {
	root := filepath.Join(p.Config.App.Home, "workspaces")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			out = append(out, entry.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func (p Provisioner) CreateAgent(workspacePath, name, description string) (string, error) {
	name = sanitizeName(name)
	if name == "" {
		return "", fmt.Errorf("agent name is required")
	}
	dir := filepath.Join(workspacePath, "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".md")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	body := fmt.Sprintf(`---
name: %s
description: %s
builtin_tools:
  - read_file
  - list_files
  - search_text
can_spawn: false
async_allowed: false
memory_policy: default
path_policy: workspace
channel_visibility: coordinator
collaboration_mode: coordinator
---

# %s

Describe this agent's role, specialty, and collaboration style here.
`, name, firstNonEmpty(description, "workspace agent"), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func (p Provisioner) ListAgents(workspacePath string) ([]string, error) {
	dir := filepath.Join(workspacePath, "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			out = append(out, strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
		}
	}
	sort.Strings(out)
	return out, nil
}

func (p Provisioner) CreateChannel(name, kind string) (string, error) {
	name = sanitizeName(name)
	kind = sanitizeName(kind)
	if name == "" || kind == "" {
		return "", fmt.Errorf("channel name and kind are required")
	}
	dir := filepath.Join(p.Config.App.Home, "config", "channels")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name+".yaml")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	content := fmt.Sprintf("%s:\n  enabled: false\n", kind)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func sanitizeName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, " ", "-")
	value = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_' || r == '.':
			return r
		default:
			return -1
		}
	}, value)
	return strings.Trim(value, "-_.")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
