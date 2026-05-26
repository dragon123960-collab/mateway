package heartbeat

import (
	"context"
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
	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/session"
)

const (
	JobMemoryLint          = "memory_lint"
	JobMemoryDailyReview   = "memory_daily_review"
	JobMemoryDailyDistill  = "memory_daily_distill"
	JobMemoryRecentCompact = "memory_recent_compact"
	JobMemoryIndexRebuild  = "memory_index_rebuild"
)

const (
	dailyDistillTaskLimit       = 3
	dailyDistillConclusionLimit = 3
	dailyDistillSourceLimit     = 3
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
	DistillPath string
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
	Config   *config.Root
	Generate func(ctx context.Context, system string, messages []model.Message) (string, error)
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
	case JobMemoryDailyDistill:
		path, err := r.writeDailyDistillation(agentID, now)
		if err != nil {
			jobState.Status = "failed"
			jobState.LastError = err.Error()
		} else {
			jobState.Status = "ok"
			if strings.TrimSpace(path) == "" {
				jobState.Summary = "no distillation candidate"
			} else {
				jobState.Summary = "wrote distillation proposal"
			}
			return r.finishRun(statePath, state, jobState, RunResult{State: jobState, DistillPath: path})
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

func (r Runner) writeDailyDistillation(agentID string, now time.Time) (string, error) {
	if r.Config == nil {
		return "", fmt.Errorf("config is required")
	}
	sessions, err := readSessionStates(filepath.Join(r.Config.App.Home, "run", "sessions"))
	if err != nil {
		return "", err
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	end := start.Add(24 * time.Hour)
	var candidates []session.TaskState
	seenTasks := map[string]struct{}{}
	for _, st := range sessions {
		for _, task := range st.Tasks {
			t := firstTaskTime(task)
			if t.IsZero() || t.Before(start) || !t.Before(end) {
				continue
			}
			if task.Status != session.TaskCompleted || task.Failed {
				continue
			}
			if !taskWorthDistilling(task) {
				continue
			}
			key := st.SessionKey + ":" + task.ID
			if _, ok := seenTasks[key]; ok {
				continue
			}
			seenTasks[key] = struct{}{}
			candidates = append(candidates, task)
		}
	}
	if len(candidates) == 0 {
		return "", nil
	}
	candidates = dedupeDistillationCandidates(candidates)
	if len(candidates) == 0 {
		return "", nil
	}
	proposalType := classifyDistillationType(candidates)
	candidates, err = r.filterExistingDistillationOverlap(agentID, proposalType, candidates)
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return firstTaskTime(candidates[i]).Before(firstTaskTime(candidates[j]))
	})
	if len(candidates) > dailyDistillTaskLimit {
		candidates = candidates[:dailyDistillTaskLimit]
	}
	proposalType = classifyDistillationType(candidates)
	body, err := r.renderDailyDistillationBody(agentID, now, candidates, proposalType)
	if err != nil {
		return "", err
	}
	title, tags := distillationProposalMetadata(proposalType, now)
	result, err := memory.NewStore(r.Config.App.Workspace).Propose(memory.ProposalInput{
		AgentID:    agentID,
		Scope:      "agent",
		Type:       proposalType,
		Title:      title,
		Body:       body,
		Sources:    distillationSources(candidates),
		Tags:       tags,
		Confidence: distillationConfidenceForType(proposalType),
		CreatedAt:  now,
	})
	if err != nil {
		return "", err
	}
	if err := appendMemoryLog(r.Config.App.Workspace, now, "heartbeat memory_daily_distill wrote "+result.Path); err != nil {
		return "", err
	}
	return result.Path, nil
}

func classifyDistillationType(tasks []session.TaskState) string {
	texts := make([]string, 0, len(tasks))
	for _, task := range tasks {
		texts = append(texts, firstNonEmpty(task.Topic, task.PlanSummary, task.ResolvedQuery, task.UserText))
	}
	text := strings.ToLower(strings.TrimSpace(strings.Join(texts, " ")))
	switch {
	case containsAnyToken(text, "workflow", "playbook", "流程", "sop"):
		return "playbook"
	case containsAnyToken(text, "preference", "偏好", "喜欢", "用中文", "简短"):
		return "preference"
	case hasExplicitProjectCue(text):
		return "project"
	case containsAnyToken(text, "decision", "方向", "原则", "决策", "约定", "memory", "rule", "direction"):
		return "decision"
	default:
		return "project"
	}
}

func distillationConfidenceForType(typ string) string {
	switch strings.TrimSpace(typ) {
	case "playbook", "decision":
		return "medium"
	case "preference", "project":
		return "low"
	default:
		return "low"
	}
}

func distillationProposalMetadata(proposalType string, now time.Time) (string, []string) {
	date := now.Format("2006-01-02")
	switch strings.TrimSpace(proposalType) {
	case "playbook":
		return "Daily playbook distillation " + date, []string{"daily-distillation", "auto-proposal", "distill-playbook"}
	case "preference":
		return "Daily preference distillation " + date, []string{"daily-distillation", "auto-proposal", "distill-preference"}
	case "project":
		return "Daily project distillation " + date, []string{"daily-distillation", "auto-proposal", "distill-project"}
	default:
		return "Daily decision distillation " + date, []string{"daily-distillation", "auto-proposal", "distill-decision"}
	}
}

func containsAnyToken(text string, tokens ...string) bool {
	for _, token := range tokens {
		if strings.Contains(text, strings.ToLower(strings.TrimSpace(token))) {
			return true
		}
	}
	return false
}

func (r Runner) filterExistingDistillationOverlap(agentID, proposalType string, tasks []session.TaskState) ([]session.TaskState, error) {
	tasks, err := r.filterExistingLongMemory(agentID, proposalType, tasks)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, nil
	}
	return r.filterExistingInboxProposals(agentID, proposalType, tasks)
}

