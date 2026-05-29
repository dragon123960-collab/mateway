package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/session"
)

func TestTestCaseMessage(t *testing.T) {
	msg, err := testCaseMessage("read-readme")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "README.md") {
		t.Fatalf("message = %q", msg)
	}
}

func TestTestCaseCustomRequiresMessage(t *testing.T) {
	if _, err := testCaseMessage("custom"); err == nil {
		t.Fatal("expected custom case to require --message")
	}
}

func TestWriteTestRecord(t *testing.T) {
	cwd := t.TempDir()
	old, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	path, err := writeTestRecord("read-readme", "test:one", "hello", runtime.Response{Reply: channel.OutboundMessage{Text: "ok"}, TracePath: "/tmp/trace.jsonl"}, map[string]any{"ok": true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, "testdata/runs/") {
		t.Fatalf("path = %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"session": "test:one"`) {
		t.Fatalf("record = %s", data)
	}
	if !strings.Contains(string(data), `"trace_path": "/tmp/trace.jsonl"`) {
		t.Fatalf("record missing trace path = %s", data)
	}
}

func TestInitSupportsHomeFlag(t *testing.T) {
	home := t.TempDir()
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		filepath.Join("config", "config.yaml"),
		filepath.Join("workspace", "skills", "software-install", "SKILL.md"),
		filepath.Join("workspace", "skills", "connector-gap", "SKILL.md"),
	} {
		if _, err := os.Stat(filepath.Join(home, rel)); err != nil {
			t.Fatalf("expected %s under init home: %v", rel, err)
		}
	}
}

func TestMemoryLintCommandUsesHomeConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	oldStdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	err = run([]string{"memory", "lint"})
	_ = write.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(read); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "memory_root:") || !strings.Contains(text, "issues: 0") {
		t.Fatalf("unexpected lint output:\n%s", text)
	}
}

func TestMemoryIndexRebuildCommandWritesIndex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	oldStdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	err = run([]string{"memory", "index", "rebuild"})
	_ = write.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(read); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "entries: 1") || !strings.Contains(text, "memory_index.json") {
		t.Fatalf("unexpected index output:\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(home, "indexes", "memory_index.json")); err != nil {
		t.Fatalf("expected index file: %v", err)
	}
}

func TestMemorySearchCommandPrintsResults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "workspace", "memory", "user", "long", "style.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`---
type: preference
scope: user
visibility: private
status: active
sources:
  - trace:style
confidence: high
created_at: 2026-05-29
updated_at: 2026-05-29
schema_version: 1
---
Prefer concise answers with source references.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	err = run([]string{"memory", "search", "--scope", "user", "concise"})
	_ = write.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(read); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "results: 1") || !strings.Contains(text, "user/long/style.md") || !strings.Contains(text, "trace:style") {
		t.Fatalf("unexpected search output:\n%s", text)
	}
}

func TestMemoryProposalCreateListRejectCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"memory", "proposal", "create", "--title", "README inspection", "--body", "Use file.read for README.", "--source", "trace:abc"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, "observe", "proposals"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d", len(entries))
	}
	id := strings.TrimSuffix(entries[0].Name(), filepath.Ext(entries[0].Name()))
	oldStdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	err = run([]string{"memory", "proposal", "list"})
	_ = write.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(read); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "README inspection") || !strings.Contains(out.String(), "status=proposed") {
		t.Fatalf("unexpected proposal list:\n%s", out.String())
	}
	if err := run([]string{"memory", "proposal", "reject", "--reason", "skip", id}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "observe", "proposals", id+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "status: rejected") {
		t.Fatalf("expected rejected proposal:\n%s", data)
	}
}

func TestMemoryProposalCommitCommandWritesMemory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"memory", "proposal", "create", "--title", "README Inspection", "--body", "Use file.read for README.", "--source", "trace:abc"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, "observe", "proposals"))
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSuffix(entries[0].Name(), filepath.Ext(entries[0].Name()))
	if err := run([]string{"memory", "proposal", "commit", id}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "workspace", "memory", "agents", "main", "experiences", "readme-inspection.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "status: active") || !strings.Contains(string(data), "trace:abc") {
		t.Fatalf("unexpected memory:\n%s", data)
	}
}

func TestMemoryDistillSessionCommandWritesReflection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	store := session.NewStore(home)
	state := session.State{
		Key: "cli:test",
		Tasks: []session.TaskNode{
			{ID: "task-1", Goal: "未完成任务", Status: "running"},
		},
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"memory", "distill", "session", "cli:test"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, "observe", "reflections"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(home, "observe", "reflections", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "task-1 running") {
		t.Fatalf("unexpected distill:\n%s", data)
	}
	loaded, err := store.Load("cli:test")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Tasks[0].Status != "running" {
		t.Fatalf("distill should not complete task: %#v", loaded.Tasks)
	}
}

func TestMemoryDistillProjectCloseCommandWritesReflection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "workspace", "memory", "projects", "demo", "decisions", "architecture.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`---
type: decision
scope: project
project_id: demo
visibility: private
status: active
sources:
  - trace:demo
confidence: high
created_at: 2026-05-29
updated_at: 2026-05-29
schema_version: 1
---
Use hook-first runtime boundaries.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"memory", "distill", "project", "close", "demo"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, "observe", "reflections"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(home, "observe", "reflections", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "project_id: demo") || !strings.Contains(string(data), "hook-first runtime") {
		t.Fatalf("unexpected project distill:\n%s", data)
	}
}

func TestMemoryHeartbeatLintIndexCommandWritesIndex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"memory", "heartbeat", "lint-index"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "indexes", "memory_index.json")); err != nil {
		t.Fatalf("expected index: %v", err)
	}
	audit, err := os.ReadFile(filepath.Join(home, "observe", "audit", "memory.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "memory_heartbeat") {
		t.Fatalf("missing heartbeat audit:\n%s", audit)
	}
}
