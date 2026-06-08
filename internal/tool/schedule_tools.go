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

func (ScheduleCreateTool) Name() string        { return "schedule.create" }
func (ScheduleCreateTool) Description() string { return "create a local scheduled task" }
func (ScheduleCreateTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"text", "run_at"}}
}
func (ScheduleCreateTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when the user asks to run a task later. Scheduled tasks are channel-neutral and are created directly without chat approval.",
		WhenNotToUse:         "Do not use for immediate tasks; execute those directly.",
		OutputContract:       "Return scheduled task id, status, run time, and interval when any.",
		Evidence:             "Return id, status, run_at, interval, session_key.",
		Acceptance:           "Accepted when the task is persisted under the local schedule store.",
		SoftFailureSignals:   []string{"invalid run_at", "missing text"},
		ParallelMode:         "forbid",
		ReusePolicy:          "never",
		ConfirmationBoundary: "guarded mutation; destructive schedule operations are enforced by tool policy, not chat approval.",
	}
}
func (ScheduleCreateTool) Risk() agentcore.Risk { return agentcore.RiskGuardedMutation }
func (t ScheduleCreateTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
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
	requireTest := false
	if raw := strings.ToLower(toolArgString(call.Args, "require_test")); raw == "false" || raw == "no" {
		requireTest = false
	} else if raw == "true" || raw == "yes" {
		requireTest = true
	}
	store := schedule.Store{Home: config.DefaultHome()}
	if t.Config != nil && strings.TrimSpace(t.Config.App.Home) != "" {
		store.Home = t.Config.App.Home
	}
	task, err := store.Create(schedule.CreateInput{
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
			"id":          task.ID,
			"status":      task.Status,
			"run_at":      task.RunAt,
			"interval":    task.Interval,
			"session_key": task.SessionKey,
		},
	}
}

func (ScheduleListTool) Name() string        { return "schedule.list" }
func (ScheduleListTool) Description() string { return "list local scheduled tasks" }
func (ScheduleListTool) Schema() agentcore.Schema {
	return agentcore.Schema{}
}
func (ScheduleListTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when the user asks what scheduled tasks exist.",
		WhenNotToUse:         "Do not use for creating a new scheduled task.",
		OutputContract:       "Return one line per scheduled task.",
		Evidence:             "Return scheduled task count.",
		Acceptance:           "Accepted when the schedule store is read successfully.",
		ParallelMode:         "read_only_ok",
		ReusePolicy:          "stable_read",
		ConfirmationBoundary: "safe read; no confirmation.",
	}
}
func (ScheduleListTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (t ScheduleListTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	store := schedule.Store{Home: config.DefaultHome()}
	if t.Config != nil && strings.TrimSpace(t.Config.App.Home) != "" {
		store.Home = t.Config.App.Home
	}
	tasks, err := store.List()
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	var lines []string
	for _, task := range tasks {
		lines = append(lines, fmt.Sprintf("%s status=%s run_at=%s interval=%s last=%s text=%s", task.ID, task.Status, task.RunAt, task.Interval, task.LastRunStatus, summarizeToolText(task.Text, 80)))
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: strings.Join(lines, "\n"), Evidence: map[string]any{"count": len(tasks)}}
}

func (ScheduleUpdateTool) Name() string        { return "schedule.update" }
func (ScheduleUpdateTool) Description() string { return "update a local scheduled task" }
func (ScheduleUpdateTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"id"}}
}
func (ScheduleUpdateTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when the user asks to change an existing scheduled task's text, run time, interval, or status.",
		WhenNotToUse:         "Do not use to create a new schedule or to run a task immediately.",
		OutputContract:       "Return updated schedule id, status, run time, interval, and session key evidence.",
		Evidence:             "Return id, status, run_at, interval, and session_key.",
		Acceptance:           "Accepted when the schedule store updates the requested task.",
		SoftFailureSignals:   []string{"schedule not found", "run_at must be RFC3339", "interval must be a Go duration"},
		ParallelMode:         "forbid",
		ReusePolicy:          "never",
		ConfirmationBoundary: "guarded mutation; schedule changes are direct and do not create chat approval pending.",
	}
}
func (ScheduleUpdateTool) Risk() agentcore.Risk { return agentcore.RiskGuardedMutation }
func (t ScheduleUpdateTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
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
	task, err := scheduleStore(t.Config).Update(input)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	return scheduleToolResult(call.ID, "updated", task)
}

