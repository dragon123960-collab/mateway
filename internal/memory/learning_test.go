package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/session"
)

func TestRecordTaskCompletionWritesDiaryOnlyForLowValueTask(t *testing.T) {
	home := t.TempDir()
	result, err := RecordTaskCompletion(LearningEvent{
		Home:       home,
		SessionKey: "cli:test",
		Task:       session.TaskNode{ID: "task-1", Goal: "hello", Status: "completed"},
		FinalText:  "收到：hello",
		TraceID:    "trace-1",
		TracePath:  filepath.Join(home, "trace", "trace-1.jsonl"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.DiaryPath == "" || result.Proposal != nil {
		t.Fatalf("unexpected result: %#v", result)
	}
	data, err := os.ReadFile(result.DiaryPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "type: diary") || !strings.Contains(text, "trace:trace-1") || !strings.Contains(text, "task:task-1") {
		t.Fatalf("unexpected diary:\n%s", text)
	}
	ledger, err := os.ReadFile(filepath.Join(home, "observe", "learning", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ledger), `"type":"task_completed"`) || !strings.Contains(string(ledger), `"task_id":"task-1"`) {
		t.Fatalf("unexpected learning ledger:\n%s", ledger)
	}
}

func TestRecordTaskCompletionSkipsProposalForPlainReadStep(t *testing.T) {
	home := t.TempDir()
	result, err := RecordTaskCompletion(LearningEvent{
		Home:       home,
		SessionKey: "cli:test",
		Task: session.TaskNode{
			ID:     "task-2",
			Goal:   "请总结 README",
			Status: "completed",
			Steps: []session.TaskStep{{
				Tool:    "file.read",
				Status:  "accepted",
				Summary: "read README",
			}},
		},
		FinalText: "总结完成",
		TraceID:   "trace-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Proposal != nil {
		t.Fatalf("plain read step should not create proposal: %#v", result.Proposal)
	}
}

func TestRecordTaskCompletionSkipsProposalForPlainWriteStep(t *testing.T) {
	home := t.TempDir()
	result, err := RecordTaskCompletion(LearningEvent{
		Home:       home,
		SessionKey: "cli:test",
		Task: session.TaskNode{
			ID:     "task-write",
			Goal:   "写入报告",
			Status: "completed",
			Steps: []session.TaskStep{{
				Tool:    "file.write",
				Status:  "accepted",
				Summary: "wrote report",
			}},
		},
		FinalText: "完成",
		TraceID:   "trace-write",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Proposal != nil {
		t.Fatalf("plain write step should not create immediate proposal: %#v", result.Proposal)
	}
}

func TestRecordTaskCompletionCreatesProposalForExplicitMemoryCue(t *testing.T) {
	home := t.TempDir()
	result, err := RecordTaskCompletion(LearningEvent{
		Home:       home,
		SessionKey: "cli:test",
		Task: session.TaskNode{
			ID:     "task-2",
			Goal:   "记住 README 总结流程",
			Status: "completed",
			Steps: []session.TaskStep{{
				Tool:    "file.read",
				Status:  "accepted",
				Summary: "read README",
			}},
		},
		FinalText: "总结完成",
		TraceID:   "trace-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Proposal == nil {
		t.Fatalf("expected proposal: %#v", result)
	}
	data, err := os.ReadFile(result.Proposal.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "Accepted tool step: file.read") || !strings.Contains(text, "trace:trace-2") || !strings.Contains(text, "task:task-2") {
		t.Fatalf("unexpected proposal:\n%s", text)
	}
}

func TestRecordTaskCompletionWritesReflectionForFailedStep(t *testing.T) {
	home := t.TempDir()
	result, err := RecordTaskCompletion(LearningEvent{
		Home:       home,
		SessionKey: "cli:test",
		Task: session.TaskNode{
			ID:     "task-3",
			Goal:   "读取缺失文件",
			Status: "completed",
			Steps: []session.TaskStep{{
				Tool:    "file.read",
				Status:  "failed",
				Summary: "file not found",
			}},
		},
		FinalText: "读取失败",
		TraceID:   "trace-3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ReflectionPath == "" {
		t.Fatalf("expected reflection: %#v", result)
	}
	data, err := os.ReadFile(result.ReflectionPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "type: reflection") || !strings.Contains(text, "file not found") {
		t.Fatalf("unexpected reflection:\n%s", text)
	}
	for _, want := range []string{"Failed or suspect steps", "Likely cause", "Alternative strategy", "Related tools", "Sources"} {
		if !strings.Contains(text, want) {
			t.Fatalf("reflection missing %q:\n%s", want, text)
		}
	}
}

func TestRecordTaskCompletionWritesSkillUsageLedger(t *testing.T) {
	home := t.TempDir()
	_, err := RecordTaskCompletion(LearningEvent{
		Home:       home,
		SessionKey: "cli:test",
		Task: session.TaskNode{
			ID:     "task-skill",
			Goal:   "use skill",
			Status: "completed",
			Steps: []session.TaskStep{{
				Tool:    "file.read",
				Status:  "accepted",
				Summary: "read file",
			}},
		},
		TraceID: "trace-skill",
		Skills:  []SkillEvidence{{Name: "fresh-search", Path: filepath.Join(home, "workspace", "skills", "fresh-search", "SKILL.md"), Scope: "shared"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "observe", "skill_usage", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"type":"skill_usage"`) || !strings.Contains(text, `"name":"fresh-search"`) || !strings.Contains(text, `"tool_sequence":["file.read"]`) {
		t.Fatalf("unexpected skill usage ledger:\n%s", text)
	}
}
