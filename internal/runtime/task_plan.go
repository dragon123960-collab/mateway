package runtime

import (
	"strings"
	"time"

	"github.com/dongping/mateway/internal/session"
)

const (
	tracePhaseExecute         = "execute"
	tracePhaseFollowupExecute = "followup_execute"
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
	case "1", "2", "3", "4":
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
	if isNewTaskSignal(lower) {
		return false
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
	return false
}

func meaningfulTokens(text string) []string {
	words := strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == ',' || r == '.' || r == ':' || r == ';' || r == '\n' || r == '\t' || r == '/' || r == '-' || r == '_'
	})
	stop := map[string]bool{
		"the": true, "a": true, "an": true, "and": true, "or": true, "to": true, "it": true, "this": true,
		"that": true, "please": true,
		"list": true, "read": true, "file": true, "files": true, "every": true, "under": true,
		"continue": true, "resume": true, "retry": true,
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

func updatePlanItemsForToolResult(state *session.State, taskID string, toolName string, status string, evidence string) {
	task := state.TaskByID(taskID)
	if task == nil || task.Execution.Contract == nil {
		return
	}
	contract := *task.Execution.Contract
	item := planItemForTool(&contract, toolName, "running", "pending")
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

func updatePlanItemForFileReadPath(state *session.State, taskID string, path string, status string, evidence string) {
	task := state.TaskByID(taskID)
	if task == nil || task.Execution.Contract == nil {
		return
	}
	contract := *task.Execution.Contract
	item := findFileReadPlanItemForPath(&contract, path)
	if item == nil {
		item = planItemForTool(&contract, "file.read", "running", "pending")
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

func findFileReadPlanItemForPath(contract *session.TaskContract, path string) *session.TaskPlanItem {
	if contract == nil || path == "" {
		return nil
	}
	lookFor := strings.ToLower(strings.TrimSpace(path))
	if lookFor == "" {
		return nil
	}
	for i := range contract.PlanItems {
		item := &contract.PlanItems[i]
		if !strings.EqualFold(strings.TrimSpace(item.Tool), "file.read") {
			continue
		}
		criteria := strings.ToLower(strings.TrimSpace(item.Criteria))
		title := strings.ToLower(strings.TrimSpace(item.Title))
		if criteria != "" && (strings.Contains(criteria, lookFor) || strings.Contains(lookFor, criteria)) {
			return item
		}
		if title != "" && (strings.Contains(title, lookFor) || strings.Contains(lookFor, title)) {
			return item
		}
	}
	for _, skill := range contract.RequiredSkills {
		skillPath := strings.ToLower(strings.TrimSpace(skill.Path))
		if skillPath != "" && skillPath == lookFor {
			for i := range contract.PlanItems {
				item := &contract.PlanItems[i]
				if fileReadPlanItemMatchesSkill(*item, skill.Name, skill.Path) {
					return item
				}
			}
		}
	}
	return nil
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
	if len(statuses) > 0 {
		// Search in priority order: first status has highest priority.
		for _, status := range statuses {
			allowed := normalizePlanStatus(status)
			for i := range contract.PlanItems {
				item := &contract.PlanItems[i]
				if !strings.EqualFold(strings.TrimSpace(item.Tool), wantTool) {
					continue
				}
				if normalizePlanStatus(item.Status) == allowed {
					return item
				}
			}
		}
		// Fallback: allow retry of a blocked item when "blocked" is not explicitly asked for.
		if !containsStatus(statuses, "blocked") {
			for i := range contract.PlanItems {
				item := &contract.PlanItems[i]
				if !strings.EqualFold(strings.TrimSpace(item.Tool), wantTool) {
					continue
				}
				if normalizePlanStatus(item.Status) == "blocked" {
					return item
				}
			}
		}
		return nil
	}
	// No status filter: return first matching item.
	for i := range contract.PlanItems {
		item := &contract.PlanItems[i]
		if strings.TrimSpace(item.Tool) == "" || !strings.EqualFold(strings.TrimSpace(item.Tool), wantTool) {
			continue
		}
		return item
	}
	return nil
}

func containsStatus(statuses []string, target string) bool {
	for _, s := range statuses {
		if normalizePlanStatus(s) == target {
			return true
		}
	}
	return false
}
