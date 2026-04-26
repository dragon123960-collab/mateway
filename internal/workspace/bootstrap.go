package workspace

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/dongping/mateway/internal/config"
)

//go:embed templates/*
var defaultFiles embed.FS

func Init(cfg config.Config) error {
	if err := config.EnsureLayout(cfg.App.Home); err != nil {
		return err
	}
	dirs := []string{
		cfg.App.Home,
		cfg.App.Workspace,
		filepath.Join(cfg.App.Home, "skills"),
		filepath.Join(cfg.App.Workspace, "skills"),
		filepath.Join(cfg.App.Workspace, "memory", "sessions"),
		filepath.Join(cfg.App.Workspace, "memory", "runs"),
		filepath.Join(cfg.App.Workspace, "memory", "reflections"),
		filepath.Join(cfg.App.Workspace, "memory", "summaries"),
		filepath.Join(cfg.App.Workspace, "memory", "knowledge"),
		filepath.Join(cfg.App.Workspace, "memory", "agents"),
		filepath.Join(cfg.App.Workspace, "memory", "wiki"),
		filepath.Join(cfg.App.Workspace, "memory", "wiki", "entities"),
		filepath.Join(cfg.App.Workspace, "memory", "wiki", "concepts"),
		filepath.Join(cfg.App.Workspace, "memory", "wiki", "notes"),
		filepath.Join(cfg.App.Workspace, "memory", "wiki", "sources"),
		filepath.Join(cfg.App.Workspace, "agents"),
		filepath.Join(cfg.App.Workspace, "workflows"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create dir %s: %w", dir, err)
		}
	}
	if err := config.Save(filepath.Join(cfg.App.Home, "config", "config.yaml"), cfg); err != nil {
		return err
	}
	entries, err := fs.ReadDir(defaultFiles, "templates")
	if err != nil {
		return fmt.Errorf("read default workspace templates: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := defaultFiles.ReadFile(filepath.Join("templates", entry.Name()))
		if err != nil {
			return fmt.Errorf("read template %s: %w", entry.Name(), err)
		}
		target := filepath.Join(cfg.App.Workspace, entry.Name())
		if _, err := os.Stat(target); err == nil {
			continue
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return fmt.Errorf("write template %s: %w", entry.Name(), err)
		}
	}
	defaultAgent := filepath.Join(cfg.App.Workspace, "agents", "default.md")
	if _, err := os.Stat(defaultAgent); os.IsNotExist(err) {
		content := `---
name: default
description: default workspace agent
builtin_tools:
  - read_file
  - list_files
  - search_text
  - search_history
can_spawn: true
async_allowed: true
memory_policy: default
path_policy: workspace
channel_visibility: coordinator
collaboration_mode: coordinator
---

# Default Agent

This agent coordinates the workspace and can delegate to specialized agents when needed.
`
		if err := os.WriteFile(defaultAgent, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write default agent: %w", err)
		}
	}
	return nil
}