func (r Runner) filterExistingLongMemory(agentID, proposalType string, tasks []session.TaskState) ([]session.TaskState, error) {
	store := memory.NewStore(r.Config.App.Workspace)
	candidates, err := store.SearchLong(memory.SearchOptions{
		AgentID: agentID,
		Query:   "memory direction rule decision workflow preference",
		Limit:   200,
	})
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return tasks, nil
	}
	var out []session.TaskState
	for _, task := range tasks {
		if !taskOverlapsExistingLongMemory(task, proposalType, candidates) {
			out = append(out, task)
		}
	}
	return out, nil
}

func (r Runner) filterExistingInboxProposals(agentID, proposalType string, tasks []session.TaskState) ([]session.TaskState, error) {
	items, err := memory.NewStore(r.Config.App.Workspace).List(memory.ListOptions{
		AgentID: agentID,
		Area:    "inbox",
		Status:  "proposed",
		Kind:    proposalType,
	})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return tasks, nil
	}
	var out []session.TaskState
	for _, task := range tasks {
		if !taskOverlapsExistingInboxProposal(task, items) {
			out = append(out, task)
		}
	}
	return out, nil
}

func dedupeDistillationCandidates(tasks []session.TaskState) []session.TaskState {
	if len(tasks) == 0 {
		return nil
	}
	best := map[string]session.TaskState{}
	for _, task := range tasks {
		key := distillationCandidateKey(task)
		if key == "" {
			key = "task:" + firstNonEmpty(task.ID, task.TraceID)
		}
		existing, ok := best[key]
		if !ok || firstTaskTime(task).Before(firstTaskTime(existing)) {
			best[key] = task
		}
	}
	keys := make([]string, 0, len(best))
	for key := range best {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]session.TaskState, 0, len(keys))
	for _, key := range keys {
		out = append(out, best[key])
	}
	sort.SliceStable(out, func(i, j int) bool {
		return firstTaskTime(out[i]).Before(firstTaskTime(out[j]))
	})
	return out
}

func distillationCandidateKey(task session.TaskState) string {
	base := strings.ToLower(strings.TrimSpace(firstNonEmpty(task.PlanSummary, task.ResolvedQuery, task.Topic, task.UserText)))
	if base == "" {
		return ""
	}
	base = strings.Join(strings.Fields(base), " ")
	return fmt.Sprintf("%s|%s|%s", classifyDistillationType([]session.TaskState{task}), strings.Join(task.ToolNames, ","), base)
}

