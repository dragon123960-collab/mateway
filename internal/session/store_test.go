package session

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileStoreRoundTrip(t *testing.T) {
	store := NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	now := time.Date(2026, 5, 20, 11, 0, 0, 0, time.FixedZone("CST", 8*3600))
	state := State{
		SessionKey:   "feishu:thread-1",
		Channel:      "feishu",
		UserID:       "u1",
		ThreadID:     "thread-1",
		TurnCount:    2,
		ActiveTaskID: "task-1",
		TaskOrder:    []string{"task-1"},
		Tasks: map[string]TaskState{
			"task-1": {
				ID:          "task-1",
				TraceID:     "trace-1",
				UserText:    "你好",
				PlanSummary: "say hi",
				Status:      TaskCompleted,
				StartedAt:   now,
				FinishedAt:  now,
				UpdatedAt:   now,
			},
		},
		CreatedAt: now,
		UpdatedAt: now,
		RecentTurns: []Turn{
			{Role: "user", Text: "你好", At: now},
			{Role: "assistant", Text: "你好，我在。", At: now},
		},
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("feishu:thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SessionKey != state.SessionKey || loaded.TurnCount != state.TurnCount {
		t.Fatalf("unexpected state %#v", loaded)
	}
	if loaded.LastTask == nil || loaded.LastTask.TraceID != "trace-1" {
		t.Fatalf("expected last task to round-trip, got %#v", loaded.LastTask)
	}
}

func TestApplyTaskKeepsRecentTurnsBounded(t *testing.T) {
	now := time.Date(2026, 5, 20, 11, 0, 0, 0, time.UTC)
	state := State{SessionKey: "cli:cli", CreatedAt: now, Tasks: map[string]TaskState{}}
	for i := 0; i < 6; i++ {
		state = ApplyTask(state, StateMeta{SessionKey: "cli:cli", Channel: "cli"}, AppendTaskInput{
			Task: TaskState{
				ID:        "task",
				UserText:  "user turn",
				StartedAt: now.Add(time.Duration(i) * time.Minute),
				Status:    TaskOpen,
			},
			AssistantReply: "assistant turn",
			At:             now.Add(time.Duration(i) * time.Minute),
			Activate:       true,
		})
	}
	if got := len(state.RecentTurns); got != recentTurnLimit {
		t.Fatalf("expected %d recent turns, got %d", recentTurnLimit, got)
	}
	if !strings.Contains(state.RecentTurns[0].Text, "user turn") && !strings.Contains(state.RecentTurns[0].Text, "assistant turn") {
		t.Fatalf("expected recent turns preserved, got %#v", state.RecentTurns)
	}
}

func TestNormalizeMigratesLegacyLastTask(t *testing.T) {
	now := time.Now()
	store := NewFileStore(filepath.Join(t.TempDir(), "sessions"))
	state := State{
		SessionKey: "cli:cli",
		LastTask: &TaskState{
			ID:        "legacy",
			TraceID:   "trace",
			UserText:  "hello",
			Status:    TaskCompleted,
			UpdatedAt: now,
		},
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("cli:cli")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ActiveTaskID != "" {
		t.Fatalf("expected completed legacy task not to become active, got %q", loaded.ActiveTaskID)
	}
	if _, ok := loaded.Tasks["legacy"]; !ok {
		t.Fatalf("expected migrated task in map, got %#v", loaded.Tasks)
	}
}

func TestApplyTaskDoesNotKeepFailedTaskActive(t *testing.T) {
	now := time.Now()
	state := ApplyTask(State{SessionKey: "cli:cli", CreatedAt: now, Tasks: map[string]TaskState{}}, StateMeta{SessionKey: "cli:cli", Channel: "cli"}, AppendTaskInput{
		Task: TaskState{
			ID:        "failed-task",
			UserText:  "run command",
			Status:    TaskFailed,
			Failed:    true,
			StartedAt: now,
		},
		AssistantReply: "failed",
		At:             now,
		Activate:       true,
	})
	if state.ActiveTaskID != "" {
		t.Fatalf("expected failed task not to remain active, got %q", state.ActiveTaskID)
	}
}
