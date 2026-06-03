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

func TestFileWriteAllowsSkillPlaintextSecret(t *testing.T) {
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
	if result.IsError {
		t.Fatalf("expected secret-like skill write, got %#v", result)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# Mail\npassword: supersecret123\n" {
		t.Fatalf("content = %q", data)
	}
}

func TestFileWriteAllowsRedactedContent(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{App: config.AppConfig{Home: home}, Security: config.SecurityConfig{EnforceWorkspacePaths: true}}
	target := filepath.Join(home, "note.txt")
	content := "api_key: [REDACTED_SECRET]\n"
	result := FileWriteTool{Config: cfg}.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_1",
		Name: "file.write",
		Args: map[string]any{"path": target, "content": content},
	})
	if result.IsError {
		t.Fatalf("expected redacted placeholder write, got %#v", result)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Fatalf("content = %q", data)
	}
}

func TestFileWriteAllowsConfigYamlReplacement(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{App: config.AppConfig{Home: home}, Security: config.SecurityConfig{EnforceWorkspacePaths: true}}
	target := filepath.Join(home, "config", "config.yaml")
	content := "app:\n  name: mateway\n"
	result := FileWriteTool{Config: cfg}.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_1",
		Name: "file.write",
		Args: map[string]any{"path": target, "content": content},
	})
	if result.IsError {
		t.Fatalf("expected config write, got %#v", result)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Fatalf("content = %q", data)
	}
}

func TestFileWriteAllowsEnvSecretLookupInSkillScript(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	cfg := &config.Root{App: config.AppConfig{Home: home, Workspace: workspace}, Security: config.SecurityConfig{EnforceWorkspacePaths: true}}
	target := filepath.Join(workspace, "skills", "mail", "scripts", "mail.send")
	content := "#!/usr/bin/env python3\n# mateway.required_secret: id=mail.auth env=MAIL_AUTH\npassword = os.environ.get(\"MAIL_AUTH\")\n"
	result := FileWriteTool{Config: cfg}.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_1",
		Name: "file.write",
		Args: map[string]any{"path": target, "content": content},
	})
	if result.IsError {
		t.Fatalf("expected env lookup script write, got %#v", result)
	}
}

func TestFileWriteAllowsTinyPartialOverwriteOfExistingSkillScript(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	cfg := &config.Root{App: config.AppConfig{Home: home, Workspace: workspace}, Security: config.SecurityConfig{EnforceWorkspacePaths: true}}
	target := filepath.Join(workspace, "skills", "mail", "scripts", "mail.search")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "#!/usr/bin/env python3\n# mateway.name: mail.search\n" + strings.Repeat("print('ok')\n", 40)
	if err := os.WriteFile(target, []byte(original), 0o755); err != nil {
		t.Fatal(err)
	}
	result := FileWriteTool{Config: cfg}.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_1",
		Name: "file.write",
		Args: map[string]any{"path": target, "content": "    return fixed\n"},
	})
	if result.IsError {
		t.Fatalf("expected partial overwrite, got %#v", result)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "    return fixed\n" {
		t.Fatalf("content = %q", data)
	}
}

func TestTerminalRunRejectsKnownSecretInCommand(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{App: config.AppConfig{Home: home}, Security: config.SecurityConfig{EnforceWorkspacePaths: true}}
	if err := (secret.Store{Home: home}).Set("mail.auth", "QBptnPtt6Hnp3awb"); err != nil {
		t.Fatal(err)
	}
	result := TerminalRunTool{Config: cfg}.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_1",
		Name: "terminal.run",
		Args: map[string]any{"command": "python3 -c 'print(\"QBptnPtt6Hnp3awb\")'"},
	})
	if !result.IsError || !strings.Contains(result.Content, "secret value") {
		t.Fatalf("expected terminal secret rejection, got %#v", result)
	}
}

func TestSecretSetToolStoresValueWithoutReturningIt(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{App: config.AppConfig{Home: home}}
	result := SecretSetTool{Config: cfg}.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_1",
		Name: "secret.set",
		Args: map[string]any{"id": "mail.auth_code", "value": "QBptnPtt6Hnp3awb"},
	})
	if result.IsError || result.Evidence["stored"] != true {
		t.Fatalf("expected stored secret result, got %#v", result)
	}
	if strings.Contains(result.Content, "QBptnPtt6Hnp3awb") || strings.Contains(fmt.Sprint(result.Evidence), "QBptnPtt6Hnp3awb") {
		t.Fatalf("secret value leaked in result: %#v", result)
	}
	entry, ok, err := secret.Store{Home: home}.Get("mail.auth_code")
	if err != nil || !ok || entry.Value != "QBptnPtt6Hnp3awb" {
		t.Fatalf("secret not stored: entry=%#v ok=%v err=%v", entry, ok, err)
	}
}

func TestSecretSetToolRejectsRedactedPlaceholder(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{App: config.AppConfig{Home: home}}
	result := SecretSetTool{Config: cfg}.Run(context.Background(), agentcore.ToolCall{
		ID:   "call_1",
		Name: "secret.set",
		Args: map[string]any{"id": "mail.auth_code", "value": "[REDACTED_SECRET]"},
	})
	if !result.IsError || !strings.Contains(result.Content, "redacted placeholder") {
		t.Fatalf("expected placeholder rejection, got %#v", result)
	}
	if _, ok, err := (secret.Store{Home: home}).Get("mail.auth_code"); err != nil || ok {
		t.Fatalf("placeholder should not be stored: ok=%v err=%v", ok, err)
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
