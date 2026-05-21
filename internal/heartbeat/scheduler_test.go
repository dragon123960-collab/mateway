package heartbeat

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/config"
)

func TestDueJobsDisabledSchedulerReturnsNone(t *testing.T) {
	cfg := testSchedulerConfig(false)
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	if got := DueJobs(cfg, State{}, now); len(got) != 0 {
		t.Fatalf("expected no jobs, got %#v", got)
	}
}

func TestDueJobsReturnsAllowedJobsAfterDailyTime(t *testing.T) {
	cfg := testSchedulerConfig(true)
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	got := DueJobs(cfg, State{}, now)
	want := []ScheduledJob{
		{AgentID: "main", Job: JobMemoryDailyReview},
		{AgentID: "main", Job: JobMemoryRecentCompact},
		{AgentID: "main", Job: JobMemoryLint},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected due jobs\nwant: %#v\n got: %#v", want, got)
	}
}

func TestDueJobsSkipsAlreadyRunToday(t *testing.T) {
	cfg := testSchedulerConfig(true)
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	state := State{Jobs: []JobState{{
		AgentID:   "main",
		Job:       JobMemoryDailyReview,
		LastRunAt: time.Date(2026, 5, 21, 4, 0, 0, 0, time.UTC),
		Status:    "ok",
	}}}
	got := DueJobs(cfg, state, now)
	want := []ScheduledJob{
		{AgentID: "main", Job: JobMemoryRecentCompact},
		{AgentID: "main", Job: JobMemoryLint},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected due jobs\nwant: %#v\n got: %#v", want, got)
	}
}

func TestDueJobsSkipsQuietHours(t *testing.T) {
	cfg := testSchedulerConfig(true)
	now := time.Date(2026, 5, 21, 7, 30, 0, 0, time.UTC)
	if got := DueJobs(cfg, State{}, now); len(got) != 0 {
		t.Fatalf("expected quiet hours to skip jobs, got %#v", got)
	}
}

func TestDueJobsSkipsBeforeDailyTime(t *testing.T) {
	cfg := testSchedulerConfig(true)
	now := time.Date(2026, 5, 21, 2, 30, 0, 0, time.UTC)
	if got := DueJobs(cfg, State{}, now); len(got) != 0 {
		t.Fatalf("expected no jobs before daily time, got %#v", got)
	}
}

func TestSchedulerRunDueWritesState(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "memory", "agents", "main", "long"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := testSchedulerConfig(true)
	cfg.App = config.AppConfig{Home: home, Workspace: workspace}
	cfg.Scheduler.StateDir = filepath.Join(home, "state")
	cfg.Agents.Profiles[0].Heartbeat.Jobs = []string{JobMemoryLint}
	scheduler := Scheduler{Runner: NewRunner(cfg)}
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	if err := scheduler.RunDue(now); err != nil {
		t.Fatalf("run due: %v", err)
	}
	state, _, err := scheduler.Runner.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(state.Jobs) != 1 || state.Jobs[0].Job != JobMemoryLint || state.Jobs[0].Status != "ok" {
		t.Fatalf("unexpected state %#v", state)
	}
}

func testSchedulerConfig(enabled bool) *config.Root {
	return &config.Root{
		Scheduler: config.SchedulerConfig{Enabled: enabled, Timezone: "UTC"},
		Agents: config.AgentsConfig{
			Default: "main",
			Profiles: []config.AgentProfileConfig{{
				ID: "main",
				Heartbeat: config.HeartbeatConfig{
					Enabled: true,
					Schedule: config.HeartbeatSchedule{
						DailyAt: "03:30",
					},
					Jobs: []string{
						JobMemoryDailyReview,
						JobMemoryRecentCompact,
						JobMemoryLint,
						"unsupported_job",
					},
					QuietHours: config.HeartbeatQuietHours{
						Start: "23:00",
						End:   "08:00",
					},
				},
			}},
		},
	}
}
