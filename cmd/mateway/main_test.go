package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/schedule"
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

func TestMemoryProposalRejectAcceptsReasonAfterID(t *testing.T) {
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
	id := strings.TrimSuffix(entries[0].Name(), filepath.Ext(entries[0].Name()))
	if err := run([]string{"memory", "proposal", "reject", id, "--reason", "skip"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "observe", "proposals", id+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "status: rejected") || !strings.Contains(string(data), "Rejected reason: skip") {
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

func TestMemoryReportCommandPrintsReadOnlySummary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	err = run([]string{"memory", "report"})
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
	for _, want := range []string{"memory_root:", "memory_files:", "index_entries:", "observe:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %q:\n%s", want, text)
		}
	}
}

func TestSkillListSearchInstallCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(source, []byte("---\nname: Demo Skill\ndescription: Demo install.\n---\n# Demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"skill", "install", source}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "workspace", "skills", "demo-skill", "SKILL.md")); err != nil {
		t.Fatalf("expected installed skill: %v", err)
	}

	out := captureStdout(t, func() error { return run([]string{"skill", "list"}) })
	if !strings.Contains(out, "demo-skill") || !strings.Contains(out, "software-install") {
		t.Fatalf("unexpected skill list:\n%s", out)
	}
	out = captureStdout(t, func() error { return run([]string{"skill", "search", "--all", "software install"}) })
	if !strings.Contains(out, "skills.sh") || !strings.Contains(out, "skillhub.cn") || !strings.Contains(out, "clawhub.ai") {
		t.Fatalf("unexpected skill search:\n%s", out)
	}
}

func TestHomeReportCommandClassifiesDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "docker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "mystery"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() error { return run([]string{"home", "report"}) })
	if !strings.Contains(out, "- workspace: agent workspace, memory, skills") {
		t.Fatalf("missing expected workspace:\n%s", out)
	}
	if !strings.Contains(out, "- docker: legacy/local service data") {
		t.Fatalf("missing local docker:\n%s", out)
	}
	if !strings.Contains(out, "- mystery: not recognized by current clean layout") {
		t.Fatalf("missing unknown dir:\n%s", out)
	}
}

func TestScheduleRunDueCommandRunsCliTask(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	store := schedule.Store{Home: home}
	_, err := store.Create(schedule.CreateInput{
		SessionKey: "cli:test-schedule",
		Text:       "/read workspace/memory/README.md",
		RunAt:      time.Now().Add(-time.Minute),
		Activate:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() error { return run([]string{"schedule", "run-due"}) })
	if !strings.Contains(out, "due: 1") || !strings.Contains(out, "ran:") {
		t.Fatalf("unexpected schedule output:\n%s", out)
	}
	tasks, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "done" {
		t.Fatalf("unexpected tasks: %#v", tasks)
	}
}

func TestScheduleCreateAndTestCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MATEWAY_HOME", home)
	if err := run([]string{"init", "--home", home}); err != nil {
		t.Fatal(err)
	}
	runAt := time.Now().Add(time.Hour).Format(time.RFC3339)
	out := captureStdout(t, func() error {
		return run([]string{"schedule", "create", "--run-at", runAt, "/read workspace/memory/README.md"})
	})
	if !strings.Contains(out, "status: pending") || !strings.Contains(out, "mateway schedule test") {
		t.Fatalf("unexpected create output:\n%s", out)
	}
	tasks, err := schedule.Store{Home: home}.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != "pending" {
		t.Fatalf("unexpected tasks: %#v", tasks)
	}
	out = captureStdout(t, func() error { return run([]string{"schedule", "test", tasks[0].ID}) })
	if !strings.Contains(out, "test: success") {
		t.Fatalf("unexpected test output:\n%s", out)
	}
	tasks, err = schedule.Store{Home: home}.List()
	if err != nil {
		t.Fatal(err)
	}
	if tasks[0].Status != "active" || tasks[0].TestedAt == "" {
		t.Fatalf("unexpected tested task: %#v", tasks[0])
	}
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	oldStdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	err = fn()
	_ = write.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(read); err != nil {
		t.Fatal(err)
	}
	return out.String()
}
