package script

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/secret"
)

func TestListAndRunScriptWithSecret(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	scriptPath := filepath.Join(home, "scripts", "hello")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	text := "#!/bin/sh\n# mateway.name: hello\n# mateway.description: test script\n# mateway.risk: safe_read\n# mateway.required_secret: id=demo.token env=DEMO_TOKEN\necho token=$DEMO_TOKEN arg=$1\n"
	if err := os.WriteFile(scriptPath, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Root{App: config.AppConfig{Home: home, Workspace: workspace}}
	cfg.NormalizeForUse()
	if err := (secret.Store{Home: home}).Set("demo.token", "secret-value"); err != nil {
		t.Fatal(err)
	}
	scripts, err := List(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(scripts) != 1 || scripts[0].Name != "hello" || scripts[0].Risk != "safe_read" || len(scripts[0].RequiredSecrets) != 1 {
		t.Fatalf("scripts = %#v", scripts)
	}
	result, err := Run(context.Background(), cfg, RunInput{Name: "hello", Args: []string{"world"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "token=secret-value arg=world") {
		t.Fatalf("unexpected output: %#v", result)
	}
}

func TestRunScriptMissingSecretFails(t *testing.T) {
	home := t.TempDir()
	scriptPath := filepath.Join(home, "scripts", "need-secret")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	text := "#!/bin/sh\n# mateway.name: need-secret\n# mateway.required_secret: id=missing.token env=MISSING_TOKEN\necho ok\n"
	if err := os.WriteFile(scriptPath, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Root{App: config.AppConfig{Home: home, Workspace: filepath.Join(home, "workspace")}}
	cfg.NormalizeForUse()
	_, err := Run(context.Background(), cfg, RunInput{Name: "need-secret"})
	if err == nil || !strings.Contains(err.Error(), "missing required secret") {
		t.Fatalf("expected missing secret error, got %v", err)
	}
}

func TestListIncludesSkillLocalScriptsWithPrecedence(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	agentScript := filepath.Join(workspace, "agents", "main", "skills", "mail", "scripts", "run")
	sharedScript := filepath.Join(workspace, "skills", "mail", "scripts", "run")
	globalScript := filepath.Join(workspace, "scripts", "run")
	for _, path := range []string{agentScript, sharedScript, globalScript} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(agentScript, []byte("#!/bin/sh\n# mateway.name: mail.run\necho agent\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sharedScript, []byte("#!/bin/sh\n# mateway.name: mail.run\necho shared\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalScript, []byte("#!/bin/sh\n# mateway.name: mail.run\necho global\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Root{App: config.AppConfig{Home: home, Workspace: workspace}, Agents: config.AgentsConfig{Default: "main"}}
	cfg.NormalizeForUse()
	scripts, err := List(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(scripts) != 1 || scripts[0].Name != "mail.run" || scripts[0].Path != agentScript {
		t.Fatalf("expected agent script to win, got %#v", scripts)
	}
	result, err := Run(context.Background(), cfg, RunInput{Name: "mail.run"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Output) != "agent" {
		t.Fatalf("expected agent output, got %#v", result)
	}
}
