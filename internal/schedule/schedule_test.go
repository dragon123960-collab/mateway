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
		SessionKey:  "feishu:chat_1",
		Text:        "say hi",
		RunAt:       now.Add(-time.Minute),
		Interval:    time.Hour,
		RequireTest: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.ID == "" {
		t.Fatal("expected id")
	}
	if task.Status != "pending" {
		t.Fatalf("status = %q want pending", task.Status)
	}
	task, err = store.Activate(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	due, err := store.Due(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].Text != "say hi" {
		t.Fatalf("unexpected due tasks: %#v", due)
	}
	record, err := store.RecordRun(RunRecord{TaskID: due[0].ID, Kind: "scheduled", Status: "success"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRan(due[0], now, record); err != nil {
		t.Fatal(err)
	}
	updated, err := store.read(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "active" || updated.RunAt == task.RunAt || updated.LastRunAt == "" {
		t.Fatalf("unexpected updated task: %#v", updated)
	}
	if updated.LastRunID == "" || updated.LastRunStatus != "success" {
		t.Fatalf("missing run metadata: %#v", updated)
	}
	if filepath.Base(store.dir()) != "schedules" {
		t.Fatalf("unexpected dir: %s", store.dir())
	}
}

func TestStoreRecordAndReadRunbook(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 5, 29, 16, 0, 0, 0, time.UTC)
	store := Store{Home: home, Now: func() time.Time { return now }}
	runbook, err := store.RecordRunbook(Runbook{
		TaskID:     "sch_1",
		Text:       "create deck",
		Lane:       "workflow",
		Steps:      []string{"draft", "review", "finalize"},
		SkillPaths: []string{"/tmp/skills/ppt/SKILL.md"},
		OutputRoot: "/tmp/outputs/deck",
	})
	if err != nil {
		t.Fatal(err)
	}
	if runbook.ID == "" || runbook.CreatedAt == "" {
		t.Fatalf("missing runbook metadata: %#v", runbook)
	}
	loaded, err := store.ReadRunbook(runbook.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Lane != "workflow" || loaded.OutputRoot != "/tmp/outputs/deck" || len(loaded.Steps) != 3 {
		t.Fatalf("unexpected runbook: %#v", loaded)
	}
	if filepath.Base(store.runbooksDir()) != "runbooks" {
		t.Fatalf("unexpected runbook dir: %s", store.runbooksDir())
	}
}

func TestStoreMarkTestedPersistsRunbookID(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 5, 29, 16, 0, 0, 0, time.UTC)
	store := Store{Home: home, Now: func() time.Time { return now }}
	task, err := store.Create(CreateInput{Text: "say hi", RunAt: now, RequireTest: true})
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.RecordRun(RunRecord{TaskID: task.ID, Kind: "test", Status: "success", RunbookID: "runbook_1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkTested(task, now, record); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Read(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "active" || updated.RunbookID != "runbook_1" {
		t.Fatalf("unexpected tested task: %#v", updated)
	}
}