func taskOverlapsExistingLongMemory(task session.TaskState, proposalType string, items []memory.SearchResult) bool {
	text := strings.ToLower(strings.TrimSpace(strings.Join([]string{
		task.Topic,
		task.PlanSummary,
		task.ResolvedQuery,
		task.UserText,
	}, " ")))
	if text == "" {
		return false
	}
	for _, item := range items {
		if !strings.EqualFold(strings.TrimSpace(item.Type), strings.TrimSpace(proposalType)) {
			continue
		}
		title := strings.ToLower(strings.TrimSpace(item.Title))
		snippet := strings.ToLower(strings.TrimSpace(item.Snippet))
		switch {
		case title != "" && (strings.Contains(text, title) || strings.Contains(title, text)):
			return true
		case snippet != "" && (strings.Contains(text, snippet) || strings.Contains(snippet, text)):
			return true
		}
	}
	return false
}

func taskOverlapsExistingInboxProposal(task session.TaskState, items []memory.MemoryItem) bool {
	text := strings.ToLower(strings.TrimSpace(strings.Join([]string{
		task.Topic,
		task.PlanSummary,
		task.ResolvedQuery,
		task.UserText,
	}, " ")))
	if text == "" {
		return false
	}
	for _, item := range items {
		title := strings.ToLower(strings.TrimSpace(item.Title))
		if title == "" {
			continue
		}
		if strings.Contains(text, title) || strings.Contains(title, text) {
			return true
		}
	}
	return false
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

func taskWorthDistilling(task session.TaskState) bool {
	text := firstNonEmpty(task.Topic, task.PlanSummary, task.ResolvedQuery, task.UserText)
	if isLowValueDistillationTask(text, task.ToolNames) {
		return false
	}
	switch classifyDistillationType([]session.TaskState{task}) {
	case "playbook":
		if !playbookTaskLooksStable(task) {
			return false
		}
	case "decision":
		if !decisionTaskLooksStable(task) {
			return false
		}
	case "preference":
		if !preferenceTaskLooksStable(task) {
			return false
		}
	case "project":
		if !projectTaskLooksStable(task) {
			return false
		}
	}
	return distillationSignalScore(task) >= 2
}

func distillationSignalScore(task session.TaskState) int {
	score := 0
	text := firstNonEmpty(task.Topic, task.PlanSummary, task.ResolvedQuery, task.UserText)
	if textLooksDurable(text) {
		score++
	}
	if len(task.Artifacts) > 0 {
		score++
	}
	for _, toolName := range task.ToolNames {
		switch strings.TrimSpace(toolName) {
		case "web.search", "web.fetch", "memory.search", "file.write", "file.patch":
			score++
		}
	}
	if hasGroundedArtifact(task.Artifacts) {
		score++
	}
	if classifyDistillationType([]session.TaskState{task}) != "project" {
		score++
	}
	return score
}

func hasGroundedArtifact(artifacts []session.Artifact) bool {
	for _, artifact := range artifacts {
		if strings.TrimSpace(artifact.Path) != "" || strings.TrimSpace(artifact.SourceURL) != "" {
			return true
		}
	}
	return false
}

func playbookTaskLooksStable(task session.TaskState) bool {
	text := strings.ToLower(firstNonEmpty(task.Topic, task.PlanSummary, task.ResolvedQuery, task.UserText))
	if !hasExplicitPlaybookCue(text) {
		return false
	}
	if hasGroundedArtifact(task.Artifacts) {
		return true
	}
	if hasPlaybookContextTool(task.ToolNames) && !looksLikeReadOnlyDocTask(text, task.ToolNames) {
		return true
	}
	return false
}

func hasExplicitPlaybookCue(text string) bool {
	return containsAnyToken(text,
		"workflow", "playbook", "procedure", "step by step", "repeatable", "operating pattern",
		"流程", "步骤", "操作手册", "可复用", "标准做法", "sop",
	)
}

func hasPlaybookContextTool(toolNames []string) bool {
	for _, toolName := range toolNames {
		switch strings.TrimSpace(toolName) {
		case "file.write", "file.patch", "web.search", "web.fetch", "memory.search":
			return true
		}
	}
	return false
}

func decisionTaskLooksStable(task session.TaskState) bool {
	text := strings.ToLower(firstNonEmpty(task.Topic, task.PlanSummary, task.ResolvedQuery, task.UserText))
	if !hasExplicitDecisionCue(text) {
		return false
	}
	if hasGroundedArtifact(task.Artifacts) {
		return true
	}
	if hasDecisionContextTool(task.ToolNames) && !looksLikeReadOnlyDocTask(text, task.ToolNames) {
		return true
	}
	return false
}

func hasExplicitDecisionCue(text string) bool {
	return containsAnyToken(text,
		"decision", "rule", "direction", "working rule", "operating rule", "constraint",
		"原则", "决策", "规则", "方向", "约定", "边界条件",
	)
}

func hasDecisionContextTool(toolNames []string) bool {
	for _, toolName := range toolNames {
		switch strings.TrimSpace(toolName) {
		case "file.write", "file.patch", "web.search", "web.fetch", "memory.search":
			return true
		}
	}
	return false
}

func preferenceTaskLooksStable(task session.TaskState) bool {
	text := strings.ToLower(firstNonEmpty(task.Topic, task.PlanSummary, task.ResolvedQuery, task.UserText))
	if !hasExplicitPreferenceCue(text) {
		return false
	}
	if hasGroundedArtifact(task.Artifacts) {
		return true
	}
	if hasPreferenceContextTool(task.ToolNames) && !looksLikeReadOnlyDocTask(text, task.ToolNames) {
		return true
	}
	return false
}

func hasExplicitPreferenceCue(text string) bool {
	return containsAnyToken(text,
		"user preference", "working preference", "prefer", "preference", "reply in chinese",
		"用中文", "中文回复", "偏好", "喜欢", "简短", "语气", "风格",
	)
}

func hasPreferenceContextTool(toolNames []string) bool {
	for _, toolName := range toolNames {
		switch strings.TrimSpace(toolName) {
		case "file.write", "file.patch", "web.search", "web.fetch", "memory.search":
			return true
		}
	}
	return false
}

func projectTaskLooksStable(task session.TaskState) bool {
	text := strings.ToLower(firstNonEmpty(task.Topic, task.PlanSummary, task.ResolvedQuery, task.UserText))
	if !hasExplicitProjectCue(text) {
		return false
	}
	if hasGroundedArtifact(task.Artifacts) {
		return true
	}
	if hasProjectContextTool(task.ToolNames) && !looksLikeReadOnlyDocTask(text, task.ToolNames) {
		return true
	}
	return false
}

func hasExplicitProjectCue(text string) bool {
	return containsAnyToken(text,
		"project architecture", "architecture", "scope", "boundary", "project direction",
		"project fact", "system design", "technical direction",
		"架构", "范围", "边界", "方向", "设计", "项目事实",
	)
}

func hasProjectContextTool(toolNames []string) bool {
	for _, toolName := range toolNames {
		switch strings.TrimSpace(toolName) {
		case "file.write", "file.patch", "web.search", "web.fetch", "memory.search":
			return true
		}
	}
	return false
}

func isLowValueDistillationTask(text string, toolNames []string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return true
	}
	if containsAnyToken(normalized,
		"test", "tests", "测试", "单测",
		"debug", "诊断", "排查", "check status", "health check",
		"temporary", "临时", "一次性", "quick check",
	) {
		return true
	}
	if looksLikeReadOnlyDocTask(normalized, toolNames) && !textLooksDurable(normalized) {
		return true
	}
	return false
}

