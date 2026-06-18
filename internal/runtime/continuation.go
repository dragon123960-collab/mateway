package runtime

import (
	"strings"

	"github.com/dongping/mateway/internal/session"
)

const (
	ActionNewGraph           = "new_graph"
	ActionContinueGraph      = "continue_graph"
	ActionResumeNode         = "resume_node"
	ActionAnswerPending      = "answer_pending"
	ActionReferenceCompleted = "reference_completed"
	ActionHistoricalSearch   = "historical_search"
)

type ContinuationDecision struct {
	Action      string
	TaskID      string
	GraphID     string
	NodeID      string
	Reason      string
	UserText    string
	ContextRefs []string
}

// determineContinuation makes a deterministic decision about where an
// inbound message should go in the task graph lifecycle.
//
// Priority order:
//  1. Pending action active → answer_pending
//  2. Active task awaiting user input → resume_node or new_graph
//  3. Active task blocked/failed → resume_node (explicit signals only) or new_graph
//  4. Active task running → continue_graph or new_graph
//  5. Completed task reference → reference_completed
//  6. Recent completed task available → new_graph with context refs
//  7. Default → new_graph
//
// This function is a pure state machine; it must not call LLMs, perform I/O,
// or modify state.
//
// Note: In the integration path (Runtime.Handle), pending actions are
// intercepted by handlePending before this function runs. Therefore
// ActionAnswerPending is reachable in unit tests but not in the
// live Runtime.Handle path. The handlePending function records its own
// continuation_decision trace event to cover the pending branch.
func determineContinuation(state session.State, userText string) ContinuationDecision {
	text := strings.TrimSpace(userText)

	if state.Pending != nil && strings.TrimSpace(state.Pending.TaskID) != "" {
		return ContinuationDecision{
			Action:   ActionAnswerPending,
			TaskID:   state.Pending.TaskID,
			Reason:   "pending action requires user input",
			UserText: text,
		}
	}

	activeTask := state.TaskByID(state.ActiveTask)
	if activeTask != nil {
		return continuationForActiveTask(text, *activeTask, state)
	}

	if text != "" {
		if dec := continuationForCompletedOrHistorical(text, state); dec.Action != "" {
			return dec
		}
	}

	return ContinuationDecision{
		Action:   ActionNewGraph,
		Reason:   "no active task or pending action",
		UserText: text,
	}
}

func continuationForActiveTask(text string, task session.TaskNode, state session.State) ContinuationDecision {
	switch task.Status {
	case "await_user_input":
		if isShortConfirmation(text) || looksLikeSameTaskFollowup(text, task) {
			return ContinuationDecision{
				Action:   ActionResumeNode,
				TaskID:   task.ID,
				NodeID:   task.Execution.CurrentNodeID,
				Reason:   "awaiting user input continuation",
				UserText: text,
			}
		}
		if isNewTaskSignal(text) {
			return ContinuationDecision{
				Action:   ActionNewGraph,
				Reason:   "explicit new task signal while awaiting input",
				UserText: text,
			}
		}
		return ContinuationDecision{
			Action:   ActionResumeNode,
			TaskID:   task.ID,
			NodeID:   task.Execution.CurrentNodeID,
			Reason:   "default continuation for awaiting input",
			UserText: text,
		}

	case "failed", "blocked":
		if isNewTaskSignal(text) {
			return ContinuationDecision{
				Action:   ActionNewGraph,
				Reason:   "explicit new task signal while task failed/blocked",
				UserText: text,
			}
		}
		if isResumeSignal(text) {
			return ContinuationDecision{
				Action:   ActionResumeNode,
				TaskID:   task.ID,
				NodeID:   task.Execution.CurrentNodeID,
				Reason:   "resume signal for blocked/failed task",
				UserText: text,
			}
		}
		if looksLikeSameTaskFollowup(text, task) || isShortConfirmation(text) {
			return ContinuationDecision{
				Action:   ActionResumeNode,
				TaskID:   task.ID,
				NodeID:   task.Execution.CurrentNodeID,
				Reason:   "same task followup on blocked/failed task",
				UserText: text,
			}
		}
		return ContinuationDecision{
			Action:   ActionNewGraph,
			Reason:   "no explicit resume signal for failed/blocked task",
			UserText: text,
		}

	default:

		if isShortConfirmation(text) || looksLikeSameTaskFollowup(text, task) {
			return ContinuationDecision{
				Action:   ActionContinueGraph,
				TaskID:   task.ID,
				Reason:   "same task continuation",
				UserText: text,
			}
		}
		if isNewTaskSignal(text) {
			return ContinuationDecision{
				Action:   ActionNewGraph,
				Reason:   "explicit new task signal during active task",
				UserText: text,
			}
		}
		return ContinuationDecision{
			Action:   ActionContinueGraph,
			TaskID:   task.ID,
			Reason:   "default continuation for running task",
			UserText: text,
		}
	}
}

func continuationForCompletedOrHistorical(text string, state session.State) ContinuationDecision {
	lower := strings.ToLower(text)

	completedTask := latestCompletedTask(state)
	if completedTask != nil && looksLikeCompletedReference(lower, *completedTask) {
		return ContinuationDecision{
			Action:      ActionReferenceCompleted,
			ContextRefs: []string{completedTask.ID},
			Reason:      "referencing completed task",
			UserText:    text,
		}
	}

	if completedTask != nil && !isNewTaskSignal(text) {
		return ContinuationDecision{
			Action:      ActionNewGraph,
			ContextRefs: []string{completedTask.ID},
			Reason:      "new graph with recent completed task context",
			UserText:    text,
		}
	}

	return ContinuationDecision{}
}

func latestCompletedTask(state session.State) *session.TaskNode {
	for i := len(state.Tasks) - 1; i >= 0; i-- {
		if state.Tasks[i].Status == "completed" {
			return &state.Tasks[i]
		}
	}
	return nil
}

func isResumeSignal(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, marker := range []string{
		"continue", "继续", "接着", "retry", "重试", "again", "再试",
		"授权", "authorized", "approved", "fixed", "修复", "done",
		"可以", "go ahead", "go on",
	} {
		if lower == marker || strings.HasPrefix(lower, marker+" ") || strings.HasSuffix(lower, " "+marker) {
			return true
		}
		if strings.Contains(lower, " "+marker+" ") {
			return true
		}
	}
	if lower == "ok" || lower == "yes" || lower == "go" {
		return true
	}
	return false
}

func isNewTaskSignal(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return lower == "/new" || strings.HasPrefix(lower, "/new ")
}

func looksLikeCompletedReference(lower string, task session.TaskNode) bool {
	return looksLikeSameTaskFollowup(strings.TrimSpace(lower), task)
}
