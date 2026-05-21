package heartbeat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/session"
)

const (
	JobMemoryLint          = "memory_lint"
	JobMemoryDailyReview   = "memory_daily_review"
	JobMemoryRecentCompact = "memory_recent_compact"
	JobMemoryIndexRebuild  = "memory_index_rebuild"
)

type State struct {
	Jobs []JobState `json:"jobs"`
}

type JobState struct {
	AgentID   string    `json:"agent_id"`
	Job       string    `json:"job"`
	LastRunAt time.Time `json:"last_run_at"`
	Status    string    `json:"status"`
	LastError string    `json:"last_error,omitempty"`
	Summary   string    `json:"summary,omitempty"`
}

type RunOptions struct {
	AgentID string
	Job     string
	Now     time.Time
}

type RunResult struct {
	State       JobState
	Report      *memory.LintReport
	DailyReview *DailyReviewResult
	Compact     *RecentCompactResult
	Index       *memory.RebuildIndexResult
}

type DailyReviewResult struct {
	Path          string
	SessionCount  int
	TaskCount     int
	OpenTasks     int
	Completed     int
	Artifacts     int
	InboxProposed int
}

type RecentCompactResult struct {
	ArchivedDir string
	Kept        int
	Archived    int
}

type Runner struct {
	Config *config.Root
}

func NewRunner(cfg *config.Root) Runner {
	return Runner{Config: cfg}
}

func (r Runner) Status() (State, string, error) {
	path := r.statePath()
	state, err := readState(path)
	if err != nil {
		return State{}, path, err
	}
	return state, path, nil
}

