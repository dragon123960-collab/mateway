package app

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/config"
	agentharness "github.com/dongping/mateway/internal/harness"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/tools"
	"github.com/dongping/mateway/internal/workspace"
)

func TestRunInitAndDoctor(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".mateway")
	t.Setenv(config.EnvHome, home)

	var out bytes.Buffer
	if err := Run(context.Background(), []string{"init"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	if err := workspace.Init(cfg); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"doctor"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("skills")) {
		t.Fatalf("unexpected doctor output: %s", out.String())
	}
}

func TestRunGatewayStatusCommand(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".mateway")
	t.Setenv(config.EnvHome, home)

	var out bytes.Buffer
	if err := Run(context.Background(), []string{"init"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(home, "gateway_state.json")
	if err := os.WriteFile(statePath, []byte(`{"gateway_host":"127.0.0.1","gateway_port":8787}`), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := gatewayLaunchctlRunner
	t.Cleanup(func() { gatewayLaunchctlRunner = orig })
	gatewayLaunchctlRunner = func(ctx context.Context, args ...string) ([]byte, error) {
		return []byte("state = running"), nil
	}

	out.Reset()
	if err := Run(context.Background(), []string{"gateway", "status"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("runtime_state:")) {
		t.Fatalf("unexpected gateway status output: %s", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte("launchctl_state:")) {
		t.Fatalf("unexpected gateway status output: %s", out.String())
	}
}

func TestRunGatewayRestartCommand(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".mateway")
	t.Setenv(config.EnvHome, home)

	var out bytes.Buffer
	if err := Run(context.Background(), []string{"init"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	orig := gatewayLaunchctlRunner
	origHealth := gatewayHealthWaiter
	t.Cleanup(func() {
		gatewayLaunchctlRunner = orig
		gatewayHealthWaiter = origHealth
	})
	called := false
	gatewayLaunchctlRunner = func(ctx context.Context, args ...string) ([]byte, error) {
		called = true
		return []byte("ok"), nil
	}
	gatewayHealthWaiter = func(ctx context.Context, cfg config.Config, timeout time.Duration) error {
		return nil
	}

	out.Reset()
	if err := Run(context.Background(), []string{"gateway", "restart"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected launchctl runner to be called")
	}
}

func TestRunModelAndChannelCommands(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".mateway")
	t.Setenv(config.EnvHome, home)

	var out bytes.Buffer
	if err := Run(context.Background(), []string{"init"}, &out, &out); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := Run(context.Background(), []string{"model", "set-default", "aliyun-qwen"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("aliyun-qwen")) {
		t.Fatalf("unexpected model output: %s", out.String())
	}

	out.Reset()
	if err := Run(context.Background(), []string{"channel", "disable", "feishu"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"channel", "list"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("feishu disabled")) {
		t.Fatalf("unexpected channel output: %s", out.String())
	}
}

func TestRunTUICommand(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".mateway")
	t.Setenv(config.EnvHome, home)

	var out bytes.Buffer
	if err := Run(context.Background(), []string{"init"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(home, "workspace", "skills", "demo-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: demo-skill
description: Demo skill for TUI listing.
---
demo
`), 0o644); err != nil {
		t.Fatal(err)
	}
	origInput := tuiInput
	t.Cleanup(func() { tuiInput = origInput })
	tuiInput = strings.NewReader("/help\n/skills\n/tools\n/exit\n")
	out.Reset()
	if err := Run(context.Background(), []string{"tui"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("Local interactive session")) ||
		!bytes.Contains(out.Bytes(), []byte("/tools")) ||
		!bytes.Contains(out.Bytes(), []byte("demo-skill [doc]")) ||
		!bytes.Contains(out.Bytes(), []byte("read_file [builtin]")) {
		t.Fatalf("unexpected tui output: %s", out.String())
	}
}

func TestRunHelpAndLogsCommands(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".mateway")
	t.Setenv(config.EnvHome, home)

	origHomeDir := gatewayUserHomeDir
	t.Cleanup(func() { gatewayUserHomeDir = origHomeDir })
	userHome := t.TempDir()
	gatewayUserHomeDir = func() (string, error) { return userHome, nil }

	var out bytes.Buffer
	if err := Run(context.Background(), []string{"init"}, &out, &out); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := Run(context.Background(), []string{"help"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("doctor")) || !bytes.Contains(out.Bytes(), []byte("logs follow")) {
		t.Fatalf("unexpected help output: %s", out.String())
	}

	out.Reset()
	if err := Run(context.Background(), []string{"help", "doctor"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("Run config, model, channel")) {
		t.Fatalf("unexpected doctor help output: %s", out.String())
	}

	cfg := config.Default()
	outLog, errLog, err := gatewayLogPaths(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(outLog), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outLog, []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(errLog, []byte("warn one\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := Run(context.Background(), []string{"logs", "show"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("line two")) || !bytes.Contains(out.Bytes(), []byte("warn one")) {
		t.Fatalf("unexpected logs output: %s", out.String())
	}

	out.Reset()
	if err := Run(context.Background(), []string{"logs", "path"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("stdout:")) || !bytes.Contains(out.Bytes(), []byte("stderr:")) {
		t.Fatalf("unexpected logs path output: %s", out.String())
	}
}

func TestRunSkillCreateCommands(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".mateway")
	t.Setenv(config.EnvHome, home)

	var out bytes.Buffer
	if err := Run(context.Background(), []string{"init"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"skill", "create", "cli", "echo-tool"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(home, "workspace", "skills", "echo-tool")
	for _, name := range []string{"SKILL.md", "_meta.json", "scripts/run.sh", "references", "assets"} {
		if _, err := os.Stat(filepath.Join(skillDir, filepath.FromSlash(name))); err != nil {
			t.Fatalf("expected scaffold file %s: %v", name, err)
		}
	}
}

func TestRunWorkspaceAndAgentCommands(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".mateway")
	t.Setenv(config.EnvHome, home)

	var out bytes.Buffer
	if err := Run(context.Background(), []string{"init"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"workspace", "create", "alpha"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	workspacePath := filepath.Join(home, "workspaces", "alpha")
	out.Reset()
	if err := Run(context.Background(), []string{"agent", "create", workspacePath, "writer"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"agent", "list", workspacePath}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("writer")) {
		t.Fatalf("unexpected agent list output: %s", out.String())
	}
}

func TestRunScheduleCommands(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".mateway")
	t.Setenv(config.EnvHome, home)

	var out bytes.Buffer
	if err := Run(context.Background(), []string{"init"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"schedule", "create", "daily", "30", "summarize", "my", "work"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"schedule", "list"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("daily")) || !bytes.Contains(out.Bytes(), []byte("schedule=every 30 min")) {
		t.Fatalf("unexpected schedule output: %s", out.String())
	}

	out.Reset()
	if err := Run(context.Background(), []string{"schedule", "create", "cron", "report", "0 3 * * *", "Asia/Shanghai", "generate", "report"}, &out, &out); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := Run(context.Background(), []string{"schedule", "get", "report"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"kind": "cron"`)) || !bytes.Contains(out.Bytes(), []byte(`"expr": "0 3 * * *"`)) {
		t.Fatalf("unexpected schedule get output: %s", out.String())
	}

	out.Reset()
	if err := Run(context.Background(), []string{"schedule", "disable", "report"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("disabled")) {
		t.Fatalf("unexpected disable output: %s", out.String())
	}

	out.Reset()
	if err := Run(context.Background(), []string{"schedule", "enable", "report"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("enabled")) {
		t.Fatalf("unexpected enable output: %s", out.String())
	}

	out.Reset()
	if err := Run(context.Background(), []string{"schedule", "remove", "report"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("removed")) {
		t.Fatalf("unexpected remove output: %s", out.String())
	}
}

func TestRunRunCommands(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".mateway")
	t.Setenv(config.EnvHome, home)

	var out bytes.Buffer
	if err := Run(context.Background(), []string{"init"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"cli answer"}}]}`))
	}))
	defer llmServer.Close()
	runner := agentharness.New(cfg.App.Workspace, session.NewStore(cfg.App.Workspace), tools.NewRegistry(), cfg.Sessions.HistoryLimit)
	cfg.ModelList = []config.ModelConfig{{
		Name:     "default",
		Provider: "openai_compat",
		Model:    "demo-model",
		APIBase:  llmServer.URL,
		APIKey:   "test-key",
		Enabled:  true,
	}}
	cfg.Models.Default = "default"
	runner.UseEinoRuntime(cfg)
	run, err := runner.Start(context.Background(), agentharness.Request{
		SessionKey: "cli:test",
		UserText:   "hello cli run",
		Mode:       "chat",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"run", "list", "cli:test"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(run.ID)) {
		t.Fatalf("unexpected run list output: %s", out.String())
	}
	out.Reset()
	if err := Run(context.Background(), []string{"run", "get", run.ID}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"id":`)) {
		t.Fatalf("unexpected run get output: %s", out.String())
	}
}
