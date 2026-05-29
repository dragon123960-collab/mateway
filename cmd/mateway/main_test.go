package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/runtime"
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
