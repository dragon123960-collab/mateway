package tool

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/schedule"
)

func (ScheduleManageTool) Name() string { return "schedule.manage" }
func (ScheduleManageTool) Description() string {
	return "manage local scheduled tasks: create, list, update, pause, resume, delete, run_now"
}
func (ScheduleManageTool) Schema() agentcore.Schema {
	return agentcore.Schema{
		Required: []string{"action"},
		Properties: map[string]any{
			"action":       map[string]any{"type": "string", "enum": []string{"create", "list", "update", "pause", "resume", "delete", "run_now"}},
			"id":           map[string]any{"type": "string", "description": "Required for update, pause, resume, delete, run_now"},
			"text":         map[string]any{"type": "string", "description": "Task text. Required for create, optional for update."},
			"run_at":       map[string]any{"type": "string", "description": "RFC3339 time. Required for create, optional for update."},
			"interval":     map[string]any{"type": "string", "description": "Go duration: 30m, 24h. Optional for create/update."},
			"session_key":  map[string]any{"type": "string", "description": "Optional session key for the scheduled task."},
			"require_test": map[string]any{"type": "boolean", "description": "If true, create as paused pending test. Default true."},
			"status":       map[string]any{"type": "string", "description": "For update: active, paused, done."},
		},
	}
}
func (ScheduleManageTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Manage local scheduled tasks. action=list is a safe read. action=delete permanently removes a task. action=create/update/pause/resume/run_now are guarded mutations that change schedule state. Use action=create when the user asks to run a task later. New schedules default to require_test=true so they remain pending until a successful test run. Scheduled tasks are channel-neutral: the scheduler does not automatically send results back to channels.",
		WhenNotToUse:         "Do not use for immediate tasks; execute those directly. Do not use action=delete when the user only wants to temporarily stop a task; use action=pause instead.",
		OutputContract:       "Return schedule id, status, run time, and interval. action=delete returns deleted=true/false. action=list returns one line per task.",
		Evidence:             "Return id, status, run_at, interval, session_key. action=delete returns id and deleted boolean. action=list returns count.",
		Acceptance:           "Accepted when the schedule store completes the requested action.",
		SoftFailureSignals:   []string{"schedule not found", "invalid run_at", "missing text", "unknown action"},
		ParallelMode:         "forbid",
		ReusePolicy:          "never",
		ConfirmationBoundary: "risks vary by action: list is safe read, delete is dangerous, others are guarded mutation. Tool policy enforces destructive boundaries.",
	}
}
func (ScheduleManageTool) Risk() agentcore.Risk { return agentcore.RiskGuardedMutation }
func (t ScheduleManageTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	action := toolArgString(call.Args, "action")
	switch action {
	case "create":
		return scheduleActionCreate(t.Config, call)
	case "list":
		return scheduleActionList(t.Config, call)
	case "update":
		return scheduleActionUpdate(t.Config, call)
	case "pause":
		return scheduleActionPause(t.Config, call)
	case "resume":
		return scheduleActionResume(t.Config, call)
	case "delete":
		return scheduleActionDelete(t.Config, call)
	case "run_now":
		return scheduleActionRunNow(t.Config, call)
	default:
		return agentcore.ToolResult{ToolCallID: call.ID, Content: "unknown action: " + action, IsError: true}
	}
}

func scheduleActionCreate(cfg *config.Root, call agentcore.ToolCall) agentcore.ToolResult {
	runAt, err := time.Parse(time.RFC3339, toolArgString(call.Args, "run_at"))
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: "run_at must be RFC3339", IsError: true}
	}
	var interval time.Duration
	if raw := toolArgString(call.Args, "interval"); raw != "" {
		interval, err = time.ParseDuration(raw)
		if err != nil {
			return agentcore.ToolResult{ToolCallID: call.ID, Content: "interval must be a Go duration such as 30m or 24h", IsError: true}
		}
	}
	requireTest := true
	if raw := strings.ToLower(toolArgString(call.Args, "require_test")); raw == "false" || raw == "no" {
		requireTest = false
	} else if raw == "true" || raw == "yes" {
		requireTest = true
	}
	task, err := scheduleStore(cfg).Create(schedule.CreateInput{
		SessionKey:  toolArgString(call.Args, "session_key"),
		Text:        toolArgString(call.Args, "text"),
		RunAt:       runAt,
		Interval:    interval,
		RequireTest: requireTest,
		Activate:    !requireTest,
	})
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	return agentcore.ToolResult{
		ToolCallID: call.ID,
		Content:    fmt.Sprintf("scheduled %s status=%s at %s", task.ID, task.Status, task.RunAt),
		Evidence: map[string]any{
			"id":           task.ID,
			"status":       task.Status,
			"run_at":       task.RunAt,
			"interval":     task.Interval,
			"session_key":  task.SessionKey,
			"require_test": task.RequireTest,
		},
	}
}

