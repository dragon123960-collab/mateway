package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/secret"
)

func TestResolveAllowedPathDefaultsRelativeToHome(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{App: config.AppConfig{Home: home, Workspace: filepath.Join(home, "workspace")}, Security: config.SecurityConfig{EnforceWorkspacePaths: true}}
	path, err := ResolveAllowedPath("notes/a.txt", cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "notes", "a.txt")
	if path != want {
		t.Fatalf("path = %q want %q", path, want)
	}
}

func TestResolveAllowedPathRejectsOutsideRoot(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{App: config.AppConfig{Home: home, Workspace: filepath.Join(home, "workspace")}, Security: config.SecurityConfig{EnforceWorkspacePaths: true}}
	if _, err := ResolveAllowedPath("/etc/passwd", cfg); err == nil {
		t.Fatal("expected outside root error")
	}
}

func TestResolveAllowedPathAllowsAccessiblePath(t *testing.T) {
	home := t.TempDir()
	extra := t.TempDir()
	cfg := &config.Root{
		App:      config.AppConfig{Home: home, Workspace: filepath.Join(home, "workspace")},
		Security: config.SecurityConfig{EnforceWorkspacePaths: true, AccessiblePaths: []string{extra}},
	}
	path, err := ResolveAllowedPath(filepath.Join(extra, "ok.txt"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(extra, "ok.txt") {
		t.Fatalf("path = %q", path)
	}
}

func TestIsDangerousCommand(t *testing.T) {
	if !IsDangerousCommand("git reset --hard") {
		t.Fatal("expected git reset to be dangerous")
	}
	if IsDangerousCommand("go test ./...") {
		t.Fatal("expected go test to be safe")
	}
}

func TestTerminalRunUsesSandboxWorkdir(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	tool := TerminalRunTool{Config: &config.Root{
		App:      config.AppConfig{Home: home, Workspace: workspace},
		Security: config.SecurityConfig{EnforceWorkspacePaths: true, TerminalSandbox: config.TerminalSandboxConfig{Enabled: true, Mode: "restricted"}},
	}}
	result := tool.Run(context.Background(), agentcore.ToolCall{ID: "1", Args: map[string]any{"command": "pwd"}})
	if result.IsError {
		t.Fatalf("unexpected terminal error: %#v", result)
	}
	if strings.TrimSpace(result.Content) != workspace {
		t.Fatalf("pwd = %q want %q", result.Content, workspace)
	}
	if result.Evidence["sandbox"] != "restricted" {
		t.Fatalf("missing sandbox evidence: %#v", result.Evidence)
	}
}

func TestFileWriteRejectsSkillPlaintextSecret(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	cfg := &config.Root{
		App:      config.AppConfig{Home: home, Workspace: workspace},
		Security: config.SecurityConfig{EnforceWorkspacePaths: true},
	}
	target := filepath.Join(workspace, "skills", "mail", "SKILL.md")
	result := FileWriteTool{Config: cfg}.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_1",
		Name: "file.write",
		Args: map[string]any{"path": target, "content": "# Mail\npassword: supersecret123\n"},
	})
	if !result.IsError || !strings.Contains(result.Content, "refusing to write secret-like content") {
		t.Fatalf("expected secret write rejection, got %#v", result)
	}
}

func TestSecretSetToolStoresValueWithoutReturningIt(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{App: config.AppConfig{Home: home}}
	result := SecretSetTool{Config: cfg}.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_1",
		Name: "secret.set",
		Args: map[string]any{"id": "mail.pop_password", "value": "supersecret123"},
	})
	if result.IsError || result.Evidence["stored"] != true {
		t.Fatalf("expected stored secret result, got %#v", result)
	}
	if strings.Contains(result.Content, "supersecret123") || strings.Contains(fmt.Sprint(result.Evidence), "supersecret123") {
		t.Fatalf("secret value leaked in result: %#v", result)
	}
	entry, ok, err := secret.Store{Home: home}.Get("mail.pop_password")
	if err != nil || !ok || entry.Value != "supersecret123" {
		t.Fatalf("secret not stored: entry=%#v ok=%v err=%v", entry, ok, err)
	}
}

