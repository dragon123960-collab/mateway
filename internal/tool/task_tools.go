package tool

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/session"
)

type taskCandidate struct {
	SessionKey string
	ArchiveID  string
	Task       session.TaskNode
}

func (TaskSearchTool) Name() string        { return "task.search" }
func (TaskSearchTool) Description() string { return "search current and archived session tasks" }
func (TaskSearchTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"query"}}
}
func (TaskSearchTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when the user asks to find, recover, continue, or choose among previous tasks.",
		WhenNotToUse:         "Do not use when the current active task is already open; continue that task directly.",
		OutputContract:       "Return numbered candidate tasks with session, archive, task id, status, updated time, goal, and summary.",
		Evidence:             "Return candidate count and structured candidate identifiers.",
		Acceptance:           "Accepted when candidates are listed or count is zero.",
		SoftFailureSignals:   []string{"session not found", "archive read error"},
		ParallelMode:         "read_only_ok",
		ReusePolicy:          "stable_read",
		ConfirmationBoundary: "safe read; no confirmation.",
	}
}
func (TaskSearchTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (t TaskSearchTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	query := strings.TrimSpace(toolArgString(call.Args, "query"))
	sessionKey := strings.TrimSpace(toolArgString(call.Args, "session_key"))
	limit := intArg(call.Args["limit"])
	if limit <= 0 {
		limit = 8
	}
	store := session.NewStore(configHome(t.Config))
	candidates, err := searchTasks(store, sessionKey, query, limit)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	lines := make([]string, 0, len(candidates))
	evidence := make([]map[string]any, 0, len(candidates))
	for i, candidate := range candidates {
		task := candidate.Task
		lines = append(lines, fmt.Sprintf("%d. session=%s archive=%s task=%s status=%s updated=%s goal=%s summary=%s", i+1, candidate.SessionKey, candidate.ArchiveID, task.ID, task.Status, task.UpdatedAt.Format(time.RFC3339), summarizeToolText(task.Goal, 90), summarizeToolText(task.Summary, 90)))
		evidence = append(evidence, taskCandidateEvidence(candidate))
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: strings.Join(lines, "\n"), Evidence: map[string]any{"count": len(candidates), "candidates": evidence}}
}

func (TaskResumeTool) Name() string        { return "task.resume" }
func (TaskResumeTool) Description() string { return "load historical task context for continuation" }
func (TaskResumeTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"task_id"}}
}
func (TaskResumeTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use after task.search when one historical task candidate is clearly selected for continuation.",
		WhenNotToUse:         "Do not use to mutate historical archives or when the user has not identified which candidate to resume.",
		OutputContract:       "Return historical task context including source, task id, goal, summary, trace path when available, and current request.",
		Evidence:             "Return session_key, archive_id, task_id, status, goal, and summary identifiers.",
		Acceptance:           "Accepted when the requested task context is loaded.",
		SoftFailureSignals:   []string{"task_id is required", "task not found"},
		ParallelMode:         "read_only_ok",
		ReusePolicy:          "stable_read",
		ConfirmationBoundary: "safe read; no confirmation and no archive mutation.",
	}
}
func (TaskResumeTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (t TaskResumeTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	store := session.NewStore(configHome(t.Config))
	sessionKey := strings.TrimSpace(toolArgString(call.Args, "session_key"))
	archiveID := strings.TrimSpace(toolArgString(call.Args, "archive_id"))
	taskID := strings.TrimSpace(toolArgString(call.Args, "task_id"))
	task, source, err := loadHistoricalTask(store, sessionKey, archiveID, taskID)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	message := strings.TrimSpace(toolArgString(call.Args, "message"))
	var b strings.Builder
	b.WriteString("Historical task context loaded.\n")
	b.WriteString("Source: ")
	b.WriteString(source)
	b.WriteString("\nTask ID: ")
	b.WriteString(task.ID)
	b.WriteString("\nGoal: ")
	b.WriteString(strings.TrimSpace(task.Goal))
	if strings.TrimSpace(task.Summary) != "" {
		b.WriteString("\nSummary: ")
		b.WriteString(strings.TrimSpace(task.Summary))
	}
	if strings.TrimSpace(task.TracePath) != "" {
		b.WriteString("\nTrace: ")
		b.WriteString(strings.TrimSpace(task.TracePath))
	}
	if message != "" {
		b.WriteString("\nCurrent request: ")
		b.WriteString(message)
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: b.String(), Evidence: taskCandidateEvidence(taskCandidate{SessionKey: sessionKey, ArchiveID: archiveID, Task: task})}
}

func searchTasks(store session.Store, sessionKey, query string, limit int) ([]taskCandidate, error) {
	keys := []string{}
	if sessionKey != "" {
		keys = append(keys, sessionKey)
	} else {
		listed, err := store.List()
		if err != nil {
			return nil, err
		}
		keys = listed
	}
	query = normalizeTaskSearch(query)
	var out []taskCandidate
	for _, key := range keys {
		state, err := store.Load(key)
		if err == nil {
			addTaskMatches(&out, key, "", state.Tasks, query, limit)
		}
		if len(out) >= limit {
			break
		}
		archives, err := store.ListArchives(key)
		if err != nil {
			continue
		}
		for i := len(archives) - 1; i >= 0 && len(out) < limit; i-- {
			archived, _, err := store.LoadArchive(key, archives[i])
			if err != nil {
				continue
			}
			addTaskMatches(&out, key, archives[i], archived.Tasks, query, limit)
		}
	}
	return out, nil
}

func addTaskMatches(out *[]taskCandidate, sessionKey, archiveID string, tasks []session.TaskNode, query string, limit int) {
	for i := len(tasks) - 1; i >= 0 && len(*out) < limit; i-- {
		task := tasks[i]
		haystack := normalizeTaskSearch(task.Goal + " " + task.Summary + " " + task.ID)
		if query == "" || strings.Contains(haystack, query) || tokenOverlapCount(query, haystack) > 0 {
			*out = append(*out, taskCandidate{SessionKey: sessionKey, ArchiveID: archiveID, Task: task})
		}
	}
}

func loadHistoricalTask(store session.Store, sessionKey, archiveID, taskID string) (session.TaskNode, string, error) {
	if taskID == "" {
		return session.TaskNode{}, "", fmt.Errorf("task_id is required")
	}
	if sessionKey != "" && archiveID != "" {
		state, _, err := store.LoadArchive(sessionKey, archiveID)
		if err != nil {
			return session.TaskNode{}, "", err
		}
		if task := findTaskByID(state.Tasks, taskID); task != nil {
			return *task, "archive:" + archiveID, nil
		}
		return session.TaskNode{}, "", fmt.Errorf("task %s not found in archive %s", taskID, archiveID)
	}
	keys, err := store.List()
	if err != nil {
		return session.TaskNode{}, "", err
	}
	if sessionKey != "" {
		keys = []string{sessionKey}
	}
	for _, key := range keys {
		state, err := store.Load(key)
		if err == nil {
			if task := findTaskByID(state.Tasks, taskID); task != nil {
				return *task, "session:" + key, nil
			}
		}
		archives, err := store.ListArchives(key)
		if err != nil {
			continue
		}
		for i := len(archives) - 1; i >= 0; i-- {
			archived, _, err := store.LoadArchive(key, archives[i])
			if err != nil {
				continue
			}
			if task := findTaskByID(archived.Tasks, taskID); task != nil {
				return *task, "archive:" + archives[i], nil
			}
		}
	}
	return session.TaskNode{}, "", fmt.Errorf("task %s not found", taskID)
}

func findTaskByID(tasks []session.TaskNode, taskID string) *session.TaskNode {
	for i := range tasks {
		if tasks[i].ID == taskID {
			return &tasks[i]
		}
	}
	return nil
}

func taskCandidateEvidence(candidate taskCandidate) map[string]any {
	return map[string]any{
		"session_key": candidate.SessionKey,
		"archive_id":  candidate.ArchiveID,
		"task_id":     candidate.Task.ID,
		"goal":        candidate.Task.Goal,
		"summary":     candidate.Task.Summary,
		"status":      candidate.Task.Status,
		"updated_at":  candidate.Task.UpdatedAt.Format(time.RFC3339),
		"trace_path":  candidate.Task.TracePath,
	}
}

func normalizeTaskSearch(text string) string {
	replacer := strings.NewReplacer("，", " ", "。", " ", "？", " ", "！", " ", "：", " ", "；", " ", ",", " ", ".", " ", "?", " ", "!", " ", "\n", " ", "\t", " ")
	return strings.Join(strings.Fields(strings.ToLower(replacer.Replace(strings.TrimSpace(text)))), " ")
}

func tokenOverlapCount(a, b string) int {
	seen := map[string]bool{}
	for _, token := range strings.Fields(a) {
		if utf8.RuneCountInString(token) >= 2 {
			seen[token] = true
		}
	}
	count := 0
	for _, token := range strings.Fields(b) {
		if seen[token] {
			count++
		}
	}
	return count
}
