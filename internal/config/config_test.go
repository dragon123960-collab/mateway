package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

func TestModelConfigSupportsModalityDefaultsToText(t *testing.T) {
	cfg := ModelConfig{}
	if !cfg.SupportsModality("text") {
		t.Fatal("expected missing modalities to support text")
	}
	if cfg.SupportsModality("image") {
		t.Fatal("expected missing modalities not to support image")
	}
	cfg.Modalities = []string{"text", "image", "audio"}
	if !cfg.SupportsModality("image") || !cfg.SupportsModality("audio") {
		t.Fatalf("expected configured modalities to be supported: %#v", cfg.Modalities)
	}
}

func TestModelConfigMaxTokensDefaults(t *testing.T) {
	if got := (ModelConfig{}).MaxTokensValue(); got != 4096 {
		t.Fatalf("default max tokens = %d", got)
	}
	if got := (ModelConfig{MaxTokens: 8192}).MaxTokensValue(); got != 8192 {
		t.Fatalf("configured max tokens = %d", got)
	}
}

func TestExecutionConfigDefaults(t *testing.T) {
	root := Root{}
	root.NormalizeForUse()
	if root.Execution.MaxParallelTools != 4 {
		t.Fatalf("default max parallel tools = %d", root.Execution.MaxParallelTools)
	}
	root = Root{Execution: ExecutionConfig{MaxParallelTools: 1}}
	root.NormalizeForUse()
	if root.Execution.MaxParallelTools != 1 {
		t.Fatalf("configured max parallel tools = %d", root.Execution.MaxParallelTools)
	}
}

func TestModelRolesAcceptStringAndList(t *testing.T) {
	var cfg struct {
		Model ModelSelection `yaml:"model"`
	}
	if err := yaml.Unmarshal([]byte(`
model:
  roles:
    vision:
      - glm-4.6v-flash
      - minimax
    strong: minimax
`), &cfg); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.Model.Roles.Models("vision"), ","); got != "glm-4.6v-flash,minimax" {
		t.Fatalf("vision roles = %q", got)
	}
	if got := strings.Join(cfg.Model.Roles.Models("strong"), ","); got != "minimax" {
		t.Fatalf("strong roles = %q", got)
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
		filepath.Join("channels", "weixin.yaml"),
		filepath.Join("channels", "weixin.sample.yaml"),
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
	assertPromptTemplate(t, filepath.Join(home, "workspace", "agents", "main", "agent.md"), []string{"# Main Assistant agent", "## Operating Rules", "Do not claim a tool"})
	assertPromptTemplate(t, filepath.Join(home, "workspace", "agents", "main", "soul.md"), []string{"# Main Assistant soul", "You are Main Assistant", "## Boundaries"})
	assertPromptTemplate(t, filepath.Join(home, "workspace", "agents", "main", "user.md"), []string{"No stable user preferences recorded yet.", "## Communication Preferences", "Do not store passwords"})
}

func TestDefaultConfigLocale(t *testing.T) {
	cfg := DefaultRoot()
	cfg.NormalizeForUse()
	if cfg.App.Locale != "auto" {
		t.Fatalf("expected locale auto, got %q", cfg.App.Locale)
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

func TestEnsureDefaultConfigFilesPreservesExistingConfigValues(t *testing.T) {
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
	text := string(data)
	if !strings.Contains(text, "name: custom") {
		t.Fatalf("expected existing config value to stay untouched, got %q", text)
	}
}

func TestEnsureDefaultConfigFilesPreservesExistingAgentProfileFiles(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "workspace", "agents", "main", "user.md")
	writeFile(t, target, "custom user profile")

	if err := EnsureDefaultConfigFiles(home); err != nil {
		t.Fatalf("ensure default config files: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read user profile: %v", err)
	}
	if string(data) != "custom user profile\n" {
		t.Fatalf("expected custom profile preserved, got %q", string(data))
	}
}

func TestEnsureDefaultConfigFilesMergesMissingConfigKeys(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "config", "config.yaml")
	writeFile(t, configPath, "app:\n  name: custom\nscheduler:\n  enabled: true\n")

	if err := EnsureDefaultConfigFiles(home); err != nil {
		t.Fatalf("ensure default config files: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "name: custom") || !strings.Contains(text, "enabled: true") {
		t.Fatalf("existing values were not preserved:\n%s", text)
	}
	for _, want := range []string{"terminal_sandbox:", "interval: 30s", "skills:", "search:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing merged key %q in:\n%s", want, text)
		}
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

func assertPromptTemplate(t *testing.T, path string, want []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(data) > 2048 {
		t.Fatalf("expected %s to stay within prompt context limit, got %d bytes", path, len(data))
	}
	text := string(data)
	for _, part := range want {
		if !strings.Contains(text, part) {
			t.Fatalf("expected %s to contain %q:\n%s", path, part, text)
		}
	}
}