func looksLikeReadOnlyDocTask(text string, toolNames []string) bool {
	if !containsAnyToken(text, "readme", "文档", "总结", "读取", "review", "summary") {
		return false
	}
	if len(toolNames) == 0 {
		return true
	}
	for _, toolName := range toolNames {
		switch strings.TrimSpace(toolName) {
		case "file.read", "file.summary", "project.index":
		default:
			return false
		}
	}
	return true
}

func textLooksDurable(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	markers := []string{
		"memory", "direction", "rule", "decision", "workflow", "preference",
		"记忆", "方向", "规则", "决策", "流程", "偏好",
	}
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (r Runner) renderDailyDistillationBody(agentID string, now time.Time, tasks []session.TaskState, proposalType string) (string, error) {
	if r.Config == nil {
		return "", fmt.Errorf("config is required")
	}
	generate := r.Generate
	if generate == nil {
		defaultAgent, err := r.Config.DefaultAgentStrict()
		if err != nil {
			return "", err
		}
		modelCfg, err := selectHeartbeatModel(r.Config.Models, defaultAgent.Model)
		if err != nil {
			return "", err
		}
		client := model.NewClient(modelCfg)
		generate = client.Generate
	}
	distilled, err := generate(context.Background(), dailyDistillationSystemPrompt(proposalType), []model.Message{{
		Role:    "user",
		Content: buildDailyDistillationPrompt(now, proposalType, tasks),
	}})
	if err != nil {
		return "", err
	}
	distilled, err = r.filterDistilledConclusions(agentID, proposalType, distilled)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "This is an automatically proposed daily distillation generated from factual task review on %s. Review it before committing to long memory.\n\n", now.Format("2006-01-02"))
	fmt.Fprintln(&b, "## Candidate Distillation")
	fmt.Fprintln(&b, "These items were selected because they look more durable than ordinary one-off task chatter. This draft is still conservative, but now includes a lightweight model summary for candidate conclusions.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Review Boundary")
	fmt.Fprintln(&b, distillationReviewNote(proposalType))
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Recommended Promotion Target")
	fmt.Fprintln(&b, distillationPromotionTarget(proposalType))
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Why It May Be Worth Keeping")
	fmt.Fprintln(&b, distillationRetentionReason(proposalType, tasks))
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Evidence Signals")
	for _, line := range distillationEvidenceSummary(proposalType, tasks) {
		fmt.Fprintln(&b, line)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Filtering Notes")
	for _, line := range distillationFilteringNotes(proposalType) {
		fmt.Fprintln(&b, line)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Candidate Conclusions")
	fmt.Fprintln(&b, limitMarkdownBullets(distilled, dailyDistillConclusionLimit))
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, distillationCandidateItemsHeading(proposalType))
	for _, line := range distillationCandidateItems(proposalType, tasks) {
		fmt.Fprintln(&b, line)
	}
	return strings.TrimSpace(b.String()), nil
}

func (r Runner) filterDistilledConclusions(agentID, proposalType, distilled string) (string, error) {
	store := memory.NewStore(r.Config.App.Workspace)
	longItems, err := store.SearchLong(memory.SearchOptions{
		AgentID: agentID,
		Query:   "memory direction rule decision workflow preference project architecture scope boundary",
		Limit:   200,
	})
	if err != nil {
		return "", err
	}
	inboxItems, err := store.List(memory.ListOptions{
		AgentID: agentID,
		Area:    "inbox",
		Status:  "proposed",
		Kind:    proposalType,
	})
	if err != nil {
		return "", err
	}
	return dedupeDistilledConclusionsAgainstMemory(proposalType, distilled, longItems, inboxItems), nil
}

func dedupeDistilledConclusionsAgainstMemory(proposalType, distilled string, longItems []memory.SearchResult, inboxItems []memory.MemoryItem) string {
	bullets := markdownBullets(distilled)
	if len(bullets) == 0 {
		return strings.TrimSpace(distilled)
	}
	var kept []string
	for _, bullet := range bullets {
		if !distilledConclusionOverlapsMemory(proposalType, bullet, longItems, inboxItems) {
			kept = append(kept, bullet)
		}
		if len(kept) >= dailyDistillConclusionLimit {
			break
		}
	}
	if len(kept) == 0 {
		kept = append(kept, bullets[0])
	}
	return strings.Join(kept, "\n")
}

func markdownBullets(text string) []string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	var bullets []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			bullets = append(bullets, trimmed)
		}
	}
	return bullets
}

