package agentprofile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/config"
)

func TestStoreCreateAndPromoteCoreProfileProposal(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	target := filepath.Join(workspace, "agents", "main", "user.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old preference"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewStore(&config.Root{App: config.AppConfig{Home: home, Workspace: workspace}})
	proposal, err := store.Create(CreateInput{TargetPath: target, NewContent: "new preference"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old preference" {
		t.Fatalf("target changed before promote: %q", data)
	}
	if proposal.AgentID != "main" || proposal.Status != "proposed" || !strings.Contains(proposal.Diff, "-old preference") {
		t.Fatalf("unexpected proposal: %#v", proposal)
	}
	promoted, backupDir, err := store.Promote(proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if promoted.Status != "promoted" {
		t.Fatalf("status = %q", promoted.Status)
	}
	data, err = os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new preference" {
		t.Fatalf("target = %q", data)
	}
	backup, err := os.ReadFile(filepath.Join(backupDir, "user.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != "old preference" {
		t.Fatalf("backup = %q", backup)
	}
}

func TestStoreRejectsUnsafeCoreProfileProposal(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	target := filepath.Join(workspace, "agents", "main", "tools.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewStore(&config.Root{App: config.AppConfig{Home: home, Workspace: workspace}})
	_, err := store.Create(CreateInput{TargetPath: target, NewContent: "[TOOL_CALL]\n{}"})
	if err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("expected unsafe rejection, got %v", err)
	}
}

func TestCoreTargetAgentDoesNotProtectAgentSkills(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	store := NewStore(&config.Root{App: config.AppConfig{Home: home, Workspace: workspace}})
	path := filepath.Join(workspace, "agents", "main", "skills", "demo", "SKILL.md")
	if _, ok := store.CoreTargetAgent(path); ok {
		t.Fatal("agent skill should not be treated as core profile")
	}
	if agentID, ok := store.CoreTargetAgent(filepath.Join(workspace, "agents", "ops", "memory.md")); !ok || agentID != "ops" {
		t.Fatalf("expected ops core profile, got %q %v", agentID, ok)
	}
	if agentID, ok := store.CoreTargetAgent(filepath.Join(workspace, "agents", "ops", "soul.md")); !ok || agentID != "ops" {
		t.Fatalf("expected ops soul core profile, got %q %v", agentID, ok)
	}
}
