package provisioning

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dongping/mateway/internal/config"
)

func TestProvisionerCreatesWorkspaceAndAgent(t *testing.T) {
	cfg := config.Default()
	cfg.App.Home = filepath.Join(t.TempDir(), ".mateway")
	p := Provisioner{Config: cfg}

	workspacePath, err := p.CreateWorkspace("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspacePath, "SOUL.md")); err != nil {
		t.Fatal(err)
	}

	agentPath, err := p.CreateAgent(workspacePath, "writer", "content helper")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(agentPath); err != nil {
		t.Fatal(err)
	}

	items, err := p.ListAgents(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0] != "writer" {
		t.Fatalf("unexpected agents: %#v", items)
	}
}
