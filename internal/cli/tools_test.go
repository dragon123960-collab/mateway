package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/config"
)

func TestPrintToolsListsRiskAndRequiredArgs(t *testing.T) {
	var out bytes.Buffer
	cfg := &config.Root{App: config.AppConfig{Home: t.TempDir()}}
	if err := PrintTools(&out, cfg, "", false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"NAME", "STATUS", "RISK", "terminal.run", "enabled", "guarded_mutation", "file.read", "path"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func TestPrintToolsVerboseIncludesDescription(t *testing.T) {
	var out bytes.Buffer
	cfg := &config.Root{App: config.AppConfig{Home: t.TempDir()}}
	if err := PrintTools(&out, cfg, "", true); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "DESCRIPTION") || !strings.Contains(text, "run a local shell command") {
		t.Fatalf("unexpected verbose tools output:\n%s", text)
	}
}

func TestPrintToolsShowsDisabledByProfile(t *testing.T) {
	var out bytes.Buffer
	cfg := &config.Root{
		App: config.AppConfig{Home: t.TempDir()},
		Agents: config.AgentsConfig{
			Default: "main",
			Profiles: []config.AgentProfileConfig{{
				ID: "main",
				Tools: config.AccessListConfig{
					Deny: []string{"terminal.run"},
				},
			}},
		},
	}
	if err := PrintTools(&out, cfg, "main", false); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "terminal.run") || !strings.Contains(text, "disabled") {
		t.Fatalf("expected disabled terminal.run:\n%s", text)
	}
}

func TestDisableAndEnableToolUpdatesProfileConfig(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Root{
		App: config.AppConfig{Home: home},
		Agents: config.AgentsConfig{
			Default: "main",
			Profiles: []config.AgentProfileConfig{{
				ID: "main",
			}},
		},
	}
	change, err := DisableTool(cfg, "main", "terminal.run")
	if err != nil {
		t.Fatal(err)
	}
	if !containsAccessValue(change.Deny, "terminal.run") {
		t.Fatalf("expected deny to include terminal.run: %#v", change)
	}
	data, err := os.ReadFile(filepath.Join(home, "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "terminal.run") {
		t.Fatalf("expected saved config to contain terminal.run:\n%s", data)
	}
	change, err = EnableTool(cfg, "main", "terminal.run")
	if err != nil {
		t.Fatal(err)
	}
	if containsAccessValue(change.Deny, "terminal.run") {
		t.Fatalf("expected deny to remove terminal.run: %#v", change)
	}
}

func containsAccessValue(values []string, value string) bool {
	for _, item := range values {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}
