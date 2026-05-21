package schedule

import (
	"context"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/config"
)

func TestSchedulerRunDueDisabledByConfig(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	if _, _, err := store.Create(CreateInput{ID: "task", Title: "Task", Prompt: "Run task", DailyAt: "09:00", Now: now}); err != nil {
		t.Fatal(err)
	}
	handler := &fakeHandler{}
	scheduler := Scheduler{
		Config: &config.Root{App: config.AppConfig{Home: home}, Scheduler: config.SchedulerConfig{Enabled: false}},
		Runner: Runner{
			Store:  store,
			Handle: handler.Handle,
		},
	}
	scheduler.Start(context.Background())
	time.Sleep(20 * time.Millisecond)
	if handler.calls != 0 {
		t.Fatalf("expected disabled scheduler not to run, calls=%d", handler.calls)
	}
}

func TestSchedulerRunDueRunsRuntime(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	if _, _, err := store.Create(CreateInput{ID: "task", Title: "Task", Prompt: "Run task", DailyAt: "09:00", Now: now}); err != nil {
		t.Fatal(err)
	}
	handler := &fakeHandler{}
	scheduler := Scheduler{
		Config: &config.Root{App: config.AppConfig{Home: home}, Scheduler: config.SchedulerConfig{Enabled: true}},
		Runner: Runner{
			Store:  store,
			Handle: handler.Handle,
		},
	}
	if err := scheduler.RunDue(now); err != nil {
		t.Fatalf("run due: %v", err)
	}
	if handler.calls != 1 {
		t.Fatalf("expected runtime call, got %d", handler.calls)
	}
	state, err := store.ReadState()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if len(state.Tasks) != 1 || state.Tasks[0].Status != "ok" {
		t.Fatalf("unexpected state %#v", state)
	}
}
