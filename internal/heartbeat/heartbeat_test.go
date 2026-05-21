package heartbeat

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/session"
)

func TestRunnerRunMemoryLintWritesState(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "memory", "agents", "main", "long"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Root{
		App:       config.AppConfig{Home: home, Workspace: workspace},
		Scheduler: config.SchedulerConfig{},
		Agents:    config.AgentsConfig{Default: "main"},
	}
	runner := NewRunner(cfg)
	result, err := runner.Run(RunOptions{AgentID: "main", Job: JobMemoryLint})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State.Status != "ok" || result.Report == nil {
		t.Fatalf("unexpected result %#v", result)
	}
	state, path, err := runner.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.HasSuffix(path, filepath.Join("run", "scheduler", "state.json")) {
		t.Fatalf("unexpected state path %s", path)
	}
	if len(state.Jobs) != 1 || state.Jobs[0].Job != JobMemoryLint {
		t.Fatalf("expected memory_lint state, got %#v", state)
	}
}

func TestRunnerRecordsUnsupportedJobFailure(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{
		App:       config.AppConfig{Home: home, Workspace: filepath.Join(home, "workspace")},
		Scheduler: config.SchedulerConfig{StateDir: filepath.Join(home, "state")},
		Agents:    config.AgentsConfig{Default: "main"},
	}
	runner := NewRunner(cfg)
	_, err := runner.Run(RunOptions{Job: "missing_job"})
	if err == nil {
		t.Fatal("expected unsupported job error")
	}
	state, _, statusErr := runner.Status()
	if statusErr != nil {
		t.Fatalf("status: %v", statusErr)
	}
	if len(state.Jobs) != 1 || state.Jobs[0].Status != "failed" || !strings.Contains(state.Jobs[0].LastError, "unsupported") {
		t.Fatalf("expected failed job state, got %#v", state)
	}
}

func TestRunnerRunMemoryIndexRebuildWritesIndex(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	longDir := filepath.Join(workspace, "memory", "agents", "main", "long")
	if err := os.MkdirAll(longDir, 0o755); err != nil {
		t.Fatal(err)
	}
	text := `---
type: note
scope: agent
status: active
sources:
  - manual
confidence: medium
---

# Indexed

Memory index heartbeat rebuilds deterministic JSON.
`
	if err := os.WriteFile(filepath.Join(longDir, "indexed.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Root{
		App:       config.AppConfig{Home: home, Workspace: workspace},
		Scheduler: config.SchedulerConfig{},
		Agents:    config.AgentsConfig{Default: "main"},
	}
	runner := NewRunner(cfg)
	result, err := runner.Run(RunOptions{AgentID: "main", Job: JobMemoryIndexRebuild})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.State.Status != "ok" || result.Index == nil || len(result.Index.Index.Entries) != 1 {
		t.Fatalf("unexpected result %#v", result)
	}
	if _, err := os.Stat(filepath.Join(workspace, "memory", "index.json")); err != nil {
		t.Fatalf("expected index file: %v", err)
	}
}

func TestRunnerRunMemoryDailyReviewWritesRecentFile(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	now := time.Date(2026, 5, 21, 10, 30, 0, 0, time.UTC)
	sessionDir := filepath.Join(home, "run", "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st := session.State{
		SessionKey: "cli:daily",
		Tasks: map[string]session.TaskState{
			"task-1": {
				ID:            "task-1",
				TraceID:       "trace-1",
				Status:        session.TaskCompleted,
				ResolvedQuery: "Review memory direction",
				PlanSummary:   "Review memory direction",
				ToolNames:     []string{"file.summary"},
				Artifacts:     []session.Artifact{{Kind: "file", Path: "/tmp/memory.md"}},
				UpdatedAt:     now,
			},
			"task-2": {
				ID:        "task-2",
				Status:    session.TaskOpen,
				Topic:     "Open planning task",
				UpdatedAt: now,
			},
		},
		TaskOrder: []string{"task-1", "task-2"},
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	sessionFile := base64.RawURLEncoding.EncodeToString([]byte(st.SessionKey)) + ".json"
	if err := os.WriteFile(filepath.Join(sessionDir, sessionFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Root{
		App:       config.AppConfig{Home: home, Workspace: workspace},
		Scheduler: config.SchedulerConfig{},
		Agents:    config.AgentsConfig{Default: "main"},
	}
	runner := NewRunner(cfg)
	result, err := runner.Run(RunOptions{AgentID: "main", Job: JobMemoryDailyReview, Now: now})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.DailyReview == nil || result.DailyReview.TaskCount != 2 || result.DailyReview.OpenTasks != 1 || result.DailyReview.Artifacts != 1 {
		t.Fatalf("unexpected daily review result %#v", result)
	}
	reviewData, err := os.ReadFile(result.DailyReview.Path)
	if err != nil {
		t.Fatal(err)
	}
	reviewText := string(reviewData)
	if !strings.Contains(reviewText, "Review memory direction") || !strings.Contains(reviewText, "Open planning task") {
		t.Fatalf("unexpected review text:\n%s", reviewText)
	}
	if !strings.Contains(reviewText, "without model judgment") {
		t.Fatalf("expected non-model boundary note, got:\n%s", reviewText)
	}
	logData, err := os.ReadFile(filepath.Join(workspace, "memory", "log.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "memory_daily_review") {
		t.Fatalf("expected memory log entry, got %s", string(logData))
	}
}

func TestRunnerRunMemoryRecentCompactArchivesOldRecentFiles(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	recentDir := filepath.Join(workspace, "memory", "agents", "main", "recent")
	if err := os.MkdirAll(recentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"2026-05-17.md": "old",
		"2026-05-20.md": "kept",
		"notes.md":      "manual",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(recentDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Root{
		App:       config.AppConfig{Home: home, Workspace: workspace},
		Memory:    config.MemoryConfig{RecentDays: 3},
		Scheduler: config.SchedulerConfig{},
		Agents:    config.AgentsConfig{Default: "main"},
	}
	runner := NewRunner(cfg)
	result, err := runner.Run(RunOptions{AgentID: "main", Job: JobMemoryRecentCompact, Now: time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Compact == nil || result.Compact.Archived != 1 || result.Compact.Kept != 2 {
		t.Fatalf("unexpected compact result %#v", result)
	}
	if _, err := os.Stat(filepath.Join(recentDir, "archive", "2026-05-17.md")); err != nil {
		t.Fatalf("expected archived file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(recentDir, "2026-05-20.md")); err != nil {
		t.Fatalf("expected kept dated file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(recentDir, "notes.md")); err != nil {
		t.Fatalf("expected manual file kept: %v", err)
	}
}
