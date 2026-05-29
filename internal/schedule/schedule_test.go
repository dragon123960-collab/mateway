package schedule

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreCreateDueAndMarkRan(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 5, 29, 16, 0, 0, 0, time.UTC)
	store := Store{Home: home, Now: func() time.Time { return now }}
	task, err := store.Create(CreateInput{
		Channel:    "feishu",
		ThreadID:   "chat_1",
		SessionKey: "feishu:chat_1",
		Text:       "say hi",
		RunAt:      now.Add(-time.Minute),
		Interval:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.ID == "" {
		t.Fatal("expected id")
	}
	due, err := store.Due(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Text != "say hi" {
		t.Fatalf("unexpected due tasks: %#v", due)
	}
	if err := store.MarkRan(due[0], now); err != nil {
		t.Fatal(err)
	}
	updated, err := store.read(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "active" || updated.RunAt == task.RunAt || updated.LastRunAt == "" {
		t.Fatalf("unexpected updated task: %#v", updated)
	}
	if filepath.Base(store.dir()) != "schedules" {
		t.Fatalf("unexpected dir: %s", store.dir())
	}
}
