package schedule

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreCreateListShowAndStatus(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	now := time.Date(2026, 5, 21, 9, 0, 0, 0, time.UTC)
	task, path, err := store.Create(CreateInput{
		Title:   "Daily AI Trends",
		Prompt:  "Collect AI trend articles with sources.",
		DailyAt: "09:00",
		Now:     now,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if task.ID != "daily-ai-trends" {
		t.Fatalf("unexpected id %q", task.ID)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected task file: %v", err)
	}
	tasks, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != task.ID {
		t.Fatalf("unexpected tasks %#v", tasks)
	}
	shown, _, err := store.Show(task.ID)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if shown.Prompt != task.Prompt {
		t.Fatalf("unexpected shown task %#v", shown)
	}
	paused, _, err := store.SetStatus(task.ID, StatusPaused)
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if paused.Status != StatusPaused {
		t.Fatalf("expected paused, got %#v", paused)
	}
}

func TestStoreUpdateTask(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	task, _, err := store.Create(CreateInput{ID: "ai-trends", Title: "AI Trends", Prompt: "Collect AI trends.", DailyAt: "09:00"})
	if err != nil {
		t.Fatal(err)
	}
	spec := ScheduleSpec{Kind: "daily", DailyAt: "10:30"}
	updated, _, err := store.Update(task.ID, UpdateInput{Prompt: "Collect AI trend papers.", Schedule: &spec})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Prompt != "Collect AI trend papers." || Summary(updated.Schedule) != "daily@10:30" {
		t.Fatalf("unexpected updated task %#v", updated)
	}
}

func TestStoreDueSkipsPausedAndAlreadyRunToday(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	active, _, err := store.Create(CreateInput{ID: "active", Title: "Active", Prompt: "Run active", DailyAt: "09:00", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	paused, _, err := store.Create(CreateInput{ID: "paused", Title: "Paused", Prompt: "Run paused", DailyAt: "09:00", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SetStatus(paused.ID, StatusPaused); err != nil {
		t.Fatal(err)
	}
	due, err := store.Due(now)
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 1 || due[0].ID != active.ID {
		t.Fatalf("unexpected due tasks %#v", due)
	}
	if err := store.WriteRunState(RunState{TaskID: active.ID, LastRunAt: now, Status: "ok"}); err != nil {
		t.Fatalf("write state: %v", err)
	}
	due, err = store.Due(now.Add(time.Hour))
	if err != nil {
		t.Fatalf("due again: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("expected no due tasks, got %#v", due)
	}
}

func TestStoreDueWeeklyAndInterval(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	friday := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	weekly, _, err := store.Create(CreateInput{ID: "weekly", Title: "Weekly", Prompt: "Run weekly", WeeklyAt: "09:00", Weekday: "friday", Now: friday})
	if err != nil {
		t.Fatal(err)
	}
	interval, _, err := store.Create(CreateInput{ID: "interval", Title: "Interval", Prompt: "Run interval", Interval: "2h", Now: friday})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteRunState(RunState{TaskID: interval.ID, LastRunAt: friday.Add(-3 * time.Hour), Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	due, err := store.Due(friday)
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	ids := map[string]bool{}
	for _, task := range due {
		ids[task.ID] = true
	}
	if !ids[weekly.ID] || !ids[interval.ID] {
		t.Fatalf("expected weekly and interval due, got %#v", due)
	}
	if err := store.WriteRunState(RunState{TaskID: weekly.ID, LastRunAt: friday, Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	due, err = store.Due(friday.Add(time.Hour))
	if err != nil {
		t.Fatalf("due again: %v", err)
	}
	if len(due) != 1 || due[0].ID != interval.ID {
		t.Fatalf("expected only interval due after weekly ran, got %#v", due)
	}
}

func TestStoreDueMultipleWeekdaysAndMonthly(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	monday := time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC)
	multi, _, err := store.Create(CreateInput{ID: "multi", Title: "Multi", Prompt: "Run multi", WeeklyAt: "09:00", Weekdays: []string{"monday", "wednesday"}, Now: monday})
	if err != nil {
		t.Fatal(err)
	}
	monthly, _, err := store.Create(CreateInput{ID: "monthly", Title: "Monthly", Prompt: "Run monthly", MonthlyAt: "09:00", MonthlyDay: 21, Now: monday})
	if err != nil {
		t.Fatal(err)
	}
	due, err := store.Due(monday)
	if err != nil {
		t.Fatalf("due monday: %v", err)
	}
	if len(due) != 1 || due[0].ID != multi.ID {
		t.Fatalf("expected multi due on monday, got %#v", due)
	}
	if err := store.WriteRunState(RunState{TaskID: multi.ID, LastRunAt: monday, Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	wednesday := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	due, err = store.Due(wednesday)
	if err != nil {
		t.Fatalf("due wednesday: %v", err)
	}
	if len(due) != 1 || due[0].ID != multi.ID {
		t.Fatalf("expected multi due again on wednesday, got %#v", due)
	}
	monthlyDue := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	due, err = store.Due(monthlyDue)
	if err != nil {
		t.Fatalf("due monthly: %v", err)
	}
	foundMonthly := false
	for _, task := range due {
		if task.ID == monthly.ID {
			foundMonthly = true
		}
	}
	if !foundMonthly {
		t.Fatalf("expected monthly due, got %#v", due)
	}
	if err := store.WriteRunState(RunState{TaskID: monthly.ID, LastRunAt: monthlyDue, Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	due, err = store.Due(monthlyDue.Add(time.Hour))
	if err != nil {
		t.Fatalf("due monthly again: %v", err)
	}
	for _, task := range due {
		if task.ID == monthly.ID {
			t.Fatalf("expected monthly not to rerun in same month, got %#v", due)
		}
	}
}

func TestStoreDelete(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	task, _, err := store.Create(CreateInput{ID: "delete-me", Title: "Delete Me", Prompt: "Run", DailyAt: "09:00"})
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.Delete(task.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected deleted file, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "schedules", "tasks")); err != nil {
		t.Fatalf("expected task dir to remain: %v", err)
	}
}
