package agentprofile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/config"
)

func TestManagerCreateReportBindUnbind(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	cfg := &config.Root{
		App: config.AppConfig{Home: home, Workspace: workspace},
		Model: config.ModelSelection{
			Default: "minimax",
		},
		Agents: config.AgentsConfig{
			Default:  "main",
			Profiles: []config.AgentProfileConfig{{ID: "main", Name: "Main", Default: true, SessionNamespace: "main", Model: config.ModelSelection{Default: "minimax"}}},
		},
	}
	cfg.NormalizeForUse()
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Config: cfg}
	created, err := manager.Create(CreateAgentInput{ID: "Ops Bot", Name: "Ops Bot"})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "opsbot" {
		t.Fatalf("unexpected id: %#v", created)
	}
	for _, name := range []string{"agent.md", "user.md", "tools.md", "memory.md"} {
		if _, err := os.Stat(filepath.Join(workspace, "agents", "opsbot", name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	report, err := manager.Report("opsbot")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Issues) != 0 || report.AgentDir == "" || report.MemoryRoot == "" {
		t.Fatalf("unexpected report: %#v", report)
	}
	binding, err := manager.Bind(BindInput{Channel: "feishu", PeerID: "chat-1", AgentID: "opsbot"})
	if err != nil {
		t.Fatal(err)
	}
	if binding.AgentID != "opsbot" || binding.PeerID != "chat-1" {
		t.Fatalf("unexpected binding: %#v", binding)
	}
	removed, err := manager.Unbind(BindInput{Channel: "feishu", PeerID: "chat-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatalf("expected binding removed")
	}
	data, err := os.ReadFile(filepath.Join(home, "config", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "opsbot") {
		t.Fatalf("config not written:\n%s", data)
	}
}
