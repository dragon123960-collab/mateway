package session

import (
	"strings"
	"testing"
)

func TestDecideGraphRecovery_NilGraph(t *testing.T) {
	d := DecideGraphRecovery(nil)
	if d.Action != RecoveryNewGraph {
		t.Fatalf("expected new_graph for nil, got %q", d.Action)
	}
}

func TestDecideGraphRecovery_EmptyGraph(t *testing.T) {
	d := DecideGraphRecovery(&TaskGraph{ID: "g1", TaskID: "t1", Nodes: []TaskGraphNode{}})
	if d.Action != RecoveryNewGraph {
		t.Fatalf("expected new_graph for empty, got %q", d.Action)
	}
}

func TestDecideGraphRecovery_CompletedGraph(t *testing.T) {
	g := &TaskGraph{
		ID: "g1", TaskID: "t1", Status: GraphStatusCompleted,
		Nodes: []TaskGraphNode{{ID: "n1", Type: NodeTypeModel, Goal: "x", Status: NodeStatusCompleted}},
	}
	d := DecideGraphRecovery(g)
	if d.Action != RecoveryCompletedReference {
		t.Fatalf("expected completed_reference, got %q: %s", d.Action, d.Reason)
	}
}

func TestDecideGraphRecovery_AwaitingInput(t *testing.T) {
	g := &TaskGraph{
		ID: "g1", TaskID: "t1", Status: GraphStatusAwaitingInput,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "x", Status: NodeStatusCompleted},
			{ID: "review", Type: NodeTypeHumanReview, Goal: "review", Status: NodeStatusAwaitingInput},
		},
	}
	d := DecideGraphRecovery(g)
	if d.Action != RecoveryWaitInput {
		t.Fatalf("expected wait_input, got %q", d.Action)
	}
	if d.NodeID != "review" {
		t.Fatalf("expected nodeID=review, got %q", d.NodeID)
	}
}

func TestDecideGraphRecovery_HasReadyNodes(t *testing.T) {
	g := &TaskGraph{
		ID: "g1", TaskID: "t1", Status: GraphStatusRunning,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "x", Status: NodeStatusReady},
			{ID: "n2", Type: NodeTypeModel, Goal: "y", Status: NodeStatusPending},
		},
	}
	d := DecideGraphRecovery(g)
	if d.Action != RecoveryContinueGraph {
		t.Fatalf("expected continue_graph, got %q", d.Action)
	}
	if !strings.Contains(d.Reason, "ready") {
		t.Fatalf("reason should mention ready, got %q", d.Reason)
	}
}

func TestDecideGraphRecovery_HasPendingNodes(t *testing.T) {
	g := &TaskGraph{
		ID: "g1", TaskID: "t1", Status: GraphStatusRunning,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeTool, Goal: "read", Status: NodeStatusCompleted, Executor: "file.read", ResultSummary: "ok", EvidenceRefs: []EvidenceRef{{Kind: "tool"}}},
			{ID: "n2", Type: NodeTypeModel, Goal: "analyze", Status: NodeStatusPending, Depends: []string{"n1"}},
		},
	}
	d := DecideGraphRecovery(g)
	if d.Action != RecoveryContinueGraph {
		t.Fatalf("expected continue_graph for pending, got %q", d.Action)
	}
}

func TestDecideGraphRecovery_AllCompleted(t *testing.T) {
	g := &TaskGraph{
		ID: "g1", TaskID: "t1", Status: GraphStatusRunning,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "x", Status: NodeStatusCompleted, ResultSummary: "done"},
		},
	}
	d := DecideGraphRecovery(g)
	if d.Action != RecoveryContinueGraph {
		t.Fatalf("expected continue_graph for all-completed, got %q: %s", d.Action, d.Reason)
	}
}

