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
		filepath.Join("models", "openai-gpt54-mini.yaml"),
		filepath.Join("models", "openai-gpt54-mini.sample.yaml"),
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
	agentPaths := []string{
		"agent.md",
		"soul.md",
		"user.md",
		"tools.md",
		"memory.md",
		filepath.Join("skills", "README.md"),
	}
	for _, rel := range agentPaths {
		path := filepath.Join(home, "workspace", "agents", "main", rel)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
}

func TestEnsureDefaultConfigFilesSeedsEditableDefaultSkills(t *testing.T) {
	home := t.TempDir()
	if err := EnsureDefaultConfigFiles(home); err != nil {
		t.Fatalf("ensure default config files: %v", err)
	}
	for _, name := range []string{"software-install", "fresh-search", "source-evaluation", "connector-gap"} {
		path := filepath.Join(home, "workspace", "skills", name, "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected default skill %s to exist: %v", path, err)
		}
	}
	defaultAgentSkill := filepath.Join(home, "workspace", "agents", "main", "skills", "fresh-search", "SKILL.md")
	if _, err := os.Stat(defaultAgentSkill); err == nil {
		t.Fatalf("init should not install default agent skills at %s", defaultAgentSkill)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat default agent skill: %v", err)
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

func TestDefaultConfigIncludesSearXNGProvider(t *testing.T) {
	home := t.TempDir()
	if err := EnsureDefaultConfigFiles(home); err != nil {
		t.Fatal(err)
	}
	cfg, err := NewLoader(home).Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Search.Providers.SearXNG.BaseURL != "http://127.0.0.1:8088" {
		t.Fatalf("searxng config = %#v", cfg.Search.Providers.SearXNG)
	}
}

func TestLoadNormalizesEnabledSearchProviderOrder(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(configDir, "config.yaml"), `
app:
  name: mateway
search:
  providers:
    tavily:
      enabled: true
    searxng:
      enabled: true
    duckduckgo:
      enabled: true
agents:
  profiles:
    - id: main
`)
	cfg, err := NewLoader(home).Load()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(cfg.Search.ProviderOrder, ",")
	if got != "tavily,searxng,duckduckgo" {
		t.Fatalf("provider order = %q", got)
	}
}

func TestSearchProviderResolvedAPIKeyReadsStandardEnv(t *testing.T) {
	t.Setenv("TAVILY_API_KEY", "tvly-standard")
	cfg := SearchProviderConfig{APIKeyEnv: "TAVILY_API_KEY"}
	if got := cfg.ResolvedAPIKey(); got != "tvly-standard" {
		t.Fatalf("expected standard env key, got %q", got)
	}
}

func TestSearchProviderResolvedAPIKeyReadsMatewayPrefixedFallback(t *testing.T) {
	t.Setenv("MATEWAY_TAVILY_API_KEY", "tvly-prefixed")
	cfg := SearchProviderConfig{APIKeyEnv: "TAVILY_API_KEY"}
	if got := cfg.ResolvedAPIKey(); got != "tvly-prefixed" {
		t.Fatalf("expected prefixed fallback env key, got %q", got)
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
