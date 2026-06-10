package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/session"
)

const (
	tracePhasePlanReview      = "plan_review"
	tracePhaseExecute         = "execute"
	tracePhaseFollowupExecute = "followup_execute"
	tracePhasePendingControl  = "pending_control"
)

type taskContinuity struct {
	Continue   bool
	TaskID     string
	IsFollowup bool
	Reason     string
}

func judgeTaskContinuity(state session.State, userText string) taskContinuity {
	text := strings.TrimSpace(userText)
	if text == "" {
		return taskContinuity{}
	}
	if state.Pending != nil && strings.TrimSpace(state.Pending.TaskID) != "" {
		return taskContinuity{Continue: true, TaskID: state.Pending.TaskID, IsFollowup: true, Reason: "pending control"}
	}
	if state.ActiveTask != "" {
		if task := state.TaskByID(state.ActiveTask); task != nil && session.IsOpenTaskStatus(task.Status) {
			if isShortConfirmation(text) {
				return taskContinuity{Continue: true, TaskID: task.ID, IsFollowup: true, Reason: "active open task"}
			}
			if task.Status == "await_user_input" && looksLikeSameTaskFollowup(text, *task) {
				return taskContinuity{Continue: true, TaskID: task.ID, IsFollowup: true, Reason: "awaiting input followup"}
			}
			if task.Status != "failed" && looksLikeSameTaskFollowup(text, *task) {
				return taskContinuity{Continue: true, TaskID: task.ID, IsFollowup: true, Reason: "same task keywords"}
			}
		}
	}
	task := latestOpenTask(state)
	if task == nil {
		return taskContinuity{}
	}
	if isShortConfirmation(text) {
		return taskContinuity{Continue: true, TaskID: task.ID, IsFollowup: true, Reason: "short confirmation"}
	}
	if looksLikeSameTaskFollowup(text, *task) {
		return taskContinuity{Continue: true, TaskID: task.ID, IsFollowup: true, Reason: "same task keywords"}
	}
	return taskContinuity{}
}

func latestOpenTask(state session.State) *session.TaskNode {
	for i := len(state.Tasks) - 1; i >= 0; i-- {
		if session.IsOpenTaskStatus(state.Tasks[i].Status) {
			return &state.Tasks[i]
		}
	}
	return nil
}

func isShortConfirmation(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "1", "yes", "y", "ok", "okay", "go", "go ahead", "continue", "继续", "好", "可以", "执行":
		return true
	default:
		return false
	}
}

func looksLikeSameTaskFollowup(text string, task session.TaskNode) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	for _, marker := range []string{"/new", "new task", "新任务", "另一个", "另外", "unrelated"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	target := strings.ToLower(task.Goal + " " + task.Summary)
	if task.Execution.Contract != nil {
		target += " " + strings.ToLower(task.Execution.Contract.Summary)
		for _, item := range task.Execution.Contract.PlanItems {
			target += " " + strings.ToLower(item.Title+" "+item.Tool+" "+item.Criteria)
		}
	}
	overlap := 0
	for _, token := range meaningfulTokens(lower) {
		if strings.Contains(target, token) {
			overlap++
		}
	}
	if overlap > 0 {
		return true
	}
	for _, marker := range []string{"继续", "接着", "再", "also", "same", "that", "它", "这个"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func meaningfulTokens(text string) []string {
	words := strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == ',' || r == '.' || r == ':' || r == ';' || r == '\n' || r == '\t'
	})
	stop := map[string]bool{
		"the": true, "a": true, "an": true, "and": true, "or": true, "to": true, "it": true, "this": true,
		"that": true, "please": true, "继续": true, "再": true, "一下": true,
		"list": true, "read": true, "file": true, "files": true, "every": true, "under": true,
	}
	var out []string
	for _, word := range words {
		word = strings.TrimSpace(strings.ToLower(word))
		if len([]rune(word)) < 3 || stop[word] {
			continue
		}
		out = append(out, word)
	}
	return out
}

func shouldPauseForTaskPlan(contract session.TaskContract) bool {
	if contract.RequiresTools || len(contract.RequiredTools) > 0 || len(contract.RequiredEvidence) > 0 {
		return true
	}
	actionItems := 0
	for _, item := range contract.PlanItems {
		if strings.TrimSpace(item.Tool) != "" {
			actionItems++
		}
	}
	return actionItems > 0 || len(contract.PlanItems) > 1
}

func parseTaskPlanConfirmAction(text string) (string, string) {
	switch strings.TrimSpace(text) {
	case "1":
		return "execute", ""
	case "2":
		return "replan", ""
	default:
		return "replan", strings.TrimSpace(text)
	}
}

