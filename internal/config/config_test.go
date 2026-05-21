package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadModelsSkipsSampleAndExampleFiles(t *testing.T) {
	home := t.TempDir()
	modelsDir := filepath.Join(home, "config", "models")
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatalf("mkdir models: %v", err)
	}
	writeFile(t, filepath.Join(modelsDir, "minimax.yaml"), `
name: minimax
provider: minimax
api: anthropic
model: MiniMax-M2.7
enabled: true
`)
	writeFile(t, filepath.Join(modelsDir, "minimax.sample.yaml"), `
name: minimax
provider: minimax
api: anthropic
model: sample-should-not-load
enabled: true
`)
	writeFile(t, filepath.Join(modelsDir, "local.example.yaml"), `
name: local-mlx
provider: mlx_lm
api: openai
model: example-should-not-load
enabled: true
`)

	models, err := NewLoader(home).loadModels()
	if err != nil {
		t.Fatalf("load models: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected 1 real model, got %d: %#v", len(models), models)
	}
	if models[0].Name != "minimax" || models[0].Model != "MiniMax-M2.7" {
		t.Fatalf("unexpected model loaded: %#v", models[0])
	}
}

func TestLoadModelsRejectsDuplicateEnabledNames(t *testing.T) {
	home := t.TempDir()
	modelsDir := filepath.Join(home, "config", "models")
	if err := os.MkdirAll(modelsDir, 0o755); err != nil {
		t.Fatalf("mkdir models: %v", err)
	}
	writeFile(t, filepath.Join(modelsDir, "a.yaml"), `
name: minimax
provider: minimax
api: anthropic
model: MiniMax-M2.7
enabled: true
`)
	writeFile(t, filepath.Join(modelsDir, "b.yaml"), `
name: MiniMax
provider: minimax
api: anthropic
model: MiniMax-M2.7
enabled: true
`)

	_, err := NewLoader(home).loadModels()
	if err == nil {
		t.Fatal("expected duplicate model error")
	}
	if !strings.Contains(err.Error(), `duplicate enabled model name`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureDefaultConfigFilesCreatesSamplesAndRealConfig(t *testing.T) {
	home := t.TempDir()

	if err := EnsureDefaultConfigFiles(home); err != nil {
		t.Fatalf("ensure default config files: %v", err)
	}

	paths := []string{
		"README.md",
		"config.yaml",
		"config.sample.yaml",
		"mateway.env.sample",
		filepath.Join("models", "minimax.yaml"),
		filepath.Join("models", "minimax.sample.yaml"),
		filepath.Join("models", "local-mlx.yaml"),
		filepath.Join("models", "local-mlx.sample.yaml"),
		filepath.Join("channels", "_README.md"),
		filepath.Join("channels", "feishu.yaml"),
		filepath.Join("channels", "feishu.sample.yaml"),
	}
	for _, rel := range paths {
		path := filepath.Join(home, "config", rel)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
	memoryPaths := []string{
		"README.md",
		"schema.md",
		"index.md",
		"log.md",
		filepath.Join("user", "index.md"),
		filepath.Join("org", "index.md"),
		filepath.Join("agents", "main", "memory.md"),
		filepath.Join("agents", "main", "index.md"),
	}
	for _, rel := range memoryPaths {
		path := filepath.Join(home, "workspace", "memory", rel)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
}

func TestEnsureDefaultConfigFilesDoesNotOverwriteExistingConfig(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "config", "config.yaml")
	writeFile(t, configPath, "app:\n  name: custom\n")

	if err := EnsureDefaultConfigFiles(home); err != nil {
		t.Fatalf("ensure default config files: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(data) != "app:\n  name: custom\n" {
		t.Fatalf("expected existing config to stay untouched, got %q", string(data))
	}
}

func writeFile(t *testing.T, path string, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(text)+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
