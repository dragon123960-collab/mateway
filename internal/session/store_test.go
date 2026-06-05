package session

import (
	"testing"
)

func TestStartTaskCreatesExecutionFrame(t *testing.T) {
	state := State{Key: "cli:test"}
	task := state.StartTask("创建飞书云文档")
	if task.Execution.ID == "" {
		t.Fatalf("expected execution frame id, got %#v", task.Execution)
	}
	if task.Execution.Mode != "agent_loop" || task.Execution.Status != "running" || task.Execution.OriginalTask != "创建飞书云文档" {
		t.Fatalf("unexpected execution frame: %#v", task.Execution)
	}
}

func TestEnsureExecutionFrameBackfillsOldTask(t *testing.T) {
	state := State{Key: "cli:test", Tasks: []TaskNode{{
		ID:     "task-old",
		Goal:   "旧任务",
		Status: "await_confirm",
	}}}
	frame := state.EnsureExecutionFrame("task-old")
	if frame == nil {
		t.Fatal("expected frame")
	}
	if frame.Mode != "agent_loop" || frame.Status != "awaiting_confirmation" || frame.OriginalTask != "旧任务" {
		t.Fatalf("unexpected backfilled frame: %#v", frame)
	}
}
