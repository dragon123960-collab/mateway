package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dongping/mateway/internal/config"
)

func TestInitCreatesWorkspaceAndConfig(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.App.Home = filepath.Join(root, ".mateway")
	cfg.App.Workspace = filepath.Join(cfg.App.Home, "workspace")
	cfg.Skills.Roots = []string{
		filepath.Join(cfg.App.Home, "skills"),
		filepath.Join(cfg.App.Workspace, "skills"),
	}

	if err := Init(cfg); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		filepath.Join(cfg.App.Home, "config", "config.yaml"),
		filepath.Join(cfg.App.Home, "config", "models", "README.md"),
		filepath.Join(cfg.App.Home, "config", "channels", "README.md"),
		filepath.Join(cfg.App.Workspace, "AGENT.md"),
		filepath.Join(cfg.App.Workspace, "USER.md"),
		filepath.Join(cfg.App.Workspace, "SOUL.md"),
		filepath.Join(cfg.App.Workspace, "agents", "default.md"),
		filepath.Join(cfg.App.Workspace, "memory", "runs"),
		filepath.Join(cfg.App.Workspace, "memory", "knowledge"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
}
