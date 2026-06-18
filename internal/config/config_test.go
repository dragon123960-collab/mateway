package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestTimezoneLocationUsesConfiguredTimezone(t *testing.T) {
	loc, name := TimezoneLocation("UTC")
	if name != "UTC" || loc.String() != "UTC" {
		t.Fatalf("expected UTC location, got %q %q", name, loc.String())
	}
}

func TestTimezoneLocationFallsBackToDefault(t *testing.T) {
	_, name := TimezoneLocation("not/a-zone")
	if name != DefaultTimezone() {
		t.Fatalf("expected default timezone fallback, got %q", name)
	}
}

func TestFeishuAccountConfigsOverlayBaseConfig(t *testing.T) {
	disabled := false
	cfg := FeishuConfig{
		Enabled:              true,
		DefaultAccount:       "main",
		AppID:                "base-direct-app",
		AppIDEnv:             "BASE_APP_ID",
		AppSecret:            "base-direct-secret",
		AppSecretEnv:         "BASE_SECRET",
		BaseURL:              "https://open.feishu.cn",
		BotName:              "mateway",
		AutoReply:            true,
		MentionRequiredGroup: true,
		WebSocket:            FeishuWebSocketConfig{Enabled: true},
		Accounts: []FeishuAccountConfig{
			{ID: "ops", AppIDEnv: "OPS_APP_ID"},
			{ID: "local", Enabled: &disabled, AppIDEnv: "LOCAL_APP_ID", AppSecretEnv: "LOCAL_SECRET"},
		},
	}
	accounts := cfg.AccountConfigs()
	if len(accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(accounts))
	}
	if accounts[0].DefaultAccount != "ops" || accounts[0].AppID != "" || accounts[0].AppIDEnv != "OPS_APP_ID" || accounts[0].AppSecret != "base-direct-secret" || accounts[0].AppSecretEnv != "BASE_SECRET" || !accounts[0].Enabled {
		t.Fatalf("unexpected ops account: %#v", accounts[0])
	}
	if accounts[1].DefaultAccount != "local" || accounts[1].AppID != "" || accounts[1].AppIDEnv != "LOCAL_APP_ID" || accounts[1].AppSecret != "" || accounts[1].AppSecretEnv != "LOCAL_SECRET" || accounts[1].Enabled {
		t.Fatalf("unexpected local account: %#v", accounts[1])
	}
}

func TestExecutionConfigDefaults(t *testing.T) {
	root := Root{}
	root.NormalizeForUse()
	if root.Execution.MaxParallelTools != 4 {
		t.Fatalf("default max parallel tools = %d", root.Execution.MaxParallelTools)
	}
	if root.Execution.MaxParallelNodes != 1 || root.Execution.MaxParallelNodesValue() != 1 {
		t.Fatalf("default max parallel nodes = %d", root.Execution.MaxParallelNodes)
	}
	if root.Execution.MaxIterationsValue() != 50 {
		t.Fatalf("default max iterations = %d", root.Execution.MaxIterationsValue())
	}
	if root.Execution.PlannerTimeout != "60s" {
		t.Fatalf("default planner timeout = %q", root.Execution.PlannerTimeout)
	}
	if root.Execution.PlannerTimeoutDuration() != time.Minute {
		t.Fatalf("default planner timeout duration = %s", root.Execution.PlannerTimeoutDuration())
	}
	if root.Execution.InactivityTimeout != "5m" {
		t.Fatalf("default inactivity timeout = %q", root.Execution.InactivityTimeout)
	}
	if root.Execution.InactivityTimeoutDuration() != 5*time.Minute {
		t.Fatalf("default inactivity timeout duration = %s", root.Execution.InactivityTimeoutDuration())
	}
	if !root.Execution.ContextBudget.EnabledValue() || root.Execution.ContextBudget.SoftRatio != 0.65 || root.Execution.ContextBudget.HardRatio != 0.90 {
		t.Fatalf("unexpected context budget defaults: %#v", root.Execution.ContextBudget)
	}
	if root.Execution.ContextBudget.RecentTurns != 8 || root.Execution.ContextBudget.ToolResultTargetTokens != 1200 || root.Execution.ContextBudget.MaxVisibleTools != 8 || !root.Execution.ContextBudget.TraceTelemetryValue() {
		t.Fatalf("unexpected context budget defaults: %#v", root.Execution.ContextBudget)
	}
	zero := 0
	root = Root{Execution: ExecutionConfig{MaxParallelTools: 1, MaxParallelNodes: 3}}
	root.NormalizeForUse()
	if root.Execution.MaxParallelTools != 1 {
		t.Fatalf("configured max parallel tools = %d", root.Execution.MaxParallelTools)
	}
	if root.Execution.MaxParallelNodes != 3 || root.Execution.MaxParallelNodesValue() != 3 {
		t.Fatalf("configured max parallel nodes = %d", root.Execution.MaxParallelNodes)
	}
	root = Root{Execution: ExecutionConfig{MaxParallelNodes: -2}}
	root.NormalizeForUse()
	if root.Execution.MaxParallelNodes != 1 || root.Execution.MaxParallelNodesValue() != 1 {
		t.Fatalf("invalid max parallel nodes should default to 1, got %d", root.Execution.MaxParallelNodes)
	}
	root = Root{Execution: ExecutionConfig{MaxIterations: &zero}}
	root.NormalizeForUse()
	if root.Execution.MaxIterationsValue() != 0 {
		t.Fatalf("configured disabled max iterations = %d", root.Execution.MaxIterationsValue())
	}
}

