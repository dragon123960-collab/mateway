package tool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryRejectsUnknownTool(t *testing.T) {
	r := NewRegistry()
	r.Register(TimeNow())
	if _, ok := r.Get("missing.tool"); ok {
		t.Fatalf("unexpected missing tool")
	}
	if _, err := r.MustGet("missing.tool"); err == nil {
		t.Fatalf("expected error for unknown tool")
	}
}

func TestBuiltinDefinitionsHideDeprecatedShellRun(t *testing.T) {
	registry := NewBuiltinRegistry()
	if _, ok := registry.Get("shell.run"); !ok {
		t.Fatalf("expected deprecated shell.run alias to remain executable")
	}
	for _, def := range registry.Definitions() {
		if def.Name == "shell.run" {
			t.Fatalf("expected shell.run to be hidden from planner definitions")
		}
	}
}

func TestBuiltinDefinitionsDeclareExplicitReusePolicy(t *testing.T) {
	registry := NewBuiltinRegistry()
	for _, def := range registry.AllDefinitions() {
		if def.Metadata.ReusePolicy == "" {
			t.Fatalf("expected builtin tool %q to declare explicit reuse policy", def.Name)
		}
	}
}

func TestDangerousCommandGuard(t *testing.T) {
	cases := []string{
		"rm -rf tmp",
		"git reset --hard",
	}
	for _, cmd := range cases {
		if !IsDangerousCommand(cmd) {
			t.Fatalf("expected dangerous: %s", cmd)
		}
	}
	if !RequireConfirmForTool("terminal.run", map[string]string{"command": "rm -rf tmp"}) {
		t.Fatalf("expected terminal.run dangerous command to require confirmation")
	}
	for _, cmd := range []string{"pwd", "ls -la", "git status", "go test ./...", "echo hi > file.txt", "npm install"} {
		if IsDangerousCommand(cmd) {
			t.Fatalf("expected safe: %s", cmd)
		}
	}
}

func TestTruncate(t *testing.T) {
	text := "abcdefghijklmnopqrstuvwxyz"
	got := Truncate(text, 10)
	if got == text || len(got) <= 10 {
		t.Fatalf("expected truncated marker, got %q", got)
	}
}

func TestPathGuardRejectsOutsideRoot(t *testing.T) {
	_, err := ResolveAllowedPath("/etc/passwd", Context{ProjectRoot: "/tmp/project", Workspace: "/tmp/workspace"})
	if err == nil {
		t.Fatalf("expected outside root error")
	}
}

func TestPathGuardAllowsProjectRelative(t *testing.T) {
	got, err := ResolveAllowedPath("README.md", Context{ProjectRoot: "/tmp/project", Workspace: "/tmp/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/project/README.md" {
		t.Fatalf("unexpected path %q", got)
	}
}

func TestPathGuardRemapsStaleProjectAbsolutePath(t *testing.T) {
	root := t.TempDir()
	doc := filepath.Join(root, "docs", "current.md")
	if err := os.MkdirAll(filepath.Dir(doc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doc, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join("/Users/yijun/ws", filepath.Base(root), "docs", "current.md")
	got, err := ResolveAllowedPath(stale, Context{ProjectRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if got != doc {
		t.Fatalf("expected remapped path %q, got %q", doc, got)
	}
}

func TestWebSearchHonorsDisabledProviders(t *testing.T) {
	result := WebSearch().Run(context.Background(), Call{
		Args:    map[string]string{"query": "Mateway"},
		Context: Context{Search: SearchConfig{}},
	})
	if result.OK || !strings.Contains(result.Error, "disabled") {
		t.Fatalf("expected disabled provider error, got %#v", result)
	}
}

func TestWebSearchUsesCacheWithoutEnabledProvider(t *testing.T) {
	root := t.TempDir()
	cfg := SearchConfig{CacheDir: root, CacheEnabled: true, CacheTTLHours: 24}
	writeWebSearchCache(cfg, "Mateway", Result{
		OK:       true,
		Output:   "cached result",
		Evidence: map[string]any{"kind": "web_search", "provider": "duckduckgo", "query": "Mateway", "result_count": 1},
	})
	result := WebSearch().Run(context.Background(), Call{
		Args:    map[string]string{"query": "Mateway"},
		Context: Context{Search: cfg},
	})
	if !result.OK || !strings.Contains(result.Output, "cached result") {
		t.Fatalf("expected cached search result, got %#v", result)
	}
	if hit, _ := result.Evidence["cache_hit"].(bool); !hit {
		t.Fatalf("expected cache_hit evidence, got %#v", result.Evidence)
	}
	if _, err := os.Stat(filepath.Join(root, "search")); err != nil {
		t.Fatalf("expected cache directory to exist: %v", err)
	}
}

func TestWebSearchSkipsTavilyWhenBudgetExhausted(t *testing.T) {
	root := t.TempDir()
	cfg := SearchConfig{
		CacheDir:            root,
		ProviderOrder:       []string{"tavily"},
		TavilyEnabled:       true,
		TavilyAPIKey:        "test",
		TavilyDailyBudget:   1,
		TavilyMonthlyBudget: 10,
	}
	recordTavilyUsage(cfg)
	result := WebSearch().Run(context.Background(), Call{
		Args:    map[string]string{"query": "Mateway"},
		Context: Context{Search: cfg},
	})
	if result.OK || !strings.Contains(result.Error, "budget exhausted") {
		t.Fatalf("expected tavily budget exhausted, got %#v", result)
	}
}

func TestWebSearchProviderOverrideFallsBackToConfiguredOrder(t *testing.T) {
	root := t.TempDir()
	cfg := SearchConfig{
		CacheDir:      root,
		CacheEnabled:  true,
		CacheTTLHours: 24,
		ProviderOrder: []string{"cache"},
	}
	writeWebSearchCache(cfg, "Mateway", Result{
		OK:       true,
		Output:   "cached fallback",
		Evidence: map[string]any{"kind": "web_search", "provider": "duckduckgo", "query": "Mateway", "result_count": 1},
	})
	result := WebSearch().Run(context.Background(), Call{
		Args:    map[string]string{"query": "Mateway", "provider": "tavily"},
		Context: Context{Search: cfg},
	})
	if !result.OK || !strings.Contains(result.Output, "cached fallback") {
		t.Fatalf("expected provider override to fall back to cache, got %#v", result)
	}
	if hit, _ := result.Evidence["cache_hit"].(bool); !hit {
		t.Fatalf("expected cache_hit evidence, got %#v", result.Evidence)
	}
}

func TestWebFetchReadsKnownURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html><head><title>Test Page</title></head><body><main>Hello <b>Mateway</b></main></body></html>`))
	}))
	defer server.Close()
	result := WebFetch().Run(context.Background(), Call{
		Args: map[string]string{"url": server.URL},
	})
	if !result.OK || !strings.Contains(result.Output, "Test Page") || !strings.Contains(result.Output, "Hello Mateway") {
		t.Fatalf("expected fetched page preview, got %#v", result)
	}
	if kind, _ := result.Evidence["kind"].(string); kind != "web_fetch" {
		t.Fatalf("expected web_fetch evidence, got %#v", result.Evidence)
	}
}
