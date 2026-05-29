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
}

func TestRecordTaskCompletionCreatesProposalForAcceptedToolStep(t *testing.T) {
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
}