func TestRemoteDefaults(t *testing.T) {
	root := Root{}
	root.NormalizeForUse()
	if root.Remote.Profiles == nil {
		t.Fatal("expected remote profiles default")
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

func TestEnsureDefaultConfigFilesSeedsEditableDefaultSkills(t *testing.T) {
	home := t.TempDir()
	if err := EnsureDefaultConfigFiles(home); err != nil {
		t.Fatalf("ensure default config files: %v", err)
	}
	for _, name := range []string{"software-install", "fresh-search", "source-evaluation", "connector-gap", "skillcreate"} {
		path := filepath.Join(home, "workspace", "skills", name, "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected default skill %s to exist: %v", path, err)
		}
		metadataPath := filepath.Join(home, "workspace", "skills", name, ".mateway", "metadata.yaml")
		data, err := os.ReadFile(metadataPath)
		if err != nil {
			t.Fatalf("expected default skill metadata %s to exist: %v", metadataPath, err)
		}
		if !strings.Contains(string(data), `adapter_version: "2"`) || !strings.Contains(string(data), `granularity: "subtask"`) {
			t.Fatalf("unexpected default skill metadata for %s:\n%s", name, data)
		}
		if name == "fresh-search" && (!strings.Contains(string(data), `type: "react"`) || !strings.Contains(string(data), `web.search`) || !strings.Contains(string(data), `web.fetch`)) {
			t.Fatalf("fresh-search metadata should allow web react execution:\n%s", data)
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

func TestEnsureDefaultConfigFilesUsesCustomAssetsDir(t *testing.T) {
	assets := copyInitAssetsForTest(t)
	customReadme := filepath.Join(assets, "config", "README.md")
	if err := os.WriteFile(customReadme, []byte("# Custom Init Assets\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := EnsureDefaultConfigFilesWithAssets(home, assets); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "config", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# Custom Init Assets\n" {
		t.Fatalf("expected custom asset content, got %q", string(data))
	}
}

func TestEnsureDefaultConfigFilesFallsBackToEmbeddedAssets(t *testing.T) {
	home := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	if err := EnsureDefaultConfigFiles(home); err != nil {
		t.Fatalf("ensure default config files from embedded assets: %v", err)
	}
	for _, rel := range []string{
		filepath.Join("config", "config.yaml"),
		filepath.Join("workspace", "skills", "fresh-search", "SKILL.md"),
		filepath.Join("workspace", "skills", "fresh-search", ".mateway", "metadata.yaml"),
		filepath.Join("workspace", "memory", "README.md"),
	} {
		if _, err := os.Stat(filepath.Join(home, rel)); err != nil {
			t.Fatalf("expected embedded init file %s: %v", rel, err)
		}
	}
}

func TestEnsureDefaultConfigFilesReportsMissingAssets(t *testing.T) {
	err := EnsureDefaultConfigFilesWithAssets(t.TempDir(), t.TempDir())
	if err == nil {
		t.Fatal("expected missing assets error")
	}
	if !strings.Contains(err.Error(), "mateway init assets not found") || !strings.Contains(err.Error(), "MATEWAY_ASSETS_DIR") {
		t.Fatalf("unexpected error: %v", err)
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

func copyInitAssetsForTest(t *testing.T) string {
	t.Helper()
	source := filepath.Join("..", "..", "assets", "init")
	target := filepath.Join(t.TempDir(), "init")
	if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		dst := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	}); err != nil {
		t.Fatalf("copy init assets: %v", err)
	}
	return target
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
