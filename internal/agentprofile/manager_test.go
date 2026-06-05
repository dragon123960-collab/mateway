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
	for _, name := range CoreProfileFileNames() {
		if _, err := os.Stat(filepath.Join(workspace, "agents", "opsbot", name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	memoryData, err := os.ReadFile(filepath.Join(workspace, "memory", "agents", "opsbot", "memory.md"))
	if err != nil {
		t.Fatal(err)
	}
	memoryText := string(memoryData)
	if !strings.HasPrefix(memoryText, "---\n") || !strings.Contains(memoryText, "owner_agent: opsbot") {
		t.Fatalf("memory entry missing frontmatter:\n%s", memoryText)
	}
	assertAgentFileContains(t, filepath.Join(workspace, "agents", "opsbot", "agent.md"), "Ops Bot", "agent \"opsbot\"", "## Operating Rules")
	assertAgentFileContains(t, filepath.Join(workspace, "agents", "opsbot", "soul.md"), "You are Ops Bot", "## Principles", "## Boundaries")
	assertAgentFileContains(t, filepath.Join(workspace, "agents", "opsbot", "user.md"), "No stable user preferences recorded yet.", "agent \"opsbot\"", "## Do Not Assume")
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

func TestManagerReportWarnsWhenSoulFileMissing(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	agentDir := filepath.Join(workspace, "agents", "legacy")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agent.md", "user.md", "tools.md", "memory.md"} {
		if err := os.WriteFile(filepath.Join(agentDir, name), []byte("# "+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(workspace, "memory", "agents", "legacy"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Root{
		App: config.AppConfig{Home: home, Workspace: workspace},
		Agents: config.AgentsConfig{
			Default:  "legacy",
			Profiles: []config.AgentProfileConfig{{ID: "legacy", Name: "Legacy", Default: true, SessionNamespace: "legacy", Model: config.ModelSelection{Default: "minimax"}}},
		},
	}
	report, err := (Manager{Config: cfg}).Report("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Issues) != 1 || report.Issues[0].Severity != "warning" || !strings.Contains(report.Issues[0].Message, "soul.md") {
		t.Fatalf("expected missing soul warning, got %#v", report.Issues)
	}
}

func TestEnsureAgentFilesPreservesExistingPromptFiles(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	target := filepath.Join(workspace, "agents", "ops", "agent.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("custom agent rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := ensureAgentFiles(workspace, config.AgentProfileConfig{ID: "ops", Name: "Ops Agent"})
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "custom agent rules\n" {
		t.Fatalf("expected existing agent.md preserved, got %q", string(data))
	}
	assertAgentFileContains(t, filepath.Join(workspace, "agents", "ops", "soul.md"), "You are Ops Agent", "agent \"ops\"")
}

func assertAgentFileContains(t *testing.T, path string, want ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(data) > 2048 {
		t.Fatalf("expected %s to stay within prompt context limit, got %d bytes", path, len(data))
	}
	text := string(data)
	for _, part := range want {
		if !strings.Contains(text, part) {
			t.Fatalf("expected %s to contain %q:\n%s", path, part, text)
		}
	}
}
