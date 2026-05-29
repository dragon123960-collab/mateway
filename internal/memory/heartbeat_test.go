package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunLintIndexHeartbeatWritesIndexAndAudit(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "workspace", "memory")
	writeFile(t, filepath.Join(root, "agents", "main", "experiences", "read-local-file.md"), `---
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
Use file.read.
`)
	result, err := RunLintIndexHeartbeat(HeartbeatInput{Home: home, MemoryRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if result.Files != 1 || result.Entries != 1 || len(result.Issues) != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(home, "indexes", "memory_index.json")); err != nil {
		t.Fatal(err)
	}
	audit, err := os.ReadFile(filepath.Join(home, "observe", "audit", "memory.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "memory_heartbeat") {
		t.Fatalf("missing audit:\n%s", audit)
	}
}

func TestRunLintIndexHeartbeatSkipsIndexOnLintError(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "workspace", "memory")
	writeFile(t, filepath.Join(root, "user", "long", "bad.md"), "# Missing frontmatter\n")
	result, err := RunLintIndexHeartbeat(HeartbeatInput{Home: home, MemoryRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Issues) == 0 {
		t.Fatalf("expected lint issue: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(home, "indexes", "memory_index.json")); !os.IsNotExist(err) {
		t.Fatalf("index should not be written on lint error, err=%v", err)
	}
}
