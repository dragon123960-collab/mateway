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

func TestAddTraceRefUpdatesLatestTraceAndDedupes(t *testing.T) {
	state := State{Key: "cli:test"}
	task := state.StartTask("search")
	state.AddTraceRef(task.ID, TraceRef{TraceID: "trace-1", TracePath: "/tmp/one.jsonl", Phase: "plan_review", MessageID: "msg-1"})
	state.AddTraceRef(task.ID, TraceRef{TraceID: "trace-1", TracePath: "/tmp/one.jsonl", Phase: "execute", MessageID: "msg-2"})
	updated := state.TaskByID(task.ID)
	if updated == nil {
		t.Fatal("expected task")
	}
	if updated.TraceID != "trace-1" || updated.TracePath != "/tmp/one.jsonl" {
		t.Fatalf("expected latest trace on task, got %#v", updated)
	}
	if len(updated.Execution.TraceRefs) != 1 || updated.Execution.TraceRefs[0].Phase != "execute" || updated.Execution.TraceRefs[0].MessageID != "msg-2" {
		t.Fatalf("expected deduped trace ref update, got %#v", updated.Execution.TraceRefs)
	}
}
