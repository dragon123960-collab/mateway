package memory

import (
	"path/filepath"
	"testing"
)

func TestSearchRootReturnsMatchingSnippets(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "agents", "main", "experience", "file-read.md"), `---
type: experience
scope: agent
owner_agent: main
visibility: private
status: active
tags: [file]
aliases: []
sources:
  - trace:file
confidence: high
created_at: 2026-05-29
updated_at: 2026-05-29
schema_version: 1
---
Use file.read when inspecting local README files.
`)
	writeFile(t, filepath.Join(root, "global", "preferences", "style.md"), `---
type: preference
scope: global
visibility: shared-user
status: active
tags: [style]
aliases: []
sources:
  - trace:style
confidence: high
created_at: 2026-05-29
updated_at: 2026-05-29
schema_version: 1
---
Prefer concise answers.
`)
	results, issues, err := SearchRoot(root, SearchOptions{Query: "README file", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %#v", issues)
	}
	if len(results) != 1 {
		t.Fatalf("results = %#v", results)
	}
	if results[0].Path != "agents/main/experience/file-read.md" || results[0].Sources[0] != "trace:file" {
		t.Fatalf("unexpected result: %#v", results[0])
	}
}

func TestSearchRootAppliesScopeAndType(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "user", "long", "style.md"), `---
type: preference
scope: user
visibility: private
status: active
sources:
  - trace:user
confidence: high
created_at: 2026-05-29
updated_at: 2026-05-29
schema_version: 1
---
Prefer concise answers.
`)
	writeFile(t, filepath.Join(root, "global", "patterns", "style.md"), `---
type: pattern
scope: global
visibility: shared-user
status: active
sources:
  - trace:pattern
confidence: medium
created_at: 2026-05-29
updated_at: 2026-05-29
schema_version: 1
---
Concise answers should keep evidence.
`)
	results, _, err := SearchRoot(root, SearchOptions{Query: "concise", Scope: "user", Type: "preference"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Path != "user/long/style.md" {
		t.Fatalf("unexpected results: %#v", results)
	}
}

func TestSearchRootRequiresQuery(t *testing.T) {
	_, _, err := SearchRoot(t.TempDir(), SearchOptions{})
	if err == nil {
		t.Fatal("expected query error")
	}
}