func (rt Runtime) handleTaskPlanConfirm(ctx context.Context, state *session.State, msg channel.InboundMessage, trace *traceRecorder) (Response, bool, error) {
	pending := state.Pending
	if pending == nil || pending.Kind != session.PendingKindTaskPlanConfirm {
		return Response{}, false, nil
	}
	task := state.TaskByID(pending.TaskID)
	if task == nil {
		state.Pending = nil
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, true, err
		}
		return Response{}, true, fmt.Errorf("task %s not found", pending.TaskID)
	}
	trace.setIdentity(map[string]any{"task_id": task.ID, "control_text": msg.Text, "effective_task_goal": task.Goal})
	state.AddTraceRef(task.ID, session.TraceRef{TraceID: trace.id, TracePath: trace.path, Phase: tracePhasePendingControl, MessageID: msg.ID})
	action, feedback := parseTaskPlanConfirmAction(msg.Text)
	switch action {
	case "execute":
		state.Pending = nil
		state.ActiveTask = task.ID
		state.AddTraceRef(task.ID, session.TraceRef{TraceID: trace.id, TracePath: trace.path, Phase: tracePhaseExecute, MessageID: msg.ID})
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, true, err
		}
		_ = trace.write(map[string]any{"type": "request", "text": msg.Text, "control_text": msg.Text, "effective_task_goal": task.Goal})
		_ = trace.write(map[string]any{"type": "task_plan_confirmed", "task_id": task.ID, "control_text": msg.Text, "effective_task_goal": task.Goal})
		resp, err := rt.runTask(ctx, msg, state, task, task.Goal, tracePhaseExecute, trace)
		return resp, true, err
	case "replan":
		if pending.ReplanCount >= 5 {
			text := "Replan limit reached. Reply 1 to execute the current plan or /new to start over."
			if prefersChinese(msg.Text, task.Goal) {
				text = "已达到重新规划次数上限。回复 1 执行当前计划，或发送 /new 开始新任务。"
			}
			resp := rt.reply(msg, text, channel.StyleInputRequired)
			resp.TraceID = trace.id
			resp.TracePath = trace.path
			return resp, true, nil
		}
		pending.ReplanCount++
		pending.Feedback = feedback
		if task.Execution.Contract != nil {
			task.Execution.Contract = nil
		}
		agent := rt.Pool.AgentForMessage(msg)
		if agent == nil {
			agent = agentcore.NewAgent(rt.Model, rt.Tools)
		}
		userText := task.Goal
		if feedback != "" {
			userText = strings.TrimSpace(task.Goal + "\nPlan feedback: " + feedback)
		}
		contract := rt.ensureTaskContract(ctx, msg, state, task, userText, agent.Model, trace)
		state.Pending = pending
		state.AddTraceRef(task.ID, session.TraceRef{TraceID: trace.id, TracePath: trace.path, Phase: tracePhasePlanReview, MessageID: msg.ID})
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, true, err
		}
		_ = trace.write(map[string]any{"type": "task_plan_replanned", "task_id": task.ID, "feedback": feedback, "replan_count": pending.ReplanCount})
		return Response{Reply: channel.OutboundMessage{
			Channel:  msg.Channel,
			ThreadID: msg.ThreadID,
			Text:     renderTaskPlanForReview(contract, firstNonEmpty(feedback, task.Goal, msg.Text)),
			Style:    channel.StyleInputRequired,
		}, TraceID: trace.id, TracePath: trace.path}, true, nil
	default:
		return Response{}, true, nil
	}
}

func updatePlanItemsForToolResult(state *session.State, taskID string, toolName string, status string, evidence string) {
	task := state.TaskByID(taskID)
	if task == nil || task.Execution.Contract == nil {
		return
	}
	contract := *task.Execution.Contract
	item := planItemForTool(&contract, toolName, "running")
	if item == nil {
		item = planItemForTool(&contract, toolName, "pending")
	}
	if item == nil {
		return
	}
	next := "completed"
	if status == "failed" || status == "suspect" || status == "blocked" {
		next = "blocked"
	}
	item.Status = next
	item.Evidence = summarize(evidence)
	item.UpdatedAt = time.Now()
	state.SetTaskContract(taskID, contract)
}

func taskHasTracePhase(task *session.TaskNode, phase string) bool {
	if task == nil {
		return false
	}
	for _, ref := range task.Execution.TraceRefs {
		if ref.Phase == phase {
			return true
		}
	}
	return false
}

func markPlanItemRunning(state *session.State, taskID string, toolName string) {
	task := state.TaskByID(taskID)
	if task == nil || task.Execution.Contract == nil {
		return
	}
	contract := *task.Execution.Contract
	item := planItemForTool(&contract, toolName, "pending")
	if item == nil {
		return
	}
	item.Status = "running"
	item.UpdatedAt = time.Now()
	state.SetTaskContract(taskID, contract)
}

func completeNoToolPlanItems(state *session.State, taskID string, evidence string) {
	task := state.TaskByID(taskID)
	if task == nil || task.Execution.Contract == nil {
		return
	}
	contract := *task.Execution.Contract
	changed := false
	for i := range contract.PlanItems {
		item := &contract.PlanItems[i]
		if strings.TrimSpace(item.Tool) != "" {
			continue
		}
		switch normalizePlanStatus(item.Status) {
		case "", "pending", "running":
			item.Status = "completed"
			item.Evidence = summarize(evidence)
			item.UpdatedAt = time.Now()
			changed = true
		}
	}
	if changed {
		state.SetTaskContract(taskID, contract)
	}
}

func planItemForTool(contract *session.TaskContract, toolName string, statuses ...string) *session.TaskPlanItem {
	if contract == nil {
		return nil
	}
	wantTool := strings.TrimSpace(toolName)
	if wantTool == "" {
		return nil
	}
	allowed := map[string]bool{}
	for _, status := range statuses {
		allowed[normalizePlanStatus(status)] = true
	}
	for i := range contract.PlanItems {
		item := &contract.PlanItems[i]
		if strings.TrimSpace(item.Tool) == "" || !strings.EqualFold(strings.TrimSpace(item.Tool), wantTool) {
			continue
		}
		if len(allowed) == 0 || allowed[normalizePlanStatus(item.Status)] {
			return item
		}
	}
	return nil
}