func distilledConclusionOverlapsMemory(proposalType, bullet string, longItems []memory.SearchResult, inboxItems []memory.MemoryItem) bool {
	text := strings.ToLower(strings.TrimSpace(strings.TrimLeft(strings.TrimLeft(bullet, "-"), "*")))
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return false
	}
	for _, item := range longItems {
		if !strings.EqualFold(strings.TrimSpace(item.Type), strings.TrimSpace(proposalType)) {
			continue
		}
		if memoryTextClearlyOverlaps(text, item.Title, item.Snippet) {
			return true
		}
	}
	for _, item := range inboxItems {
		if memoryTextClearlyOverlaps(text, item.Title) {
			return true
		}
	}
	return false
}

func memoryTextClearlyOverlaps(text string, candidates ...string) bool {
	for _, candidate := range candidates {
		normalized := strings.ToLower(strings.TrimSpace(candidate))
		normalized = strings.Join(strings.Fields(normalized), " ")
		if normalized == "" {
			continue
		}
		if strings.Contains(text, normalized) || strings.Contains(normalized, text) {
			return true
		}
	}
	return false
}

func distillationRetentionReason(proposalType string, tasks []session.TaskState) string {
	count := len(tasks)
	switch strings.TrimSpace(proposalType) {
	case "preference":
		return fmt.Sprintf("- %d completed task(s) point toward a possibly stable working preference rather than one-off output formatting chatter.", count)
	case "playbook":
		return fmt.Sprintf("- %d completed task(s) show a repeatable workflow or operating pattern that may be worth preserving as a playbook.", count)
	case "project":
		return fmt.Sprintf("- %d completed task(s) suggest a project fact or direction that may still matter beyond the day-level review.", count)
	default:
		return fmt.Sprintf("- %d completed task(s) suggest a durable decision, rule, or direction that may be useful as long memory after review.", count)
	}
}

