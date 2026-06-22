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

func TestSearchRootFiltersLifecycleByDefault(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "projects", "mateway", "environment", "old-host.md"), `---
type: fact
scope: project
visibility: private
status: superseded
topic_path: projects/mateway/environment
subject: staging_server
predicate: ssh_host
object: 10.0.0.8
sources:
  - trace:old
confidence: high
created_at: 2026-05-01
updated_at: 2026-05-01
schema_version: 1
---
Old staging host 10.0.0.8.
`)
	writeFile(t, filepath.Join(root, "projects", "mateway", "environment", "new-host.md"), `---
type: fact
scope: project
visibility: private
status: active
topic_path: projects/mateway/environment
subject: staging_server
predicate: ssh_host
object: 10.0.0.9
sources:
  - trace:new
confidence: high
created_at: 2026-06-01
updated_at: 2026-06-01
schema_version: 1
---
Current staging host 10.0.0.9.
`)
	writeFile(t, filepath.Join(root, "projects", "mateway", "environment", "expired-host.md"), `---
type: fact
scope: project
visibility: private
status: active
topic_path: projects/mateway/environment
subject: staging_server
predicate: ssh_host
object: 10.0.0.7
sources:
  - trace:expired
confidence: medium
valid_until: 2026-01-01
created_at: 2025-12-01
updated_at: 2025-12-01
schema_version: 1
---
Expired staging host 10.0.0.7.
`)
	results, _, err := SearchRoot(root, SearchOptions{Query: "staging host", TopicPath: "projects/mateway/environment", Subject: "staging_server", Predicate: "ssh_host"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Path != "projects/mateway/environment/new-host.md" {
		t.Fatalf("expected only active current memory, got %#v", results)
	}
	history, _, err := SearchRoot(root, SearchOptions{Query: "staging host", IncludeHistory: true, TopicPath: "projects/mateway/environment", Subject: "staging_server", Predicate: "ssh_host"})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("expected history results, got %#v", history)
	}
}

func TestSearchRootRequiresQuery(t *testing.T) {
	_, _, err := SearchRoot(t.TempDir(), SearchOptions{})
	if err == nil {
		t.Fatal("expected query error")
	}
}
