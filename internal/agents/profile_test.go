package agents

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFrontmatterProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "writer.md")
	err := os.WriteFile(path, []byte(`---
name: writer
description: writing specialist
builtin_tools:
  - read_file
can_spawn: true
---

Write polished output.
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "writer" || !profile.CanSpawn {
		t.Fatalf("unexpected profile: %#v", profile)
	}
	if profile.Prompt == "" {
		t.Fatal("expected prompt body")
	}
}

func TestResolveInheritedProfile(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "base.md"), []byte(`---
name: base
builtin_tools:
  - read_file
  - list_files
allowed_skills:
  - search
can_spawn: true
memory_policy: retained
collaboration_mode: coordinator
---

Base prompt.
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(dir, "writer.md"), []byte(`---
name: writer
inherits: base
builtin_tools:
  - write_file
allowed_skills:
  - compose
collaboration_mode: shared
---

Writer prompt.
`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := Resolve(dir, "writer")
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.BuiltinTools) != 3 {
		t.Fatalf("expected merged builtin tools, got %#v", profile.BuiltinTools)
	}
	if profile.MemoryPolicy != "retained" {
		t.Fatalf("expected inherited memory policy, got %#v", profile)
	}
	if profile.CollaborationMode != "shared" {
		t.Fatalf("expected child collaboration mode override, got %#v", profile)
	}
	if profile.Prompt == "" || profile.Prompt == "Writer prompt." {
		t.Fatalf("expected merged prompt body, got %#v", profile.Prompt)
	}
}
