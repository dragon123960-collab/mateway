package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func runSkillCommand(_ context.Context, args []string, stdout io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("usage: mateway skill create <cli|api> <name>")
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "create":
		if len(args) < 3 {
			return errors.New("usage: mateway skill create <cli|api> <name>")
		}
		kind := strings.ToLower(strings.TrimSpace(args[1]))
		name := strings.TrimSpace(args[2])
		if name == "" {
			return errors.New("skill name is required")
		}
		path, err := scaffoldSkill(cfg.App.Workspace, kind, name)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "created skill scaffold %s\n", path)
		return nil
	default:
		return fmt.Errorf("unknown skill subcommand: %s", args[0])
	}
}

func scaffoldSkill(workspace, kind, name string) (string, error) {
	dir := filepath.Join(workspace, "skills", sanitizeSkillDir(name))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, subdir := range []string{"references", "assets"} {
		if err := os.MkdirAll(filepath.Join(dir, subdir), 0o755); err != nil {
			return "", err
		}
	}
	skillDoc := buildSkillMarkdown(kind, name)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillDoc), 0o644); err != nil {
		return "", err
	}
	meta := buildSkillMeta(kind, name)
	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "_meta.json"), metaData, 0o644); err != nil {
		return "", err
	}
	if kind == "cli" {
		if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
			return "", err
		}
		scriptPath := filepath.Join(dir, "scripts", "run.sh")
		if err := os.WriteFile(scriptPath, []byte("#!/usr/bin/env bash\nset -euo pipefail\n\necho \"replace this with your real CLI wrapper\"\n"), 0o755); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func buildSkillMarkdown(kind, name string) string {
	description := "Describe what this skill does."
	switch kind {
	case "cli":
		description = "Run a local CLI or script through a SKILL.md + _meta.json executable binding."
	case "api":
		description = "Call an HTTP API through a SKILL.md + _meta.json executable binding."
	}
	return fmt.Sprintf(`---
name: %s
description: %s
version: 0.1.0
---

# %s

## What This Skill Does

Explain when the agent should use this skill.

## Inputs

- Describe the expected inputs.

## Output

- Describe the expected output.

## Resources

- Put executable helpers under scripts/.
- Put reference docs under references/.
- Put images or binary assets under assets/.
`, name, description, strings.Title(name))
}

func buildSkillMeta(kind, name string) map[string]any {
	mateway := map[string]any{
		"type":       kind,
		"read_only":  true,
		"risk_level": "low",
		"tags":       []string{"skill", kind, sanitizeSkillDir(name)},
	}
	switch kind {
	case "cli":
		mateway["entry"] = "./scripts/run.sh"
	case "api":
		mateway["method"] = "GET"
		mateway["url"] = "https://example.com/api"
	}
	return map[string]any{
		"slug":    sanitizeSkillDir(name),
		"version": "0.1.0",
		"mateway": mateway,
	}
}

func sanitizeSkillDir(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	name = strings.ReplaceAll(name, " ", "-")
	name = strings.ReplaceAll(name, "_", "-")
	return name
}
