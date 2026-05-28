package main

import (
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