func (SchedulePauseTool) Name() string        { return "schedule.pause" }
func (SchedulePauseTool) Description() string { return "pause a local scheduled task" }
func (SchedulePauseTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"id"}}
}
func (SchedulePauseTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when the user asks to temporarily stop an existing scheduled task.",
		WhenNotToUse:         "Do not use to permanently remove a schedule; use schedule.delete only when deletion is explicitly requested.",
		OutputContract:       "Return paused schedule id, status, run time, interval, and session key evidence.",
		Evidence:             "Return id, status, run_at, interval, and session_key.",
		Acceptance:           "Accepted when the target schedule status becomes paused.",
		SoftFailureSignals:   []string{"schedule not found"},
		ParallelMode:         "forbid",
		ReusePolicy:          "never",
		ConfirmationBoundary: "guarded mutation; pause is reversible and does not create chat approval pending.",
	}
}
func (SchedulePauseTool) Risk() agentcore.Risk { return agentcore.RiskGuardedMutation }
func (t SchedulePauseTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	task, err := scheduleStore(t.Config).Pause(toolArgString(call.Args, "id"))
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	return scheduleToolResult(call.ID, "paused", task)
}

func (ScheduleResumeTool) Name() string        { return "schedule.resume" }
func (ScheduleResumeTool) Description() string { return "resume a local scheduled task" }
func (ScheduleResumeTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"id"}}
}
func (ScheduleResumeTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when the user asks to reactivate a paused or inactive scheduled task.",
		WhenNotToUse:         "Do not use to create a new schedule or change its timing unless the user asked for that too.",
		OutputContract:       "Return resumed schedule id, status, run time, interval, and session key evidence.",
		Evidence:             "Return id, status, run_at, interval, and session_key.",
		Acceptance:           "Accepted when the target schedule status becomes active.",
		SoftFailureSignals:   []string{"schedule not found"},
		ParallelMode:         "forbid",
		ReusePolicy:          "never",
		ConfirmationBoundary: "guarded mutation; resume is direct and does not create chat approval pending.",
	}
}
func (ScheduleResumeTool) Risk() agentcore.Risk { return agentcore.RiskGuardedMutation }
func (t ScheduleResumeTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	task, err := scheduleStore(t.Config).Activate(toolArgString(call.Args, "id"))
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	return scheduleToolResult(call.ID, "resumed", task)
}

func (ScheduleDeleteTool) Name() string        { return "schedule.delete" }
func (ScheduleDeleteTool) Description() string { return "delete a local scheduled task" }
func (ScheduleDeleteTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"id"}}
}
func (ScheduleDeleteTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use only when the user explicitly asks to delete an existing scheduled task.",
		WhenNotToUse:         "Do not use for temporary stopping; use schedule.pause when the user asks to pause.",
		OutputContract:       "Return deleted schedule id and deleted=true evidence.",
		Evidence:             "Return id and deleted boolean.",
		Acceptance:           "Accepted when the schedule store deletes the requested task.",
		SoftFailureSignals:   []string{"schedule not found"},
		ParallelMode:         "forbid",
		ReusePolicy:          "never",
		ConfirmationBoundary: "dangerous mutation; deletion is handled by hard tool boundaries, not chat approval pending.",
	}
}
func (ScheduleDeleteTool) Risk() agentcore.Risk { return agentcore.RiskDangerous }
func (t ScheduleDeleteTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	id := toolArgString(call.Args, "id")
	deleted, err := scheduleStore(t.Config).Delete(id)
	if err != nil {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: err.Error(), IsError: true}
	}
	if !deleted {
		return agentcore.ToolResult{ToolCallID: call.ID, Content: "schedule not found: " + id, IsError: true, Evidence: map[string]any{"id": id, "deleted": false}}
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: "deleted schedule " + id, Evidence: map[string]any{"id": id, "deleted": true}}
}

func (ScheduleRunNowTool) Name() string        { return "schedule.run_now" }
func (ScheduleRunNowTool) Description() string { return "mark a local scheduled task due now" }
func (ScheduleRunNowTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: []string{"id"}}
}
func (ScheduleRunNowTool) ToolContract() agentcore.ToolContract {
	return agentcore.ToolContract{
		WhenToUse:            "Use when the user asks to trigger an existing scheduled task immediately.",
		WhenNotToUse:         "Do not use for ordinary immediate tasks that should be handled directly in the current conversation.",
		OutputContract:       "Return schedule id, active status, updated run time, interval, and session key evidence.",
		Evidence:             "Return id, status, run_at, interval, and session_key.",
		Acceptance:           "Accepted when the schedule is marked due now.",
		SoftFailureSignals:   []string{"schedule not found"},
		ParallelMode:         "forbid",
		ReusePolicy:          "never",
		ConfirmationBoundary: "guarded mutation; run-now only changes scheduler state and does not create chat approval pending.",
	}
}
func (ScheduleRunNowTool) Risk() agentcore.Risk { return agentcore.RiskGuardedMutation }
func (t ScheduleRunNowTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	now := time.Now()
	input := schedule.UpdateInput{ID: toolArgString(call.Args, "id"), RunAt: &now}
	status := "active"
	input.Status = &status
	task, err := scheduleStore(t.Config).Update(input)
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
			"id":          task.ID,
			"status":      task.Status,
			"run_at":      task.RunAt,
			"interval":    task.Interval,
			"session_key": task.SessionKey,
		},
	}
}
