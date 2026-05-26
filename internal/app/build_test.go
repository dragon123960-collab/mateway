package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitSeedsDefaultSoftwareInstallSkill(t *testing.T) {
	home := t.TempDir()
	gotHome, err := Init(home)
	if err != nil {
		t.Fatal(err)
	}
	if gotHome != home {
		t.Fatalf("expected init home %q, got %q", home, gotHome)
	}
	path := filepath.Join(home, "workspace", "skills", "software-install", "SKILL.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected software-install skill to be seeded at %s: %v", path, err)
	}
}