func distillationEvidenceSummary(proposalType string, tasks []session.TaskState) []string {
	artifactCount := 0
	toolCounts := map[string]int{}
	var sourceRefs []string
	for _, task := range tasks {
		artifactCount += len(task.Artifacts)
		for _, toolName := range task.ToolNames {
			toolName = strings.TrimSpace(toolName)
			if toolName == "" {
				continue
			}
			toolCounts[toolName]++
		}
		if ref := firstNonEmpty(task.TraceID, task.ID); ref != "" && len(sourceRefs) < dailyDistillSourceLimit {
			sourceRefs = append(sourceRefs, ref)
		}
	}
	lines := []string{
		fmt.Sprintf("- Completed candidate tasks: %d", len(tasks)),
		fmt.Sprintf("- Artifact evidence count: %d", artifactCount),
	}
	if tools := summarizeDistillationTools(toolCounts); tools != "" {
		lines = append(lines, "- Tool signals: "+tools)
	}
	if len(sourceRefs) > 0 {
		lines = append(lines, "- Example task/trace refs: "+strings.Join(sourceRefs, ", "))
	}
	switch strings.TrimSpace(proposalType) {
	case "playbook":
		lines = append(lines, fmt.Sprintf("- Repeatable workflow signals: %d task(s) included procedural or workflow-like wording.", countDistillationTasksMatching(tasks, "workflow", "playbook", "流程", "步骤", "daily report")))
	case "preference":
		lines = append(lines, fmt.Sprintf("- Preference-like signals: %d task(s) referenced style, language, or working-preference hints.", countDistillationTasksMatching(tasks, "preference", "偏好", "用中文", "简短", "style", "reply")))
	case "project":
		lines = append(lines, fmt.Sprintf("- Project-direction signals: %d task(s) referenced project facts, scope, or architecture context.", countDistillationTasksMatching(tasks, "project", "architecture", "方向", "scope", "memory")))
	default:
		lines = append(lines, fmt.Sprintf("- Decision/rule signals: %d task(s) referenced direction, rule, or decision language.", countDistillationTasksMatching(tasks, "decision", "direction", "rule", "memory", "原则", "决策")))
	}
	return lines
}

func distillationFilteringNotes(proposalType string) []string {
	lines := []string{
		"- Only completed tasks with durable-signal gating are considered.",
		"- Same-day similar tasks are deduped before distillation.",
		"- Obvious overlap with same-type long memory and proposed inbox items is filtered before proposal write.",
		"- Candidate conclusions are post-checked against existing same-type memory to reduce repeated bullets.",
	}
	switch strings.TrimSpace(proposalType) {
	case "playbook":
		lines = append(lines, "- Playbook candidates must show explicit workflow/playbook cues plus grounded evidence or non-read-only execution context.")
	case "preference":
		lines = append(lines, "- Preference candidates must show explicit preference cues plus grounded evidence or non-read-only execution context.")
	case "project":
		lines = append(lines, "- Project candidates must show explicit architecture/scope/boundary-style cues plus grounded evidence or non-read-only execution context.")
	default:
		lines = append(lines, "- Decision candidates must show explicit decision/rule/direction cues plus grounded evidence or non-read-only execution context.")
	}
	return lines
}