func TestScriptRunToolAcceptsItemArgsObject(t *testing.T) {
	home := t.TempDir()
	scriptPath := filepath.Join(home, "scripts", "mail")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\n# mateway.name: email.receive\necho count=$1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Root{App: config.AppConfig{Home: home, Workspace: filepath.Join(home, "workspace")}}
	cfg.NormalizeForUse()
	result := ScriptRunTool{Config: cfg}.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_1",
		Name: "script.run",
		Args: map[string]any{"name": "email.receive", "args": map[string]any{"item": "1"}},
	})
	if result.IsError || !strings.Contains(result.Content, "count=1") {
		t.Fatalf("expected script arg to be passed, got %#v", result)
	}
	if got := fmt.Sprint(result.Evidence["args"]); got != "[1]" {
		t.Fatalf("args evidence = %s", got)
	}
}

func TestFileWriteCreatesProposalForAgentCoreProfile(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	target := filepath.Join(workspace, "agents", "main", "user.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Root{
		App:      config.AppConfig{Home: home, Workspace: workspace},
		Security: config.SecurityConfig{EnforceWorkspacePaths: true},
	}
	result := FileWriteTool{Config: cfg}.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_1",
		Name: "file.write",
		Args: map[string]any{"path": target, "content": "new"},
	})
	if result.IsError || result.Evidence["requires_review"] != true || strings.TrimSpace(result.Evidence["proposal_id"].(string)) == "" {
		t.Fatalf("expected profile proposal result, got %#v", result)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("core profile should not be overwritten before review, got %q", data)
	}
	if entries, err := os.ReadDir(filepath.Join(home, "observe", "agent_profile_proposals")); err != nil || len(entries) != 1 {
		t.Fatalf("expected one proposal entry, entries=%v err=%v", entries, err)
	}
}

func TestFileWriteAllowsAgentSkillProfilePath(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	target := filepath.Join(workspace, "agents", "main", "skills", "demo", "SKILL.md")
	cfg := &config.Root{
		App:      config.AppConfig{Home: home, Workspace: workspace},
		Security: config.SecurityConfig{EnforceWorkspacePaths: true},
	}
	result := FileWriteTool{Config: cfg}.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_1",
		Name: "file.write",
		Args: map[string]any{"path": target, "content": "# Demo\n"},
	})
	if result.IsError || result.Evidence["requires_review"] == true {
		t.Fatalf("expected direct skill write, got %#v", result)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleCreateToolWritesTask(t *testing.T) {
	home := t.TempDir()
	runAt := "2026-05-29T16:30:00+08:00"
	tool := ScheduleCreateTool{Config: &config.Root{App: config.AppConfig{Home: home}}}
	result := tool.Run(context.Background(), agentcore.ToolCall{ID: "1", Args: map[string]any{
		"text":        "提醒我检查日报",
		"run_at":      runAt,
		"session_key": "feishu:chat_1",
	}})
	if result.IsError {
		t.Fatalf("unexpected schedule error: %#v", result)
	}
	if result.Evidence["status"] != "pending" || result.Evidence["session_key"] != "feishu:chat_1" {
		t.Fatalf("unexpected evidence: %#v", result.Evidence)
	}
	if entries, err := os.ReadDir(filepath.Join(home, "schedules")); err != nil || len(entries) != 1 {
		t.Fatalf("expected one schedule entry, entries=%v err=%v", entries, err)
	}
}

func TestFileReadRejectsLargeFile(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "large.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 512*1024+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := FileReadTool{Config: &config.Root{App: config.AppConfig{Home: home}, Security: config.SecurityConfig{EnforceWorkspacePaths: true}}}
	result := tool.Run(nil, agentcore.ToolCall{ID: "1", Args: map[string]any{"path": path}})
	if !result.IsError || !strings.Contains(result.Content, "file too large") {
		t.Fatalf("expected large file error, got %#v", result)
	}
}

func TestFileReadRejectsBinaryFile(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "binary.bin")
	if err := os.WriteFile(path, []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	tool := FileReadTool{Config: &config.Root{App: config.AppConfig{Home: home}, Security: config.SecurityConfig{EnforceWorkspacePaths: true}}}
	result := tool.Run(nil, agentcore.ToolCall{ID: "1", Args: map[string]any{"path": path}})
	if !result.IsError || !strings.Contains(result.Content, "binary") {
		t.Fatalf("expected binary file error, got %#v", result)
	}
}
