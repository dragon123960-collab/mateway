package session

import (
	"fmt"
	"strings"
)

type RecoveryAction string

const (
	RecoveryContinueGraph      RecoveryAction = "continue_graph"
	RecoveryResumeNode         RecoveryAction = "resume_node"
	RecoveryWaitInput          RecoveryAction = "wait_input"
	RecoveryBlocked            RecoveryAction = "blocked"
	RecoveryCompletedReference RecoveryAction = "completed_reference"
	RecoveryNewGraph           RecoveryAction = "new_graph"
)

type RecoveryDecision struct {
	Action  RecoveryAction
	TaskID  string
	GraphID string
	NodeID  string
	Reason  string
}

func DecideGraphRecovery(g *TaskGraph) RecoveryDecision {
	if g == nil || len(g.Nodes) == 0 {
		return RecoveryDecision{
			Action:  RecoveryNewGraph,
			GraphID: g.graphID(),
			TaskID:  g.taskID(),
			Reason:  "empty or nil graph",
		}
	}

	if g.Status == GraphStatusCompleted {
		return RecoveryDecision{
			Action:  RecoveryCompletedReference,
			GraphID: g.ID,
			TaskID:  g.TaskID,
			Reason:  "graph is completed, only usable as context reference",
		}
	}

	if g.Status == GraphStatusFailed {
		blockedNodes := findNodesByStatus(g, NodeStatusBlocked)
		failedNodes := findNodesByStatus(g, NodeStatusFailed)
		if len(blockedNodes) > 0 && len(failedNodes) == 0 {
			return RecoveryDecision{
				Action:  RecoveryBlocked,
				GraphID: g.ID,
				TaskID:  g.TaskID,
				Reason:  fmt.Sprintf("graph has blocked nodes: %s", strings.Join(blockedNodes, ", ")),
			}
		}
		return RecoveryDecision{
			Action:  RecoveryBlocked,
			GraphID: g.ID,
			TaskID:  g.TaskID,
			Reason:  fmt.Sprintf("graph failed with nodes: %s", strings.Join(failedNodes, ", ")),
		}
	}

	if g.Status == GraphStatusBlocked {
		blockedNodes := findNodesByStatus(g, NodeStatusBlocked)
		return RecoveryDecision{
			Action:  RecoveryBlocked,
			GraphID: g.ID,
			TaskID:  g.TaskID,
			Reason:  fmt.Sprintf("graph is blocked: %s", strings.Join(blockedNodes, ", ")),
		}
	}

	for _, n := range g.Nodes {
		if n.Status == NodeStatusAwaitingInput {
			return RecoveryDecision{
				Action:  RecoveryWaitInput,
				GraphID: g.ID,
				TaskID:  g.TaskID,
				NodeID:  n.ID,
				Reason:  fmt.Sprintf("node %s is awaiting input", n.ID),
			}
		}
	}

	readyNodes := findNodesByStatus(g, NodeStatusReady)
	runningNodes := findNodesByStatus(g, NodeStatusRunning)
	pendingNodes := findNodesByStatus(g, NodeStatusPending)

	if len(readyNodes) > 0 || len(runningNodes) > 0 || len(pendingNodes) > 0 {
		var desc []string
		if len(readyNodes) > 0 {
			desc = append(desc, "ready")
		}
		if len(runningNodes) > 0 {
			desc = append(desc, "running")
		}
		if len(pendingNodes) > 0 {
			desc = append(desc, "pending")
		}
		return RecoveryDecision{
			Action:  RecoveryContinueGraph,
			GraphID: g.ID,
			TaskID:  g.TaskID,
			Reason:  fmt.Sprintf("graph has %s nodes", strings.Join(desc, "/")),
		}
	}

	allDone := true
	for _, n := range g.Nodes {
		if n.Status != NodeStatusCompleted && n.Status != NodeStatusSkipped {
			allDone = false
			break
		}
	}
	if allDone {
		return RecoveryDecision{
			Action:  RecoveryContinueGraph,
			GraphID: g.ID,
			TaskID:  g.TaskID,
			Reason:  "all nodes completed, graph ready for finalization",
		}
	}

	return RecoveryDecision{
		Action:  RecoveryContinueGraph,
		GraphID: g.ID,
		TaskID:  g.TaskID,
		Reason:  "graph has remaining work",
	}
}

func RecoverRunningNodes(g *TaskGraph) {
	if g == nil {
		return
	}
	for i := range g.Nodes {
		switch g.Nodes[i].Status {
		case NodeStatusRunning, NodeStatusRetrying:
			if requiresRecoveryConfirmation(&g.Nodes[i]) {
				g.Nodes[i].Status = NodeStatusAwaitingInput
				if strings.TrimSpace(g.Nodes[i].FailureReason) == "" {
					g.Nodes[i].FailureReason = "node was interrupted during a high-risk action and requires confirmation before retry"
				}
				continue
			}
			g.Nodes[i].Status = NodeStatusPending
		case NodeStatusVerifying:
			// Keep verifying — node has produced result/evidence that must be preserved.
			// The verifier will re-evaluate on next runtime tick.
		case NodeStatusCompleted:
			if !g.Nodes[i].Acceptance.Verified {
				g.Nodes[i].Status = NodeStatusVerifying
			}
		case NodeStatusNeedsReplan:
			// Preserve the marker so the runtime can apply the local replan
			// decision instead of retrying the original failed node.
		}
	}
}

func requiresRecoveryConfirmation(n *TaskGraphNode) bool {
	if n == nil {
		return false
	}
	if n.Type == NodeTypeHumanConfirm || n.Type == NodeTypeHumanReview {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(n.Mode), NodeModeHuman) {
		return true
	}
	for _, key := range []string{"risk", "mutation", "human_gate", "requires_human_confirmation"} {
		value, ok := n.Input[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case bool:
			if v {
				return true
			}
		case string:
			text := strings.ToLower(strings.TrimSpace(v))
			switch text {
			case "high", "dangerous", "guarded_mutation", "mutation", "true", "yes", "required", "confirm":
				return true
			}
		}
	}
	return false
}

func findNodesByStatus(g *TaskGraph, status string) []string {
	var ids []string
	for _, n := range g.Nodes {
		if n.Status == status {
			ids = append(ids, n.ID)
		}
	}
	return ids
}

func (g *TaskGraph) graphID() string {
	if g == nil {
		return ""
	}
	return g.ID
}

func (g *TaskGraph) taskID() string {
	if g == nil {
		return ""
	}
	return g.TaskID
}