func TestDecideGraphRecovery_BlockedNodes(t *testing.T) {
	g := &TaskGraph{
		ID: "g1", TaskID: "t1", Status: GraphStatusBlocked,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeTool, Goal: "read", Status: NodeStatusBlocked, Executor: "file.read", FailureReason: "permission denied"},
		},
	}
	d := DecideGraphRecovery(g)
	if d.Action != RecoveryBlocked {
		t.Fatalf("expected blocked, got %q", d.Action)
	}
	if !strings.Contains(d.Reason, "n1") {
		t.Fatalf("reason should mention blocked node, got %q", d.Reason)
	}
}

func TestDecideGraphRecovery_FailedGraph(t *testing.T) {
	g := &TaskGraph{
		ID: "g1", TaskID: "t1", Status: GraphStatusFailed,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeTool, Goal: "run", Status: NodeStatusFailed, Executor: "terminal.run", FailureReason: "command not found"},
		},
	}
	d := DecideGraphRecovery(g)
	if d.Action != RecoveryBlocked {
		t.Fatalf("expected blocked for failed graph, got %q: %s", d.Action, d.Reason)
	}
}

func TestRecoverRunningNodes(t *testing.T) {
	g := &TaskGraph{
		ID:     "g1",
		TaskID: "t1",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeTool, Goal: "read", Status: NodeStatusRunning, Executor: "file.read"},
			{ID: "n2", Type: NodeTypeModel, Goal: "analyze", Status: NodeStatusPending},
			{ID: "n3", Type: NodeTypeModel, Goal: "done", Status: NodeStatusCompleted},
		},
	}
	RecoverRunningNodes(g)
	if g.Nodes[0].Status != NodeStatusPending {
		t.Fatalf("running node should be reset to pending, got %q", g.Nodes[0].Status)
	}
	if g.Nodes[1].Status != NodeStatusPending {
		t.Fatalf("pending node should remain pending, got %q", g.Nodes[1].Status)
	}
	if g.Nodes[2].Status != NodeStatusCompleted {
		t.Fatalf("completed node should remain completed, got %q", g.Nodes[2].Status)
	}
}

func TestRecoverRunningNodes_NilGraph(t *testing.T) {
	RecoverRunningNodes(nil)
}

func TestDecideGraphRecovery_HasRunningNodes(t *testing.T) {
	g := &TaskGraph{
		ID: "g1", TaskID: "t1", Status: GraphStatusRunning,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "x", Status: NodeStatusRunning},
		},
	}
	d := DecideGraphRecovery(g)
	if d.Action != RecoveryContinueGraph {
		t.Fatalf("running nodes should continue graph, got %q", d.Action)
	}
}

func TestDecideGraphRecovery_AllSkipped(t *testing.T) {
	g := &TaskGraph{
		ID: "g1", TaskID: "t1", Status: GraphStatusRunning,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "x", Status: NodeStatusSkipped},
			{ID: "n2", Type: NodeTypeModel, Goal: "y", Status: NodeStatusSkipped},
		},
	}
	d := DecideGraphRecovery(g)
	if d.Action != RecoveryContinueGraph {
		t.Fatalf("all skipped should continue, got %q", d.Action)
	}
}

func TestDecideGraphRecovery_AwaitingInputBeatsPending(t *testing.T) {
	g := &TaskGraph{
		ID: "g1", TaskID: "t1", Status: GraphStatusAwaitingInput,
		Nodes: []TaskGraphNode{
			{ID: "pending", Type: NodeTypeModel, Goal: "x", Status: NodeStatusPending},
			{ID: "review", Type: NodeTypeHumanReview, Goal: "review", Status: NodeStatusAwaitingInput},
		},
	}
	d := DecideGraphRecovery(g)
	if d.Action != RecoveryWaitInput {
		t.Fatalf("awaiting input beats pending, got %q", d.Action)
	}
	if d.NodeID != "review" {
		t.Fatalf("should return awaiting node ID, got %q", d.NodeID)
	}
}