func scheduleActionList(cfg *config.Root, call agentcore.ToolCall) agentcore.ToolResult {
	tasks, err := scheduleStore(cfg).List()
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	var lines []string
	for _, task := range tasks {
		lines = append(lines, fmt.Sprintf("%s status=%s run_at=%s interval=%s last=%s text=%s", task.ID, task.Status, task.RunAt, task.Interval, task.LastRunStatus, summarizeToolText(task.Text, 80)))
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: strings.Join(lines, "\n"), Evidence: map[string]any{"count": len(tasks)}}
}

func scheduleActionUpdate(cfg *config.Root, call agentcore.ToolCall) agentcore.ToolResult {
	input := schedule.UpdateInput{ID: toolArgString(call.Args, "id")}
	if raw := toolArgString(call.Args, "text"); raw != "" {
		input.Text = &raw
	}
	if raw := toolArgString(call.Args, "run_at"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return agentcore.ToolResult{ToolCallID: call.ID, Content: "run_at must be RFC3339", IsError: true}
		}
		input.RunAt = &parsed
	}
	if raw := toolArgString(call.Args, "interval"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return agentcore.ToolResult{ToolCallID: call.ID, Content: "interval must be a Go duration such as 30m or 24h", IsError: true}
		}
		input.Interval = &parsed
	}
	if raw := toolArgString(call.Args, "status"); raw != "" {
		input.Status = &raw
	}
	task, err := scheduleStore(cfg).Update(input)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	return scheduleToolResult(call.ID, "updated", task)
}

func scheduleActionPause(cfg *config.Root, call agentcore.ToolCall) agentcore.ToolResult {
	task, err := scheduleStore(cfg).Pause(toolArgString(call.Args, "id"))
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	return scheduleToolResult(call.ID, "paused", task)
}

func scheduleActionResume(cfg *config.Root, call agentcore.ToolCall) agentcore.ToolResult {
	task, err := scheduleStore(cfg).Activate(toolArgString(call.Args, "id"))
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	return scheduleToolResult(call.ID, "resumed", task)
}

func scheduleActionDelete(cfg *config.Root, call agentcore.ToolCall) agentcore.ToolResult {
	id := toolArgString(call.Args, "id")
	deleted, err := scheduleStore(cfg).Delete(id)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	if !deleted {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: "schedule not found: " + id, IsError: true, Evidence: map[string]any{"id": id, "deleted": false}}
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: "deleted schedule " + id, Evidence: map[string]any{"id": id, "deleted": true}}
}

func scheduleActionRunNow(cfg *config.Root, call agentcore.ToolCall) agentcore.ToolResult {
	now := time.Now()
	input := schedule.UpdateInput{ID: toolArgString(call.Args, "id"), RunAt: &now}
	status := "active"
	input.Status = &status
	task, err := scheduleStore(cfg).Update(input)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	return scheduleToolResult(call.ID, "scheduled to run now", task)
}

func scheduleStore(cfg *config.Root) schedule.Store {
	store := schedule.Store{Home: config.DefaultHome()}
	if cfg != nil && strings.TrimSpace(cfg.App.Home) != "" {
		store.Home = cfg.App.Home
	}
	return store
}

func scheduleToolResult(callID, action string, task schedule.Task) agentcore.ToolResult {
	return agentcore.ToolResult{
		ToolCallID: callID,
		Content:    fmt.Sprintf("%s schedule %s status=%s run_at=%s", action, task.ID, task.Status, task.RunAt),
		Evidence: map[string]any{
			"id":           task.ID,
			"status":       task.Status,
			"run_at":       task.RunAt,
			"interval":     task.Interval,
			"session_key":  task.SessionKey,
			"require_test": task.RequireTest,
			"runbook_id":   task.RunbookID,
		},
	}
}
