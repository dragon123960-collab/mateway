package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLintRootAcceptsValidMemoryEntry(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "agents", "main", "memory.md"), `---
type: wiki
scope: agent
owner_agent: main
visibility: private
status: active
tags: []
aliases: []
sources:
  - trace:abc
confidence: high
created_at: 2026-05-29
updated_at: 2026-05-29
schema_version: 1
---
# Agent Memory

Verified note.
`)
	result, err := LintRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Files != 1 {
		t.Fatalf("files = %d", result.Files)
	}
	if result.HasErrors() || len(result.Issues) != 0 {
		t.Fatalf("unexpected issues: %#v", result.Issues)
	}
}

func TestLintRootReportsMissingFrontmatter(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "user", "long", "preference.md"), "# Preference\n")
	result, err := LintRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if !result.HasErrors() || !hasIssue(result.Issues, "missing_frontmatter") {
		t.Fatalf("expected missing frontmatter error, got %#v", result.Issues)
	}
}

func TestLintRootSkipsWorkspaceDraftDirs(t *testing.T) {
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
	writeFile(t, filepath.Join(root, "agents", "main", "recent", "2026-05-29.md"), "# Recent scratch\n")
	writeFile(t, filepath.Join(root, "agents", "main", "learning", "note.md"), "# Learning scratch\n")
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
	result, err := LintRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Files != 1 {
		t.Fatalf("files = %d, issues = %#v", result.Files, result.Issues)
	}
	if result.HasErrors() {
		t.Fatalf("unexpected issues: %#v", result.Issues)
	}
}

func TestLintRootIndexesNestedReadmeMemory(t *testing.T) {
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
	result, err := LintRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.Files != 1 {
		t.Fatalf("files = %d, issues = %#v", result.Files, result.Issues)
	}
	if result.HasErrors() {
		t.Fatalf("unexpected issues: %#v", result.Issues)
	}
}

func TestLintRootWarnsForActiveMemoryWithoutSources(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "global", "preferences", "answer-style.md"), `---
type: preference
scope: global
visibility: shared-user
status: active
sources: []
confidence: medium
created_at: 2026-05-29
updated_at: 2026-05-29
schema_version: 1
---
Prefer concise answers.
`)
	result, err := LintRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.HasErrors() || !hasIssue(result.Issues, "active_without_sources") {
		t.Fatalf("expected source warning only, got %#v", result.Issues)
	}
}

func TestLintRootReportsSecretLikeBody(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "projects", "demo", "context", "token.md"), `---
type: wiki
scope: project
project_id: demo
visibility: private
status: proposed
sources:
  - trace:abc
confidence: low
created_at: 2026-05-29
updated_at: 2026-05-29
schema_version: 1
---
api_key: should-not-live-here
`)
	result, err := LintRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if !result.HasErrors() || !hasIssue(result.Issues, "possible_secret") {
		t.Fatalf("expected secret-like issue, got %#v", result.Issues)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasIssue(issues []Issue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
