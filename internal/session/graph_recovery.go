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
		if g.Nodes[i].Status == NodeStatusRunning {
			g.Nodes[i].Status = NodeStatusPending
		}
	}
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
