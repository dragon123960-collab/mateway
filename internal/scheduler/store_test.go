package scheduler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type stubJobRunner struct {
	result RunResult
	err    error
}

func (s stubJobRunner) RunScheduledJob(context.Context, Job) (RunResult, error) {
	return s.result, s.err
}

func TestStoreSaveAndDueInterval(t *testing.T) {
	store := Store{Workspace: t.TempDir()}
	job, err := NewIntervalJob("daily", "s1", "summarize", 30)
	if err != nil {
		t.Fatal(err)
	}
	job.State.NextRunAt = time.Now().Add(-time.Minute)
	if err := store.Save(job); err != nil {
		t.Fatal(err)
	}
	items, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("unexpected items: %#v", items)
	}
	due, err := store.Due(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("unexpected due jobs: %#v", due)
	}
	if due[0].Schedule.Kind != ScheduleKindInterval {
		t.Fatalf("expected interval schedule, got %#v", due[0].Schedule)
	}
}

func TestStoreMigratesLegacyJobs(t *testing.T) {
	root := t.TempDir()
	store := Store{Workspace: root}
	legacy := []legacyJob{{
		Name:            "legacy",
		SessionKey:      "s1",
		Mode:            "chat",
		Prompt:          "hello",
		IntervalMinutes: 60,
		NextRunAt:       time.Now().Add(time.Hour),
	}}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(store.path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.path(), data, 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Schedule.Kind != ScheduleKindInterval {
		t.Fatalf("unexpected migrated jobs: %#v", items)
	}
}

func TestCronNextAfter(t *testing.T) {
	job, err := NewCronJob("report", "s1", "daily", "0 10 * * *", "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 4, 24, 9, 30, 0, 0, time.FixedZone("CST", 8*3600))
	next, err := job.Schedule.NextRunAfter(base)
	if err != nil {
		t.Fatal(err)
	}
	if next.Hour() != 10 || next.Minute() != 0 {
		t.Fatalf("unexpected next run: %s", next.Format(time.RFC3339))
	}
}

func TestStoreUpsertIsIdempotentAndPreservesIdentity(t *testing.T) {
	store := Store{Workspace: t.TempDir()}
	job, err := NewCronJob("report", "schedule:report", "first", "0 3 * * *", "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	created, action, err := store.Upsert(job)
	if err != nil {
		t.Fatal(err)
	}
	if action != "create" {
		t.Fatalf("expected create action, got %s", action)
	}

	same := created
	same.Prompt = "first"
	noopJob, action, err := store.Upsert(same)
	if err != nil {
		t.Fatal(err)
	}
	if action != "noop" {
		t.Fatalf("expected noop action, got %s", action)
	}
	if noopJob.ID != created.ID {
		t.Fatalf("expected id to be preserved, got %s vs %s", noopJob.ID, created.ID)
	}

	updatedInput := created
	updatedInput.Prompt = "second"
	updatedJob, action, err := store.Upsert(updatedInput)
	if err != nil {
		t.Fatal(err)
	}
	if action != "update" {
		t.Fatalf("expected update action, got %s", action)
	}
	if updatedJob.ID != created.ID {
		t.Fatalf("expected id to stay stable, got %s vs %s", updatedJob.ID, created.ID)
	}
}

func TestResolveTargetCurrentSessionAndExplicitAgent(t *testing.T) {
	sessionKey, agentName, target, err := ResolveTarget("nightly", "feishu:p2p:u1", "writer", Target{
		SessionMode: TargetSessionCurrent,
		AgentMode:   TargetAgentExplicit,
		AgentName:   "planner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sessionKey != "feishu:p2p:u1" {
		t.Fatalf("unexpected session key: %s", sessionKey)
	}
	if agentName != "planner" {
		t.Fatalf("unexpected agent name: %s", agentName)
	}
	if target.SessionMode != TargetSessionCurrent || target.AgentMode != TargetAgentExplicit {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestServiceRunNowUpdatesStateAndHistory(t *testing.T) {
	root := t.TempDir()
	store := Store{Workspace: root}

	job, err := NewIntervalJob("manual", "schedule:manual", "hello", 15)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(job); err != nil {
		t.Fatal(err)
	}

	svc := Service{Store: store, Runner: stubJobRunner{result: RunResult{RunID: "run_1", Status: "completed"}}}
	updated, err := svc.RunNow(context.Background(), "manual")
	if err != nil {
		t.Fatal(err)
	}
	if updated.State.LastRunStatus != "completed" && updated.State.LastRunStatus != "ok" {
		t.Fatalf("unexpected last run status: %#v", updated.State)
	}
	lines, err := store.ReadRuns("manual", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) == 0 {
		t.Fatal("expected schedule run history")
	}
	if !strings.Contains(lines[0], `"job_name":"manual"`) {
		t.Fatalf("unexpected run history: %s", lines[0])
	}
}
