package runtime

import (
	"strings"

	"github.com/dongping/mateway/internal/session"
)

func graphAwareContinuation(state session.State, userText string, task *session.TaskNode) ContinuationDecision {
	base := determineContinuation(state, userText)

	if task == nil || task.Graph == nil || len(task.Graph.Nodes) == 0 {
		return base
	}

	recovery := session.DecideGraphRecovery(task.Graph)

	switch recovery.Action {
	case session.RecoveryCompletedReference:
		return ContinuationDecision{
			Action:      ActionReferenceCompleted,
			TaskID:      recovery.TaskID,
			GraphID:     recovery.GraphID,
			NodeID:      recovery.NodeID,
			Reason:      recovery.Reason,
			UserText:    base.UserText,
			ContextRefs: base.ContextRefs,
		}
	case session.RecoveryWaitInput:
		if base.Action == ActionNewGraph || base.Action == ActionHistoricalSearch || base.Action == ActionReferenceCompleted {
			return base
		}
		return ContinuationDecision{
			Action:   ActionResumeNode,
			TaskID:   recovery.TaskID,
			GraphID:  recovery.GraphID,
			NodeID:   recovery.NodeID,
			Reason:   recovery.Reason,
			UserText: base.UserText,
		}
	case session.RecoveryBlocked:
		if base.Action == ActionResumeNode && base.NodeID != "" {
			return base
		}
		if base.Action == ActionNewGraph || base.Action == ActionHistoricalSearch || base.Action == ActionReferenceCompleted {
			return base
		}
		return ContinuationDecision{
			Action:   ActionContinueGraph,
			TaskID:   recovery.TaskID,
			GraphID:  recovery.GraphID,
			Reason:   recovery.Reason,
			UserText: base.UserText,
		}
	case session.RecoveryContinueGraph:
		if base.Action == ActionNewGraph || base.Action == ActionHistoricalSearch || base.Action == ActionReferenceCompleted {
			return base
		}
		return ContinuationDecision{
			Action:   ActionContinueGraph,
			TaskID:   recovery.TaskID,
			GraphID:  recovery.GraphID,
			NodeID:   recovery.NodeID,
			Reason:   recovery.Reason,
			UserText: base.UserText,
		}
	default:
		return base
	}
}

func buildGraphContinuation(task *session.TaskNode, userText string) ContinuationDecision {
	if task == nil || task.Graph == nil || len(task.Graph.Nodes) == 0 {
		return ContinuationDecision{
			Action:   ActionNewGraph,
			Reason:   "no graph on task",
			UserText: strings.TrimSpace(userText),
		}
	}

	recovery := session.DecideGraphRecovery(task.Graph)

	action := ActionContinueGraph
	switch recovery.Action {
	case session.RecoveryWaitInput:
		action = ActionResumeNode
	case session.RecoveryCompletedReference:
		action = ActionReferenceCompleted
	case session.RecoveryBlocked:
		action = ActionContinueGraph
	case session.RecoveryNewGraph:
		action = ActionNewGraph
	}

	return ContinuationDecision{
		Action:   action,
		TaskID:   recovery.TaskID,
		GraphID:  recovery.GraphID,
		NodeID:   recovery.NodeID,
		Reason:   recovery.Reason,
		UserText: strings.TrimSpace(userText),
	}
}