func countDistillationTasksMatching(tasks []session.TaskState, markers ...string) int {
	count := 0
	for _, task := range tasks {
		text := strings.ToLower(firstNonEmpty(task.Topic, task.PlanSummary, task.ResolvedQuery, task.UserText))
		if containsAnyToken(text, markers...) {
			count++
		}
	}
	return count
}

func summarizeDistillationTools(toolCounts map[string]int) string {
	if len(toolCounts) == 0 {
		return ""
	}
	type pair struct {
		name  string
		count int
	}
	var pairs []pair
	for name, count := range toolCounts {
		pairs = append(pairs, pair{name: name, count: count})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].count == pairs[j].count {
			return pairs[i].name < pairs[j].name
		}
		return pairs[i].count > pairs[j].count
	})
	parts := make([]string, 0, len(pairs))
	for _, item := range pairs {
		parts = append(parts, fmt.Sprintf("%s(%d)", item.name, item.count))
	}
	return strings.Join(parts, ", ")
}

func distillationCandidateItemsHeading(proposalType string) string {
	switch strings.TrimSpace(proposalType) {
	case "playbook":
		return "## Candidate Workflow Signals"
	case "preference":
		return "## Candidate Preference Signals"
	case "project":
		return "## Candidate Project Signals"
	default:
		return "## Candidate Decision Signals"
	}
}

func distillationCandidateItems(proposalType string, tasks []session.TaskState) []string {
	lines := make([]string, 0, len(tasks)*5)
	for _, task := range tasks {
		title := compactReviewText(firstNonEmpty(task.Topic, task.PlanSummary, task.ResolvedQuery, task.UserText, task.ID), 180)
		switch strings.TrimSpace(proposalType) {
		case "playbook":
			lines = append(lines, "- Workflow candidate: "+title)
			if strings.TrimSpace(task.PlanSummary) != "" {
				lines = append(lines, "  - repeatable plan signal: "+compactReviewText(task.PlanSummary, 160))
			}
		case "preference":
			lines = append(lines, "- Preference candidate: "+title)
			lines = append(lines, "  - preference cue: review whether this reflects a stable user or working style preference")
		case "project":
			lines = append(lines, "- Project candidate: "+title)
			lines = append(lines, "  - project cue: review whether this remains useful beyond the day-level summary")
		default:
			lines = append(lines, "- Decision candidate: "+title)
			lines = append(lines, "  - decision cue: review whether this expresses a stable rule, direction, or operating constraint")
		}
		if len(task.ToolNames) > 0 {
			lines = append(lines, "  - tools: "+strings.Join(task.ToolNames, ", "))
		}
		if len(task.Artifacts) > 0 {
			lines = append(lines, fmt.Sprintf("  - artifacts: %d", len(task.Artifacts)))
		}
		lines = append(lines, "  - task: "+firstNonEmpty(task.ID, "unknown"))
		lines = append(lines, "  - trace: "+firstNonEmpty(task.TraceID, "unknown"))
	}
	return lines
}

func distillationReviewNote(proposalType string) string {
	switch strings.TrimSpace(proposalType) {
	case "preference":
		return "- Treat this as a possible stable preference only if it looks durable across tasks. Be conservative before promoting it."
	case "playbook":
		return "- Confirm the workflow is repeatable and not just a one-off workaround before promoting it."
	case "project":
		return "- Confirm this is a lasting project fact or direction, not just a temporary summary for the day."
	default:
		return "- Confirm this conclusion is stable, source-backed, and useful enough to keep as long memory."
	}
}

func distillationPromotionTarget(proposalType string) string {
	switch strings.TrimSpace(proposalType) {
	case "preference":
		return "- Promote only if this looks like a stable user or working preference. Prefer a preference-style long memory page."
	case "playbook":
		return "- Promote into a workflow/playbook-style long memory page if the procedure is repeatable."
	case "project":
		return "- Promote into a project fact/note-style long memory page only if this remains useful beyond today."
	default:
		return "- Promote into a decision-style long memory page if the conclusion is stable and source-backed."
	}
}

