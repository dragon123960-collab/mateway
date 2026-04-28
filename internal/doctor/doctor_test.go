package doctor

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/config"
)

func TestRunIncludesLLMConnectivityCheck(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	defer server.Close()

	cfg := config.Default()
	cfg.App.Workspace = t.TempDir()
	cfg.ModelList = []config.ModelConfig{{
		Name:     "default",
		Provider: "openai_compat",
		Model:    "demo-model",
		APIBase:  server.URL,
		APIKey:   "test-key",
		Enabled:  true,
	}}
	cfg.Models.Default = "default"

	checks := Run(context.Background(), cfg)
	check, ok := findCheck(checks, "llm_connectivity")
	if !ok {
		t.Fatalf("expected llm_connectivity check, got %#v", checks)
	}
	if check.Status != "ok" || !strings.Contains(check.Details, "http 200") {
		t.Fatalf("unexpected llm_connectivity check: %#v", check)
	}
}

func TestRunWarnsWhenTavilyKeyMissing(t *testing.T) {
	cfg := config.Default()
	cfg.App.Workspace = t.TempDir()
	cfg.Integrations.WebSearch.Enabled = true
	cfg.Integrations.WebSearch.Provider = "tavily"
	cfg.Integrations.WebSearch.Tavily.BaseURL = "https://api.tavily.com/search"
	cfg.Integrations.WebSearch.Tavily.APIKey = ""

	checks := Run(context.Background(), cfg)
	check, ok := findCheck(checks, "web_search")
	if !ok {
		t.Fatalf("expected web_search check, got %#v", checks)
	}
	if check.Status != "warn" || !strings.Contains(check.Details, "api_key missing") {
		t.Fatalf("unexpected web_search check: %#v", check)
	}
}

func TestRunChecksCLIProviderAvailability(t *testing.T) {
	dir := t.TempDir()
	cliPath := filepath.Join(dir, "mockcli")
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\necho mock help\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.App.Workspace = dir
	cfg.CLIProviders = []config.CLIProviderConfig{
		{
			Name:     "mock",
			Binary:   cliPath,
			ListArgs: []string{"--help"},
		},
		{
			Name:   "missing",
			Binary: filepath.Join(dir, "does-not-exist"),
		},
	}

	checks := Run(context.Background(), cfg)
	okCheck, ok := findCheck(checks, "cli_provider:mock")
	if !ok {
		t.Fatalf("expected cli_provider:mock check, got %#v", checks)
	}
	if okCheck.Status != "ok" || !strings.Contains(okCheck.Details, cliPath) {
		t.Fatalf("unexpected cli_provider:mock check: %#v", okCheck)
	}
	missingCheck, ok := findCheck(checks, "cli_provider:missing")
	if !ok {
		t.Fatalf("expected cli_provider:missing check, got %#v", checks)
	}
	if missingCheck.Status != "warn" {
		t.Fatalf("unexpected cli_provider:missing check: %#v", missingCheck)
	}
}

func findCheck(checks []Check, name string) (Check, bool) {
	for _, check := range checks {
		if check.Name == name {
			return check, true
		}
	}
	return Check{}, false
}
