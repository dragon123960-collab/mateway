package session

import (
	"fmt"
	"strings"
)

type GraphFinalizeResult struct {
	Status     string // completed | partial | blocked | failed | awaiting_input
	ReplyText  string
	ReplyStyle string // completed | input_required | error | partial
	KeepTask   bool
}

const (
	FinalizeCompleted     = "completed"
	FinalizePartial       = "partial"
	FinalizeBlocked       = "blocked"
	FinalizeFailed        = "failed"
	FinalizeAwaitingInput = "awaiting_input"
)

func FinalizeGraph(g *TaskGraph, vr GraphVerificationResult) GraphFinalizeResult {
	if g == nil || len(g.Nodes) == 0 {
		return GraphFinalizeResult{
			Status:     FinalizeCompleted,
			ReplyText:  "Task completed.",
			ReplyStyle: GraphStatusCompleted,
		}
	}

	switch vr.Status {
	case GraphStatusCompleted:
		return finalizeCompleted(g, vr)
	case GraphStatusAwaitingInput:
		return finalizeAwaitingInput(g, vr)
	case GraphStatusBlocked:
		return finalizeBlocked(g, vr)
	case GraphStatusFailed:
		return finalizeFailed(g, vr)
	case GraphStatusNeedsRepair:
		// The runtime repair loop escalates needs_repair to blocked once the
		// repair cap is reached; reaching the finalizer in this state is a
		// safety net — treat as a concrete blocker.
		return finalizeBlocked(g, vr)
	default:
		return finalizePartial(g, vr)
	}
}

func finalizeCompleted(g *TaskGraph, vr GraphVerificationResult) GraphFinalizeResult {
	var parts []string
	for _, n := range g.Nodes {
		if n.Status != NodeStatusCompleted && n.Status != NodeStatusSkipped {
			continue
		}
		summary := strings.TrimSpace(n.ResultSummary)
		if summary == "" {
			continue
		}
		if n.Acceptance.Criteria != "" && !n.Acceptance.Verified {
			continue
		}
		parts = append(parts, fmt.Sprintf("- [%s] %s", n.Goal, summary))
	}

	var reply string
	if len(parts) > 0 {
		reply = "All steps completed:\n" + strings.Join(parts, "\n")
	} else {
		reply = "Task completed."
	}

	return GraphFinalizeResult{
		Status:     FinalizeCompleted,
		ReplyText:  reply,
		ReplyStyle: GraphStatusCompleted,
	}
}

func finalizeAwaitingInput(g *TaskGraph, vr GraphVerificationResult) GraphFinalizeResult {
	var pendingNodes []string
	for _, n := range g.Nodes {
		if n.Status == NodeStatusAwaitingInput {
			label := n.Goal
			if label == "" {
				label = n.Acceptance.Criteria
			}
			pendingNodes = append(pendingNodes, fmt.Sprintf("%s (%s)", n.ID, label))
		}
	}

	reply := "The following steps require your input:\n" + strings.Join(pendingNodes, "\n")

	return GraphFinalizeResult{
		Status:     FinalizeAwaitingInput,
		ReplyText:  reply,
		ReplyStyle: "input_required",
		KeepTask:   true,
	}
}

func finalizeBlocked(g *TaskGraph, vr GraphVerificationResult) GraphFinalizeResult {
	var blockers []string
	for _, n := range g.Nodes {
		if n.Status == NodeStatusBlocked {
			reason := n.FailureReason
			if reason == "" {
				reason = "blocked"
			}
			blockers = append(blockers, fmt.Sprintf("- %s: %s", n.ID, reason))
		}
	}
	unverified := findUnverifiedCriteriaNodes(g)
	for _, id := range unverified {
		blockers = append(blockers, fmt.Sprintf("- %s: acceptance criteria not verified", id))
	}

	reply := "Task blocked:\n" + strings.Join(blockers, "\n") + "\n\nResolve the blockers before retrying."

	return GraphFinalizeResult{
		Status:     FinalizeBlocked,
		ReplyText:  reply,
		ReplyStyle: "error",
		KeepTask:   true,
	}
}

func finalizeFailed(g *TaskGraph, vr GraphVerificationResult) GraphFinalizeResult {
	var failures []string
	for _, n := range g.Nodes {
		if n.Status == NodeStatusFailed {
			reason := n.FailureReason
			if reason == "" {
				reason = "execution failed"
			}
			failures = append(failures, fmt.Sprintf("- %s: %s", n.ID, reason))
		}
	}
	if len(failures) == 0 {
		for _, id := range vr.MissingNodes {
			if strings.TrimSpace(id) != "" {
				failures = append(failures, fmt.Sprintf("- %s: acceptance or task contract not satisfied", id))
			}
		}
	}
	if len(failures) == 0 && strings.TrimSpace(vr.Reason) != "" {
		failures = append(failures, "- "+vr.Reason)
	}
	if len(failures) == 0 {
		failures = append(failures, "- graph verification failed")
	}

	reply := "Task failed:\n" + strings.Join(failures, "\n")

	return GraphFinalizeResult{
		Status:     FinalizeFailed,
		ReplyText:  reply,
		ReplyStyle: "error",
	}
}

func finalizePartial(g *TaskGraph, vr GraphVerificationResult) GraphFinalizeResult {
	var done []string
	var pending []string
	for _, n := range g.Nodes {
		switch n.Status {
		case NodeStatusCompleted, NodeStatusSkipped:
			if n.ResultSummary != "" {
				done = append(done, fmt.Sprintf("- %s: %s", n.ID, n.ResultSummary))
			}
		case NodeStatusPending, NodeStatusReady, NodeStatusRunning:
			pending = append(pending, n.ID)
		}
	}

	var reply string
	if len(done) > 0 {
		reply = "Partial results:\n" + strings.Join(done, "\n")
		if len(pending) > 0 {
			reply += fmt.Sprintf("\n\nStill running: %s", strings.Join(pending, ", "))
		}
	} else {
		reply = "Task is still running."
	}

	return GraphFinalizeResult{
		Status:     FinalizePartial,
		ReplyText:  reply,
		ReplyStyle: "partial",
		KeepTask:   true,
	}
}
