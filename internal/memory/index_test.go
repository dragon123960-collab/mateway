package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRebuildIndexBuildsEntriesFromMarkdown(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "agents", "main", "experience", "read-local-readme.md"), `---
type: experience
scope: agent
owner_agent: main
visibility: private
status: active
tags: [tool, file]
aliases: [readme-check]
op_fingerprint: file.read:README
sources:
  - trace:abc
confidence: high
created_at: 2026-05-29
updated_at: 2026-05-29
schema_version: 1
---
# README check

Use file.read for local README inspection.
`)
	index, issues, err := RebuildIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %#v", issues)
	}
	if len(index.Entries) != 1 {
		t.Fatalf("entries = %#v", index.Entries)
	}
	entry := index.Entries[0]
	if entry.Path != "agents/main/experience/read-local-readme.md" || entry.Type != "experience" || entry.Scope != "agent" {
		t.Fatalf("unexpected entry: %#v", entry)
	}
	if len(entry.Tags) != 2 || entry.Tags[0] != "tool" || entry.Sources[0] != "trace:abc" {
		t.Fatalf("unexpected metadata: %#v", entry)
	}
	if entry.Snippet == "" {
		t.Fatalf("expected snippet: %#v", entry)
	}
}

func TestRebuildIndexSkipsInvalidDocuments(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "user", "long", "bad.md"), "# Missing frontmatter\n")
	index, issues, err := RebuildIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Entries) != 0 {
		t.Fatalf("expected no entries, got %#v", index.Entries)
	}
	if !hasIssue(issues, "missing_frontmatter") {
		t.Fatalf("expected issue, got %#v", issues)
	}
}

func TestRebuildIndexIgnoresWorkspaceDraftDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "agents", "main", "inbox", "legacy.md"), `---
type: source
scope: agent
visibility:
status: proposed
sources: []
confidence: medium
created_at: 2026-05-29
updated_at: 2026-05-29
schema_version: 1
---
Draft only.
`)
	writeFile(t, filepath.Join(root, "agents", "main", "experiences", "ok.md"), `---
type: experience
scope: agent
visibility: private
status: active
sources:
  - trace:abc
confidence: high
created_at: 2026-05-29
updated_at: 2026-05-29
schema_version: 1
---
Usable memory.
`)
	index, issues, err := RebuildIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %#v", issues)
	}
	if len(index.Entries) != 1 || index.Entries[0].Path != "agents/main/experiences/ok.md" {
		t.Fatalf("unexpected entries: %#v", index.Entries)
	}
}

func TestRebuildIndexIncludesNestedReadmeMemory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "README.md"), "# Memory root support doc\n")
	writeFile(t, filepath.Join(root, "agents", "main", "experiences", "readme.md"), `---
type: experience
scope: agent
visibility: private
status: active
sources:
  - trace:abc
confidence: high
created_at: 2026-05-29
updated_at: 2026-05-29
schema_version: 1
---
Remember README inspection notes.
`)
	index, issues, err := RebuildIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %#v", issues)
	}
	if len(index.Entries) != 1 || index.Entries[0].Path != "agents/main/experiences/readme.md" {
		t.Fatalf("unexpected entries: %#v", index.Entries)
	}
}

func TestWriteIndexWritesJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "indexes", "memory_index.json")
	index := Index{SchemaVersion: 1, Root: "/tmp/memory", Entries: []IndexEntry{{Path: "one.md", Type: "wiki"}}}
	if err := WriteIndex(path, index); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Index
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Entries) != 1 || decoded.Entries[0].Path != "one.md" {
		t.Fatalf("decoded = %#v", decoded)
	}
}
