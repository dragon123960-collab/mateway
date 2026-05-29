package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/session"
)

func TestDistillSessionWritesReflectionWithoutChangingTasks(t *testing.T) {
	state := session.State{
		Key: "cli:test",
		Tasks: []session.TaskNode{
			{ID: "task-1", Goal: "完成的任务", Status: "completed"},
			{ID: "task-2", Goal: "还没完成", Status: "running"},
		},
		Pending: &session.PendingAction{Kind: "user_input", Question: "请补充主题"},
	}
	result, err := DistillSession(SessionDistillInput{Home: t.TempDir(), State: state, Reason: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"type: reflection", "session:cli:test", "task-1 completed", "task-2 running", "Pending: user_input"} {
		if !strings.Contains(text, want) {
			t.Fatalf("distill missing %q:\n%s", want, text)
		}
	}
	if state.Tasks[1].Status != "running" {
		t.Fatalf("distill mutated state: %#v", state.Tasks)
	}
}

func TestDistillProjectWritesReflectionFromProjectMemory(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "workspace", "memory")
	projectPath := filepath.Join(root, "projects", "demo", "decisions", "architecture.md")
	writeFile(t, projectPath, `---
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
`)
	writeFile(t, filepath.Join(root, "projects", "other", "decisions", "skip.md"), `---
type: decision
scope: project
project_id: other
visibility: private
status: active
sources:
  - trace:other
confidence: high
created_at: 2026-05-29
updated_at: 2026-05-29
schema_version: 1
---
Other project.
`)
	result, err := DistillProject(ProjectDistillInput{Home: home, MemoryRoot: root, ProjectID: "demo", Reason: "close"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Entries != 1 {
		t.Fatalf("entries = %d", result.Entries)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"project_id: demo", "projects/demo/decisions/architecture.md", "Use hook-first runtime boundaries"} {
		if !strings.Contains(text, want) {
			t.Fatalf("distill missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Other project") {
		t.Fatalf("distill included other project:\n%s", text)
	}
	audit, err := os.ReadFile(filepath.Join(home, "observe", "audit", "memory.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "project_distilled") {
		t.Fatalf("missing audit:\n%s", audit)
	}
}
