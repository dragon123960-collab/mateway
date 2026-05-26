package heartbeat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/model"
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

func TestRunnerRunMemoryDailyDistillWritesInboxProposal(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	now := time.Date(2026, 5, 21, 10, 30, 0, 0, time.UTC)
	sessionDir := filepath.Join(home, "run", "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st := session.State{
		SessionKey: "cli:distill",
		Tasks: map[string]session.TaskState{
			"task-1": {
				ID:            "task-1",
				TraceID:       "trace-1",
				Status:        session.TaskCompleted,
				ResolvedQuery: "Summarize project memory direction",
				PlanSummary:   "Review memory direction",
				ToolNames:     []string{"file.summary"},
				Artifacts:     []session.Artifact{{Kind: "file", Path: "/tmp/memory.md"}},
				UpdatedAt:     now,
			},
		},
		TaskOrder: []string{"task-1"},
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
	runner.Generate = func(ctx context.Context, system string, messages []model.Message) (string, error) {
		return "- Durable conclusion: Review memory direction is a recurring project-level concern.", nil
	}
	result, err := runner.Run(RunOptions{AgentID: "main", Job: JobMemoryDailyDistill, Now: now})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.DistillPath == "" {
		t.Fatalf("expected distillation proposal path, got %#v", result)
	}
	data, err = os.ReadFile(result.DistillPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"type: decision",
		"tags: [daily-distillation, auto-proposal, distill-decision]",
		"confidence: medium",
		"Daily decision distillation 2026-05-21",
		"## Review Boundary",
		"Confirm this conclusion is stable, source-backed, and useful enough to keep as long memory.",
		"## Recommended Promotion Target",
		"Promote into a decision-style long memory page",
		"## Why It May Be Worth Keeping",
		"1 completed task(s) suggest a durable decision, rule, or direction",
		"## Evidence Signals",
		"Completed candidate tasks: 1",
		"Artifact evidence count: 1",
		"Tool signals: file.summary(1)",
		"Example task/trace refs: trace-1",
		"Decision/rule signals: 1 task(s) referenced direction, rule, or decision language.",
		"## Filtering Notes",
		"Only completed tasks with durable-signal gating are considered.",
		"Decision candidates must show explicit decision/rule/direction cues plus grounded evidence or non-read-only execution context.",
		"## Candidate Conclusions",
		"Durable conclusion: Review memory direction is a recurring project-level concern.",
		"## Candidate Decision Signals",
		"Decision candidate: Review memory direction",
		"decision cue: review whether this expresses a stable rule, direction, or operating constraint",
		"Review memory direction",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected distillation proposal to contain %q, got:\n%s", want, text)
		}
	}
}

func TestDedupeDistillationCandidatesKeepsDistinctSignals(t *testing.T) {
	tasks := []session.TaskState{
		{ID: "task-1", TraceID: "trace-1", PlanSummary: "Review memory direction", ToolNames: []string{"file.summary"}},
		{ID: "task-2", TraceID: "trace-2", PlanSummary: "Review memory direction", ToolNames: []string{"file.summary"}},
		{ID: "task-3", TraceID: "trace-3", PlanSummary: "Improve workflow for daily report", ToolNames: []string{"file.patch"}},
	}
	got := dedupeDistillationCandidates(tasks)
	if len(got) != 2 {
		t.Fatalf("expected 2 deduped candidates, got %d: %#v", len(got), got)
	}
	if got[0].ID != "task-1" || got[1].ID != "task-3" {
		t.Fatalf("unexpected deduped tasks %#v", got)
	}
}

