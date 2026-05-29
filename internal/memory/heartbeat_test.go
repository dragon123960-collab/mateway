package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestServeHeartbeatRunsConfiguredJob(t *testing.T) {
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
	ctx, cancel := context.WithCancel(context.Background())
	var calls int
	err := ServeHeartbeat(ctx, HeartbeatServeInput{
		Home:       home,
		MemoryRoot: root,
		Jobs:       []string{"memory_lint", "memory_index_rebuild"},
		Interval:   time.Millisecond,
		OnResult: func(result HeartbeatResult) {
			calls++
			if result.Entries != 1 {
				t.Fatalf("entries = %d want 1", result.Entries)
			}
		},
		Sleep: func(context.Context, time.Duration) error {
			cancel()
			return context.Canceled
		},
	})
	if err != context.Canceled {
		t.Fatalf("err = %v want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d want 1", calls)
	}
}