func distillationSources(tasks []session.TaskState) []string {
	var sources []string
	for _, task := range tasks {
		if task.ID != "" {
			sources = append(sources, "task:"+task.ID)
		}
		if task.TraceID != "" {
			sources = append(sources, "trace:"+task.TraceID)
		}
	}
	if len(sources) == 0 {
		return []string{"manual"}
	}
	return sources
}

func buildDailyDistillationPrompt(now time.Time, proposalType string, tasks []session.TaskState) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Date: %s\n\n", now.Format("2006-01-02"))
	fmt.Fprintf(&b, "Distillation type: %s\n\n", strings.TrimSpace(proposalType))
	fmt.Fprintln(&b, "Summarize 1-3 candidate durable conclusions from these completed tasks.")
	fmt.Fprintln(&b, distillationPromptInstruction(proposalType))
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Tasks:")
	for _, task := range tasks {
		fmt.Fprintf(&b, "- topic: %s\n", compactReviewText(firstNonEmpty(task.Topic, task.PlanSummary, task.ResolvedQuery, task.UserText, task.ID), 180))
		if strings.TrimSpace(task.PlanSummary) != "" {
			fmt.Fprintf(&b, "  plan: %s\n", compactReviewText(task.PlanSummary, 160))
		}
		if len(task.ToolNames) > 0 {
			fmt.Fprintf(&b, "  tools: %s\n", strings.Join(task.ToolNames, ", "))
		}
		if len(task.Artifacts) > 0 {
			fmt.Fprintf(&b, "  artifacts: %d\n", len(task.Artifacts))
		}
	}
	return strings.TrimSpace(b.String())
}

func distillationPromptInstruction(proposalType string) string {
	switch strings.TrimSpace(proposalType) {
	case "playbook":
		return "Prefer repeatable workflow knowledge, step ordering, and reusable operating patterns. Avoid one-off fixes, transient debugging chatter, or purely descriptive summaries."
	case "preference":
		return "Prefer stable user or working preferences such as language, tone, or output-shape expectations. Avoid guessing hidden preferences from a single weak signal."
	case "project":
		return "Prefer lasting project facts, scope boundaries, architecture direction, or durable context that still matters beyond today. Avoid short-lived progress notes."
	default:
		return "Prefer stable decisions, working rules, operating constraints, or project direction. Avoid generic process chatter or tentative statements that are not yet source-backed."
	}
}

func limitMarkdownBullets(text string, limit int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if limit <= 0 {
		return strings.TrimSpace(text)
	}
	var out []string
	count := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			if count >= limit {
				break
			}
			count++
		}
		if trimmed != "" {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func dailyDistillationSystemPrompt(proposalType string) string {
	lines := []string{
		"You are the Mateway memory distillation assistant.",
		"Return concise markdown bullets only.",
		"Do not invent facts not supported by the tasks.",
		"Do not propose secrets, credentials, or unstable one-off details.",
	}
	switch strings.TrimSpace(proposalType) {
	case "playbook":
		lines = append(lines, "Prefer reusable workflow knowledge and step patterns that could help on future similar tasks.")
	case "preference":
		lines = append(lines, "Prefer only clearly signaled stable preferences; be conservative and avoid overfitting to one turn.")
	case "project":
		lines = append(lines, "Prefer durable project facts, boundaries, or architecture context that remain useful after the daily review.")
	default:
		lines = append(lines, "Prefer durable conclusions such as stable decisions, working rules, and project direction.")
	}
	return strings.Join(lines, "\n")
}

func selectHeartbeatModel(models []config.ModelConfig, selection config.ModelSelection) (config.ModelConfig, error) {
	if name := strings.TrimSpace(selection.Default); name != "" {
		for _, item := range models {
			if item.Enabled && strings.EqualFold(item.Name, name) {
				return item, nil
			}
		}
	}
	for _, item := range models {
		if strings.EqualFold(item.Name, "minimax") && item.Enabled {
			return item, nil
		}
	}
	for _, item := range models {
		if item.Enabled {
			return item, nil
		}
	}
	return config.ModelConfig{}, fmt.Errorf("no enabled model found for heartbeat distillation")
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