func TestRunnerRunMemoryDailyDistillDedupesSimilarSameDayTasks(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	now := time.Date(2026, 5, 21, 10, 30, 0, 0, time.UTC)
	sessionDir := filepath.Join(home, "run", "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st := session.State{
		SessionKey: "cli:distill-dedupe",
		Tasks: map[string]session.TaskState{
			"task-1": {
				ID:            "task-1",
				TraceID:       "trace-1",
				Status:        session.TaskCompleted,
				ResolvedQuery: "Review memory direction",
				PlanSummary:   "Review memory direction",
				ToolNames:     []string{"file.summary"},
				Artifacts:     []session.Artifact{{Kind: "file", Path: "/tmp/memory-1.md"}},
				UpdatedAt:     now,
			},
			"task-2": {
				ID:            "task-2",
				TraceID:       "trace-2",
				Status:        session.TaskCompleted,
				ResolvedQuery: "Review memory direction",
				PlanSummary:   "Review memory direction",
				ToolNames:     []string{"file.summary"},
				Artifacts:     []session.Artifact{{Kind: "file", Path: "/tmp/memory-2.md"}},
				UpdatedAt:     now.Add(5 * time.Minute),
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
	runner.Generate = func(ctx context.Context, system string, messages []model.Message) (string, error) {
		return "- Durable conclusion: Review memory direction is a recurring project-level concern.", nil
	}
	result, err := runner.Run(RunOptions{AgentID: "main", Job: JobMemoryDailyDistill, Now: now})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err = os.ReadFile(result.DistillPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "task: task-2") {
		t.Fatalf("expected deduped distillation to skip duplicate task-2, got:\n%s", text)
	}
	if !strings.Contains(text, "Completed candidate tasks: 1") {
		t.Fatalf("expected evidence summary to reflect deduped candidate count, got:\n%s", text)
	}
}

func TestClassifyDistillationType(t *testing.T) {
	cases := []struct {
		task session.TaskState
		want string
	}{
		{task: session.TaskState{PlanSummary: "Review memory direction"}, want: "decision"},
		{task: session.TaskState{PlanSummary: "Improve workflow for daily report"}, want: "playbook"},
		{task: session.TaskState{PlanSummary: "User preference: reply in Chinese"}, want: "preference"},
		{task: session.TaskState{PlanSummary: "Project architecture summary"}, want: "project"},
	}
	for _, tc := range cases {
		if got := classifyDistillationType([]session.TaskState{tc.task}); got != tc.want {
			t.Fatalf("classifyDistillationType(%q) = %q, want %q", tc.task.PlanSummary, got, tc.want)
		}
	}
}

func TestDistillationConfidenceForType(t *testing.T) {
	cases := []struct {
		typ  string
		want string
	}{
		{typ: "decision", want: "medium"},
		{typ: "playbook", want: "medium"},
		{typ: "preference", want: "low"},
		{typ: "project", want: "low"},
	}
	for _, tc := range cases {
		if got := distillationConfidenceForType(tc.typ); got != tc.want {
			t.Fatalf("distillationConfidenceForType(%q) = %q, want %q", tc.typ, got, tc.want)
		}
	}
}

func TestDistillationProposalMetadata(t *testing.T) {
	now := time.Date(2026, 5, 21, 10, 30, 0, 0, time.UTC)
	title, tags := distillationProposalMetadata("playbook", now)
	if title != "Daily playbook distillation 2026-05-21" {
		t.Fatalf("unexpected title %q", title)
	}
	if len(tags) != 3 || tags[2] != "distill-playbook" {
		t.Fatalf("unexpected tags %#v", tags)
	}
}

func TestDistillationPromotionTarget(t *testing.T) {
	cases := []struct {
		typ  string
		want string
	}{
		{typ: "decision", want: "decision-style"},
		{typ: "playbook", want: "workflow/playbook-style"},
		{typ: "preference", want: "preference-style"},
		{typ: "project", want: "project fact/note-style"},
	}
	for _, tc := range cases {
		if got := distillationPromotionTarget(tc.typ); !strings.Contains(got, tc.want) {
			t.Fatalf("distillationPromotionTarget(%q) = %q, want to contain %q", tc.typ, got, tc.want)
		}
	}
}

func TestDistillationCandidateItemsHeading(t *testing.T) {
	cases := []struct {
		typ  string
		want string
	}{
		{typ: "decision", want: "## Candidate Decision Signals"},
		{typ: "playbook", want: "## Candidate Workflow Signals"},
		{typ: "preference", want: "## Candidate Preference Signals"},
		{typ: "project", want: "## Candidate Project Signals"},
	}
	for _, tc := range cases {
		if got := distillationCandidateItemsHeading(tc.typ); got != tc.want {
			t.Fatalf("distillationCandidateItemsHeading(%q) = %q, want %q", tc.typ, got, tc.want)
		}
	}
}

func TestBuildDailyDistillationPromptUsesTypeSpecificInstruction(t *testing.T) {
	tasks := []session.TaskState{{
		ID:            "task-1",
		TraceID:       "trace-1",
		PlanSummary:   "Improve workflow for daily report",
		ResolvedQuery: "Improve workflow for daily report",
		ToolNames:     []string{"file.patch"},
	}}
	now := time.Date(2026, 5, 21, 10, 30, 0, 0, time.UTC)
	prompt := buildDailyDistillationPrompt(now, "playbook", tasks)
	for _, want := range []string{
		"Distillation type: playbook",
		"Prefer repeatable workflow knowledge, step ordering, and reusable operating patterns.",
		"Avoid one-off fixes, transient debugging chatter, or purely descriptive summaries.",
		"Improve workflow for daily report",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected playbook prompt to contain %q, got:\n%s", want, prompt)
		}
	}
}

func TestDailyDistillationSystemPromptUsesTypeSpecificGuidance(t *testing.T) {
	cases := []struct {
		typ  string
		want string
	}{
		{typ: "decision", want: "Prefer durable conclusions such as stable decisions, working rules, and project direction."},
		{typ: "playbook", want: "Prefer reusable workflow knowledge and step patterns that could help on future similar tasks."},
		{typ: "preference", want: "Prefer only clearly signaled stable preferences; be conservative and avoid overfitting to one turn."},
		{typ: "project", want: "Prefer durable project facts, boundaries, or architecture context that remain useful after the daily review."},
	}
	for _, tc := range cases {
		prompt := dailyDistillationSystemPrompt(tc.typ)
		if !strings.Contains(prompt, tc.want) {
			t.Fatalf("expected system prompt for %q to contain %q, got:\n%s", tc.typ, tc.want, prompt)
		}
	}
}

func TestTaskWorthDistilling(t *testing.T) {
	cases := []struct {
		name string
		task session.TaskState
		want bool
	}{
		{
			name: "durable task with grounded artifact",
			task: session.TaskState{
				PlanSummary: "Review memory direction",
				ToolNames:   []string{"file.summary"},
				Artifacts:   []session.Artifact{{Kind: "file", Path: "/tmp/memory.md"}},
			},
			want: true,
		},
		{
			name: "weak decision summary without stable context",
			task: session.TaskState{
				PlanSummary: "Review memory direction",
				ToolNames:   []string{"file.summary"},
			},
			want: false,
		},
		{
			name: "read only readme summary",
			task: session.TaskState{
				PlanSummary: "Read README and summarize setup",
				ToolNames:   []string{"file.summary"},
			},
			want: false,
		},
		{
			name: "test like task",
			task: session.TaskState{
				PlanSummary: "Run tests for memory package",
				ToolNames:   []string{"terminal.run"},
			},
			want: false,
		},
		{
			name: "debug task",
			task: session.TaskState{
				ResolvedQuery: "诊断 git 认证",
				ToolNames:     []string{"terminal.run"},
			},
			want: false,
		},
		{
			name: "workflow task with mutation signal",
			task: session.TaskState{
				PlanSummary: "Improve workflow for daily report",
				ToolNames:   []string{"file.patch"},
			},
			want: true,
		},
		{
			name: "weak playbook summary without stable context",
			task: session.TaskState{
				PlanSummary: "Workflow summary for daily report",
				ToolNames:   []string{"file.summary"},
			},
			want: false,
		},
		{
			name: "stable playbook with grounded artifact",
			task: session.TaskState{
				PlanSummary: "Repeatable workflow for daily report",
				ToolNames:   []string{"file.summary"},
				Artifacts:   []session.Artifact{{Kind: "file", Path: "/tmp/playbook.md"}},
			},
			want: true,
		},
		{
			name: "playbook cue with mutation context tool",
			task: session.TaskState{
				PlanSummary: "Playbook: step by step release workflow",
				ToolNames:   []string{"file.patch"},
			},
			want: true,
		},
		{
			name: "stable decision with grounded artifact",
			task: session.TaskState{
				PlanSummary: "Working rule for memory direction",
				ToolNames:   []string{"file.summary"},
				Artifacts:   []session.Artifact{{Kind: "file", Path: "/tmp/rule.md"}},
			},
			want: true,
		},
		{
			name: "decision with mutation context tool",
			task: session.TaskState{
				PlanSummary: "Decision: keep memory commits review-only",
				ToolNames:   []string{"file.patch"},
			},
			want: true,
		},
		{
			name: "weak preference without stable context",
			task: session.TaskState{
				PlanSummary: "User preference: reply in Chinese",
				ToolNames:   []string{"file.summary"},
			},
			want: false,
		},
		{
			name: "stable preference with grounded artifact",
			task: session.TaskState{
				PlanSummary: "User preference: reply in Chinese",
				ToolNames:   []string{"file.write"},
				Artifacts:   []session.Artifact{{Kind: "file", Path: "/tmp/reply.md"}},
			},
			want: true,
		},
		{
			name: "preference with write context tool",
			task: session.TaskState{
				PlanSummary: "Working preference: keep the reply concise",
				ToolNames:   []string{"file.patch"},
			},
			want: true,
		},
		{
			name: "weak project summary without stable context",
			task: session.TaskState{
				PlanSummary: "Project architecture summary",
				ToolNames:   []string{"file.summary"},
			},
			want: false,
		},
		{
			name: "stable project cue with grounded artifact",
			task: session.TaskState{
				PlanSummary: "Project architecture boundary",
				ToolNames:   []string{"file.summary"},
				Artifacts:   []session.Artifact{{Kind: "file", Path: "/tmp/architecture.md"}},
			},
			want: true,
		},
		{
			name: "project cue with mutation context tool",
			task: session.TaskState{
				PlanSummary: "Project scope boundary for memory module",
				ToolNames:   []string{"file.patch"},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		if got := taskWorthDistilling(tc.task); got != tc.want {
			t.Fatalf("%s: taskWorthDistilling() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPlaybookTaskLooksStable(t *testing.T) {
	cases := []struct {
		name string
		task session.TaskState
		want bool
	}{
		{
			name: "playbook cue but read only",
			task: session.TaskState{
				PlanSummary: "Workflow summary for daily report",
				ToolNames:   []string{"file.summary"},
			},
			want: false,
		},
		{
			name: "playbook cue with grounded artifact",
			task: session.TaskState{
				PlanSummary: "Repeatable workflow for daily report",
				ToolNames:   []string{"file.summary"},
				Artifacts:   []session.Artifact{{Kind: "file", Path: "/tmp/playbook.md"}},
			},
			want: true,
		},
		{
			name: "playbook cue with mutation context",
			task: session.TaskState{
				PlanSummary: "Playbook: step by step release workflow",
				ToolNames:   []string{"file.patch"},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		if got := playbookTaskLooksStable(tc.task); got != tc.want {
			t.Fatalf("%s: playbookTaskLooksStable() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDecisionTaskLooksStable(t *testing.T) {
	cases := []struct {
		name string
		task session.TaskState
		want bool
	}{
		{
			name: "decision cue but read only",
			task: session.TaskState{
				PlanSummary: "Review memory direction",
				ToolNames:   []string{"file.summary"},
			},
			want: false,
		},
		{
			name: "decision cue with grounded artifact",
			task: session.TaskState{
				PlanSummary: "Working rule for memory direction",
				ToolNames:   []string{"file.summary"},
				Artifacts:   []session.Artifact{{Kind: "file", Path: "/tmp/rule.md"}},
			},
			want: true,
		},
		{
			name: "decision cue with mutation context",
			task: session.TaskState{
				PlanSummary: "Decision: keep memory commits review-only",
				ToolNames:   []string{"file.patch"},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		if got := decisionTaskLooksStable(tc.task); got != tc.want {
			t.Fatalf("%s: decisionTaskLooksStable() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPreferenceTaskLooksStable(t *testing.T) {
	cases := []struct {
		name string
		task session.TaskState
		want bool
	}{
		{
			name: "explicit preference but read only",
			task: session.TaskState{
				PlanSummary: "User preference: reply in Chinese",
				ToolNames:   []string{"file.summary"},
			},
			want: false,
		},
		{
			name: "explicit preference with grounded artifact",
			task: session.TaskState{
				PlanSummary: "User preference: reply in Chinese",
				ToolNames:   []string{"file.summary"},
				Artifacts:   []session.Artifact{{Kind: "file", Path: "/tmp/reply.md"}},
			},
			want: true,
		},
		{
			name: "explicit preference with mutation context",
			task: session.TaskState{
				PlanSummary: "Working preference: keep the reply concise",
				ToolNames:   []string{"file.patch"},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		if got := preferenceTaskLooksStable(tc.task); got != tc.want {
			t.Fatalf("%s: preferenceTaskLooksStable() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestProjectTaskLooksStable(t *testing.T) {
	cases := []struct {
		name string
		task session.TaskState
		want bool
	}{
		{
			name: "project cue but read only",
			task: session.TaskState{
				PlanSummary: "Project architecture summary",
				ToolNames:   []string{"file.summary"},
			},
			want: false,
		},
		{
			name: "project cue with grounded artifact",
			task: session.TaskState{
				PlanSummary: "Project architecture boundary",
				ToolNames:   []string{"project.index"},
				Artifacts:   []session.Artifact{{Kind: "file", Path: "/tmp/architecture.md"}},
			},
			want: true,
		},
		{
			name: "project cue with mutation context",
			task: session.TaskState{
				PlanSummary: "Project scope boundary for memory module",
				ToolNames:   []string{"file.patch"},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		if got := projectTaskLooksStable(tc.task); got != tc.want {
			t.Fatalf("%s: projectTaskLooksStable() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRunnerRunMemoryDailyDistillSkipsLowValueCompletedTasks(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	now := time.Date(2026, 5, 21, 10, 30, 0, 0, time.UTC)
	sessionDir := filepath.Join(home, "run", "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st := session.State{
		SessionKey: "cli:distill-low-value",
		Tasks: map[string]session.TaskState{
			"task-1": {
				ID:            "task-1",
				TraceID:       "trace-1",
				Status:        session.TaskCompleted,
				ResolvedQuery: "Read README and summarize setup",
				PlanSummary:   "Read README and summarize setup",
				ToolNames:     []string{"file.summary"},
				UpdatedAt:     now,
			},
			"task-2": {
				ID:            "task-2",
				TraceID:       "trace-2",
				Status:        session.TaskCompleted,
				ResolvedQuery: "Run tests for memory package",
				PlanSummary:   "Run tests for memory package",
				ToolNames:     []string{"terminal.run"},
				UpdatedAt:     now.Add(5 * time.Minute),
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
	runner.Generate = func(ctx context.Context, system string, messages []model.Message) (string, error) {
		return "- Should not be called.", nil
	}
	result, err := runner.Run(RunOptions{AgentID: "main", Job: JobMemoryDailyDistill, Now: now})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.DistillPath != "" {
		t.Fatalf("expected no distillation proposal for low-value completed tasks, got %q", result.DistillPath)
	}
}

func TestRunnerRunMemoryDailyDistillSkipsExistingLongMemoryOverlap(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	now := time.Date(2026, 5, 21, 10, 30, 0, 0, time.UTC)
	sessionDir := filepath.Join(home, "run", "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	longDir := filepath.Join(workspace, "memory", "agents", "main", "long")
	if err := os.MkdirAll(longDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `---
type: decision
scope: agent
status: active
sources:
  - manual
confidence: medium
---

# Review memory direction

Project memory direction should be reviewed carefully.
`
	if err := os.WriteFile(filepath.Join(longDir, "memory-direction.md"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	st := session.State{
		SessionKey: "cli:distill-overlap",
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
		},
		TaskOrder: []string{"task-1"},
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
	runner.Generate = func(ctx context.Context, system string, messages []model.Message) (string, error) {
		return "- Durable conclusion: Review memory direction is a recurring concern.", nil
	}
	result, err := runner.Run(RunOptions{AgentID: "main", Job: JobMemoryDailyDistill, Now: now})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.DistillPath != "" {
		t.Fatalf("expected no new distillation proposal due to long memory overlap, got %q", result.DistillPath)
	}
}

func TestRunnerRunMemoryDailyDistillSkipsExistingInboxProposalOverlap(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	now := time.Date(2026, 5, 21, 10, 30, 0, 0, time.UTC)
	sessionDir := filepath.Join(home, "run", "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store := memory.NewStore(workspace)
	if _, err := store.Propose(memory.ProposalInput{
		AgentID:    "main",
		Scope:      "agent",
		Type:       "decision",
		Title:      "Review memory direction",
		Body:       "Existing proposed distillation should block duplicate daily proposal.",
		Sources:    []string{"manual"},
		Confidence: "medium",
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("propose: %v", err)
	}
	st := session.State{
		SessionKey: "cli:distill-inbox-overlap",
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
		},
		TaskOrder: []string{"task-1"},
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
	runner.Generate = func(ctx context.Context, system string, messages []model.Message) (string, error) {
		return "- Durable conclusion: Review memory direction is a recurring concern.", nil
	}
	result, err := runner.Run(RunOptions{AgentID: "main", Job: JobMemoryDailyDistill, Now: now})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.DistillPath != "" {
		t.Fatalf("expected no new distillation proposal due to inbox overlap, got %q", result.DistillPath)
	}
}

func TestRunnerRunMemoryDailyDistillFiltersRepeatedCandidateConclusions(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	now := time.Date(2026, 5, 21, 10, 30, 0, 0, time.UTC)
	sessionDir := filepath.Join(home, "run", "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	longDir := filepath.Join(workspace, "memory", "agents", "main", "long")
	if err := os.MkdirAll(longDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `---
type: decision
scope: agent
status: active
sources:
  - manual
confidence: medium
---

# Existing Decision Memory

Review memory direction is a recurring project-level concern.
`
	if err := os.WriteFile(filepath.Join(longDir, "decision-memory-direction.md"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	st := session.State{
		SessionKey: "cli:distill-conclusion-filter",
		Tasks: map[string]session.TaskState{
			"task-1": {
				ID:            "task-1",
				TraceID:       "trace-1",
				Status:        session.TaskCompleted,
				ResolvedQuery: "Decision: keep memory commits review-only",
				PlanSummary:   "Decision: keep memory commits review-only",
				ToolNames:     []string{"file.patch"},
				Artifacts:     []session.Artifact{{Kind: "file", Path: "/tmp/memory-rule.md"}},
				UpdatedAt:     now,
			},
		},
		TaskOrder: []string{"task-1"},
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
	runner.Generate = func(ctx context.Context, system string, messages []model.Message) (string, error) {
		return strings.Join([]string{
			"- Review memory direction is a recurring project-level concern.",
			"- Working rule: keep memory commits review-only until proposals are approved.",
		}, "\n"), nil
	}
	result, err := runner.Run(RunOptions{AgentID: "main", Job: JobMemoryDailyDistill, Now: now})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err = os.ReadFile(result.DistillPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "- Review memory direction is a recurring project-level concern.") {
		t.Fatalf("expected repeated conclusion to be filtered, got:\n%s", text)
	}
	if !strings.Contains(text, "- Working rule: keep memory commits review-only until proposals are approved.") {
		t.Fatalf("expected new conclusion to remain, got:\n%s", text)
	}
}

func TestRunnerRunMemoryDailyDistillIgnoresDifferentLongMemoryType(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	now := time.Date(2026, 5, 21, 10, 30, 0, 0, time.UTC)
	sessionDir := filepath.Join(home, "run", "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	longDir := filepath.Join(workspace, "memory", "agents", "main", "long")
	if err := os.MkdirAll(longDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `---
type: project
scope: agent
status: active
sources:
  - manual
confidence: low
---

# Review memory direction

Project overview text with a similar title should not block decision distillation.
`
	if err := os.WriteFile(filepath.Join(longDir, "project-memory-direction.md"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	st := session.State{
		SessionKey: "cli:distill-type-overlap",
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
		},
		TaskOrder: []string{"task-1"},
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
	runner.Generate = func(ctx context.Context, system string, messages []model.Message) (string, error) {
		return "- Durable conclusion: Review memory direction is a recurring concern.", nil
	}
	result, err := runner.Run(RunOptions{AgentID: "main", Job: JobMemoryDailyDistill, Now: now})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.DistillPath == "" {
		t.Fatalf("expected decision distillation to survive different-type long memory overlap")
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
