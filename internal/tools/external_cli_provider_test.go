package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalCLIProviderExposesConfiguredTools(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "demo-cli")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	provider := ExternalCLIProvider{
		Name:       "demo cli",
		BinaryPath: bin,
		Enabled:    true,
		ListArgs:   []string{"--help"},
	}
	toolsList, err := provider.Tools(context.Background(), Scope{})
	if err != nil {
		t.Fatal(err)
	}
	if len(toolsList) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(toolsList))
	}
	if toolsList[0].Spec().Name != "demo_cli_list" {
		t.Fatalf("unexpected list tool name: %s", toolsList[0].Spec().Name)
	}
	if toolsList[1].Spec().Name != "demo_cli_run" {
		t.Fatalf("unexpected run tool name: %s", toolsList[1].Spec().Name)
	}
}

func TestExternalCLIRunBlocksDeniedCommands(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "demo-cli")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := externalCLIRunTool{
		name:            "demo",
		binaryPath:      bin,
		allowedCommands: []string{"search", "read"},
		blockedCommands: []string{"install"},
		riskLevel:       "medium",
	}
	blocked, err := tool.Invoke(context.Background(), Call{Arguments: mustJSON(map[string]any{"args": []string{"install", "foo"}})})
	if err != nil {
		t.Fatalf("expected blocked command to be returned as a policy denial result, got error: %v", err)
	}
	if !strings.Contains(string(blocked.Output), "provider policy deny") {
		t.Fatalf("unexpected blocked output: %s", string(blocked.Output))
	}
	disallowed, err := tool.Invoke(context.Background(), Call{Arguments: mustJSON(map[string]any{"args": []string{"write", "foo"}})})
	if err != nil {
		t.Fatalf("expected disallowed command to be returned as a policy denial result, got error: %v", err)
	}
	if !strings.Contains(string(disallowed.Output), "allowed root commands") {
		t.Fatalf("unexpected disallowed output: %s", string(disallowed.Output))
	}
}

func TestExternalCLIRunSpecMentionsAllowedCommands(t *testing.T) {
	tool := externalCLIRunTool{
		name:            "demo",
		description:     "Demo provider",
		allowedCommands: []string{"web", "github"},
	}
	spec := tool.Spec()
	if !strings.Contains(spec.Description, "Allowed root commands: web, github") {
		t.Fatalf("unexpected description: %s", spec.Description)
	}
}

func TestExternalCLIListReturnsRecoverableUnavailableMessage(t *testing.T) {
	tool := externalCLIListTool{
		name:       "demo",
		binaryPath: filepath.Join(t.TempDir(), "missing-demo"),
		args:       []string{"--help"},
		shellPath:  "/bin/zsh",
	}
	res, err := tool.Invoke(context.Background(), Call{})
	if err != nil {
		t.Fatalf("expected recoverable list failure, got error: %v", err)
	}
	if !strings.Contains(string(res.Output), "provider unavailable") {
		t.Fatalf("unexpected output: %s", string(res.Output))
	}
}

func TestExternalCLIRunTurnsUnknownHelpSurfaceIntoLearnBeforeRunGuidance(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "demo-cli")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 127\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := externalCLIRunTool{
		name:            "demo",
		binaryPath:      bin,
		allowedCommands: []string{"web"},
		shellPath:       "/bin/zsh",
	}
	res, err := tool.Invoke(context.Background(), Call{Arguments: mustJSON(map[string]any{"args": []string{"web", "ai"}})})
	if err != nil {
		t.Fatalf("expected recoverable run failure, got error: %v", err)
	}
	if !strings.Contains(string(res.Output), "learn-before-run") {
		t.Fatalf("unexpected output: %s", string(res.Output))
	}
}
