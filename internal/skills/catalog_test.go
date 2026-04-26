package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogRefreshLoadsSKILLMarkdownAndMeta(t *testing.T) {
	root := t.TempDir()
	goodDir := filepath.Join(root, "echo")
	badDir := filepath.Join(root, "broken")
	if err := os.MkdirAll(goodDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goodDir, "SKILL.md"), []byte(`---
name: echo
description: Echo skill
---

# Echo
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goodDir, "_meta.json"), []byte(`{"slug":"echo","mateway":{"type":"cli","entry":"./run.sh","read_only":true,"risk_level":"low","tags":["echo","test"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "SKILL.md"), []byte(`---
description: Missing name
---

# Broken
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "_meta.json"), []byte(`{"mateway":{"type":"cli"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	catalog := NewCatalog([]string{root})
	err := catalog.Refresh()
	if err == nil {
		t.Fatal("expected partial refresh error")
	}
	skills := catalog.Snapshot()
	if len(skills) != 1 || skills[0].Manifest.Name != "echo" || !skills[0].Executable {
		t.Fatalf("unexpected skills snapshot: %#v", skills)
	}
	if skills[0].Manifest.Entry != "./run.sh" {
		t.Fatalf("expected runtime entry to be loaded from _meta.json, got %#v", skills[0].Manifest)
	}
}

func TestCatalogRefreshLoadsDocOnlySkill(t *testing.T) {
	root := t.TempDir()
	docDir := filepath.Join(root, "summarize")
	if err := os.MkdirAll(docDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docDir, "SKILL.md"), []byte(`---
name: summarize
description: Summarize content
---

# Summarize
`), 0o644); err != nil {
		t.Fatal(err)
	}

	catalog := NewCatalog([]string{root})
	if err := catalog.Refresh(); err != nil {
		t.Fatalf("expected doc-only SKILL.md to load, got %v", err)
	}
	snapshot := catalog.Snapshot()
	if len(snapshot) != 1 || snapshot[0].Manifest.Name != "summarize" {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if snapshot[0].Executable {
		t.Fatalf("expected doc-only skill to be non-executable")
	}
}

func TestCatalogRefreshLoadsSkillResources(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "designer")
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "references", "patterns"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(`---
name: designer
description: Design skill
---

# Designer
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "run.sh"), []byte("echo ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "patterns", "guide.md"), []byte("# guide"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "banner.txt"), []byte("asset"), 0o644); err != nil {
		t.Fatal(err)
	}

	catalog := NewCatalog([]string{root})
	if err := catalog.Refresh(); err != nil {
		t.Fatal(err)
	}
	snapshot := catalog.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	got := snapshot[0].Resources
	if len(got.Scripts) != 1 || got.Scripts[0] != "scripts/run.sh" {
		t.Fatalf("unexpected scripts: %#v", got.Scripts)
	}
	if len(got.References) != 1 || got.References[0] != "references/patterns/guide.md" {
		t.Fatalf("unexpected references: %#v", got.References)
	}
	if len(got.Assets) != 1 || got.Assets[0] != "assets/banner.txt" {
		t.Fatalf("unexpected assets: %#v", got.Assets)
	}
}

func TestCatalogRefreshLoadsDeclaredExtraResourceDirs(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "builder")
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(`---
name: builder
description: Builder skill
resource_dirs:
  - templates
---

# Builder
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "templates", "page.md"), []byte("# page"), 0o644); err != nil {
		t.Fatal(err)
	}

	catalog := NewCatalog([]string{root})
	if err := catalog.Refresh(); err != nil {
		t.Fatal(err)
	}
	got := catalog.Snapshot()[0].Resources
	if len(got.Extra["templates"]) != 1 || got.Extra["templates"][0] != "templates/page.md" {
		t.Fatalf("unexpected extra resources: %#v", got.Extra)
	}
}
