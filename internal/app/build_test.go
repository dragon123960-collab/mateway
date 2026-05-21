package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/config"
)

func TestSelectModelUsesExplicitDefault(t *testing.T) {
	models := []config.ModelConfig{
		{Name: "local-mlx", API: "openai", Enabled: true},
		{Name: "minimax", API: "anthropic", Enabled: true},
	}

	got, err := selectModel(models, config.ModelSelection{Default: "local-mlx"})
	if err != nil {
		t.Fatalf("select model: %v", err)
	}
	if got.Name != "local-mlx" {
		t.Fatalf("expected local-mlx, got %s", got.Name)
	}
}

func TestSelectModelDoesNotPreferOpenAIImplicitly(t *testing.T) {
	models := []config.ModelConfig{
		{Name: "local-mlx", API: "openai", Enabled: true},
		{Name: "minimax", API: "anthropic", Enabled: true},
	}

	got, err := selectModel(models, config.ModelSelection{})
	if err != nil {
		t.Fatalf("select model: %v", err)
	}
	if got.Name != "minimax" {
		t.Fatalf("expected legacy minimax default, got %s", got.Name)
	}
}

func TestSelectModelUnknownDefaultFailsClearly(t *testing.T) {
	models := []config.ModelConfig{
		{Name: "minimax", API: "anthropic", Enabled: true},
	}

	_, err := selectModel(models, config.ModelSelection{Default: "missing"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), `configured default model "missing"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDefaultAgentStrictFailsOnUnknownDefault(t *testing.T) {
	cfg := &config.Root{
		Agents: config.AgentsConfig{
			Default: "missing",
			Profiles: []config.AgentProfileConfig{
				{ID: "main"},
			},
		},
	}

	_, err := cfg.DefaultAgentStrict()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), `configured default agent "missing"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureLayoutBackfillsMemoryTemplates(t *testing.T) {
	home := t.TempDir()
	if err := ensureLayout(home); err != nil {
		t.Fatalf("ensure layout: %v", err)
	}
	path := filepath.Join(home, "workspace", "memory", "README.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected memory template to be backfilled: %v", err)
	}
}