func (r Runner) Run(opts RunOptions) (RunResult, error) {
	if r.Config == nil {
		return RunResult{}, fmt.Errorf("config is required")
	}
	agentID := strings.TrimSpace(opts.AgentID)
	if agentID == "" {
		agentID = firstNonEmpty(r.Config.Agents.Default, "main")
	}
	job := strings.TrimSpace(opts.Job)
	if job == "" {
		job = JobMemoryLint
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	statePath := r.statePath()
	state, err := readState(statePath)
	if err != nil {
		return RunResult{}, err
	}
	jobState := JobState{AgentID: agentID, Job: job, LastRunAt: now}
	var report *memory.LintReport
	switch job {
	case JobMemoryLint:
		root := filepath.Join(r.Config.App.Workspace, "memory")
		lintReport, err := memory.Lint(root)
		report = &lintReport
		if err != nil {
			jobState.Status = "failed"
			jobState.LastError = err.Error()
		} else {
			jobState.Status = "ok"
			jobState.Summary = fmt.Sprintf("%d issue(s)", len(lintReport.Issues))
		}
	case JobMemoryDailyReview:
		review, err := r.writeDailyReview(agentID, now)
		if err != nil {
			jobState.Status = "failed"
			jobState.LastError = err.Error()
		} else {
			jobState.Status = "ok"
			jobState.Summary = fmt.Sprintf("%d task(s), %d open, %d artifact(s)", review.TaskCount, review.OpenTasks, review.Artifacts)
			return r.finishRun(statePath, state, jobState, RunResult{State: jobState, DailyReview: &review})
		}
	case JobMemoryRecentCompact:
		compact, err := r.compactRecent(agentID, now)
		if err != nil {
			jobState.Status = "failed"
			jobState.LastError = err.Error()
		} else {
			jobState.Status = "ok"
			jobState.Summary = fmt.Sprintf("%d archived, %d kept", compact.Archived, compact.Kept)
			return r.finishRun(statePath, state, jobState, RunResult{State: jobState, Compact: &compact})
		}
	case JobMemoryIndexRebuild:
		index, err := memory.NewStore(r.Config.App.Workspace).RebuildIndex(now)
		if err != nil {
			jobState.Status = "failed"
			jobState.LastError = err.Error()
		} else {
			jobState.Status = "ok"
			jobState.Summary = fmt.Sprintf("%d indexed, %d issue(s)", len(index.Index.Entries), index.Index.IssueCount)
			return r.finishRun(statePath, state, jobState, RunResult{State: jobState, Index: &index})
		}
	default:
		jobState.Status = "failed"
		jobState.LastError = "unsupported heartbeat job: " + job
	}
	return r.finishRun(statePath, state, jobState, RunResult{State: jobState, Report: report})
}

func (r Runner) finishRun(statePath string, state State, jobState JobState, result RunResult) (RunResult, error) {
	state = upsertJobState(state, jobState)
	if err := writeState(statePath, state); err != nil {
		return RunResult{}, err
	}
	result.State = jobState
	if jobState.Status == "failed" {
		return result, fmt.Errorf(jobState.LastError)
	}
	return result, nil
}

func (r Runner) statePath() string {
	if r.Config == nil {
		return filepath.Join(config.DefaultHome(), "run", "scheduler", "state.json")
	}
	if dir := strings.TrimSpace(r.Config.Scheduler.StateDir); dir != "" {
		return filepath.Join(dir, "state.json")
	}
	return filepath.Join(r.Config.App.Home, "run", "scheduler", "state.json")
}

func readState(path string) (State, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func writeState(path string, state State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func upsertJobState(state State, next JobState) State {
	for i, item := range state.Jobs {
		if item.AgentID == next.AgentID && item.Job == next.Job {
			state.Jobs[i] = next
			return state
		}
	}
	state.Jobs = append(state.Jobs, next)
	return state
}

func (r Runner) writeDailyReview(agentID string, now time.Time) (DailyReviewResult, error) {
	if r.Config == nil {
		return DailyReviewResult{}, fmt.Errorf("config is required")
	}
	sessions, err := readSessionStates(filepath.Join(r.Config.App.Home, "run", "sessions"))
	if err != nil {
		return DailyReviewResult{}, err
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := start.Add(24 * time.Hour)
	var tasks []session.TaskState
	seenTasks := map[string]struct{}{}
	for _, st := range sessions {
		for _, task := range st.Tasks {
			t := firstTaskTime(task)
			if t.IsZero() || t.Before(start) || !t.Before(end) {
				continue
			}
			key := st.SessionKey + ":" + task.ID
			if _, ok := seenTasks[key]; ok {
				continue
			}
			seenTasks[key] = struct{}{}
			tasks = append(tasks, task)
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		return firstTaskTime(tasks[i]).Before(firstTaskTime(tasks[j]))
	})
	review := DailyReviewResult{SessionCount: len(sessions), TaskCount: len(tasks)}
	for _, task := range tasks {
		if task.IsOpenLike() {
			review.OpenTasks++
		}
		if task.Status == session.TaskCompleted {
			review.Completed++
		}
		review.Artifacts += len(task.Artifacts)
	}
	items, err := memory.NewStore(r.Config.App.Workspace).List(memory.ListOptions{AgentID: agentID, Area: "inbox", Status: "proposed"})
	if err != nil {
		return DailyReviewResult{}, err
	}
	review.InboxProposed = len(items)
	recentDir := filepath.Join(r.Config.App.Workspace, "memory", "agents", agentID, "recent")
	if err := os.MkdirAll(recentDir, 0o755); err != nil {
		return DailyReviewResult{}, err
	}
	review.Path = filepath.Join(recentDir, now.Format("2006-01-02")+".md")
	if err := os.WriteFile(review.Path, []byte(renderDailyReview(now, tasks, review)), 0o644); err != nil {
		return DailyReviewResult{}, err
	}
	if err := appendMemoryLog(r.Config.App.Workspace, now, "heartbeat memory_daily_review wrote "+review.Path); err != nil {
		return DailyReviewResult{}, err
	}
	return review, nil
}

func readSessionStates(dir string) ([]session.State, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var states []session.State
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var st session.State
		if err := json.Unmarshal(data, &st); err != nil {
			return nil, err
		}
		if strings.TrimSpace(st.SessionKey) == "" {
			st.SessionKey = decodeSessionKey(entry.Name())
		}
		states = append(states, st)
	}
	return states, nil
}

func renderDailyReview(now time.Time, tasks []session.TaskState, review DailyReviewResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Daily Memory Index: %s\n\n", now.Format("2006-01-02"))
	fmt.Fprintln(&b, "This is a factual maintenance index generated without model judgment. Review items manually before promoting anything to long memory.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Summary")
	fmt.Fprintf(&b, "- Sessions scanned: %d\n", review.SessionCount)
	fmt.Fprintf(&b, "- Tasks today: %d\n", review.TaskCount)
	fmt.Fprintf(&b, "- Completed tasks: %d\n", review.Completed)
	fmt.Fprintf(&b, "- Open tasks: %d\n", review.OpenTasks)
	fmt.Fprintf(&b, "- Artifacts: %d\n", review.Artifacts)
	fmt.Fprintf(&b, "- Proposed inbox items: %d\n", review.InboxProposed)
	if len(tasks) > 0 {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "## Tasks")
		for _, task := range tasks {
			fmt.Fprintf(&b, "- [%s] %s\n", firstNonEmpty(task.Status, "unknown"), compactReviewText(firstNonEmpty(task.Topic, task.PlanSummary, task.ResolvedQuery, task.UserText, task.ID), 160))
			if task.TraceID != "" {
				fmt.Fprintf(&b, "  - trace: %s\n", task.TraceID)
			}
			if len(task.ToolNames) > 0 {
				fmt.Fprintf(&b, "  - tools: %s\n", strings.Join(task.ToolNames, ", "))
			}
			if len(task.Artifacts) > 0 {
				fmt.Fprintf(&b, "  - artifacts: %d\n", len(task.Artifacts))
			}
		}
	}
	return b.String()
}

func (r Runner) compactRecent(agentID string, now time.Time) (RecentCompactResult, error) {
	retentionDays := r.Config.Memory.RecentDays
	if retentionDays <= 0 {
		retentionDays = 3
	}
	recentDir := filepath.Join(r.Config.App.Workspace, "memory", "agents", agentID, "recent")
	archiveDir := filepath.Join(recentDir, "archive")
	entries, err := os.ReadDir(recentDir)
	if os.IsNotExist(err) {
		return RecentCompactResult{ArchivedDir: archiveDir}, nil
	}
	if err != nil {
		return RecentCompactResult{}, err
	}
	cutoff := startOfDay(now).AddDate(0, 0, -retentionDays+1)
	result := RecentCompactResult{ArchivedDir: archiveDir}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		day, ok := parseRecentDate(entry.Name())
		if !ok {
			result.Kept++
			continue
		}
		if !day.Before(cutoff) {
			result.Kept++
			continue
		}
		if err := os.MkdirAll(archiveDir, 0o755); err != nil {
			return RecentCompactResult{}, err
		}
		src := filepath.Join(recentDir, entry.Name())
		dst := filepath.Join(archiveDir, entry.Name())
		if err := os.Rename(src, dst); err != nil {
			return RecentCompactResult{}, err
		}
		result.Archived++
	}
	if err := appendMemoryLog(r.Config.App.Workspace, now, fmt.Sprintf("heartbeat memory_recent_compact archived=%d kept=%d", result.Archived, result.Kept)); err != nil {
		return RecentCompactResult{}, err
	}
	return result, nil
}

func firstTaskTime(task session.TaskState) time.Time {
	for _, value := range []time.Time{task.UpdatedAt, task.FinishedAt, task.StartedAt} {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func parseRecentDate(name string) (time.Time, bool) {
	day, err := time.Parse("2006-01-02", strings.TrimSuffix(name, filepath.Ext(name)))
	if err != nil {
		return time.Time{}, false
	}
	return day, true
}

func compactReviewText(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return strings.TrimSpace(text[:limit-3]) + "..."
}

func appendMemoryLog(workspace string, at time.Time, line string) error {
	path := filepath.Join(workspace, "memory", "log.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(fmt.Sprintf("- %s %s\n", at.Format(time.RFC3339), strings.TrimSpace(line)))
	return err
}

func decodeSessionKey(name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	data, err := base64.RawURLEncoding.DecodeString(base)
	if err != nil {
		return base
	}
	return string(data)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
