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
			{ID: "n3", Type: NodeTypeModel, Goal: "done", Status: NodeStatusCompleted, Acceptance: Acceptance{Verified: true}},
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

func TestRecoverRunningNodes_VerifyingStaysVerifying(t *testing.T) {
	g := &TaskGraph{
		ID:     "g1",
		TaskID: "t1",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "work", Status: NodeStatusVerifying, ResultSummary: "result", EvidenceRefs: []EvidenceRef{{Kind: "tool", ToolName: "bash"}}},
		},
	}
	RecoverRunningNodes(g)
	if g.Nodes[0].Status != NodeStatusVerifying {
		t.Fatalf("verifying node should stay verifying, got %q", g.Nodes[0].Status)
	}
	if g.Nodes[0].ResultSummary != "result" {
		t.Fatal("verifying node should preserve result_summary")
	}
	if len(g.Nodes[0].EvidenceRefs) != 1 {
		t.Fatal("verifying node should preserve evidence_refs")
	}
}

func TestRecoverRunningNodes_RetryingBecomesPending(t *testing.T) {
	g := &TaskGraph{
		ID:     "g1",
		TaskID: "t1",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "retry", Status: NodeStatusRetrying, Attempts: 2},
		},
	}
	RecoverRunningNodes(g)
	if g.Nodes[0].Status != NodeStatusPending {
		t.Fatalf("retrying node should become pending, got %q", g.Nodes[0].Status)
	}
	if g.Nodes[0].Attempts != 2 {
		t.Fatalf("attempts should be preserved, got %d", g.Nodes[0].Attempts)
	}
}

func TestRecoverRunningNodes_CompletedUnverifiedBecomesVerifying(t *testing.T) {
	g := &TaskGraph{
		ID:     "g1",
		TaskID: "t1",
		Nodes: []TaskGraphNode{
			{
				ID:         "n1",
				Type:       NodeTypeModel,
				Goal:       "done but not verified",
				Status:     NodeStatusCompleted,
				Acceptance: Acceptance{Criteria: "must pass", Verified: false},
			},
		},
	}
	RecoverRunningNodes(g)
	if g.Nodes[0].Status != NodeStatusVerifying {
		t.Fatalf("completed unverified should become verifying, got %q", g.Nodes[0].Status)
	}
}

func TestRecoverRunningNodes_CompletedVerifiedStaysCompleted(t *testing.T) {
	g := &TaskGraph{
		ID:     "g1",
		TaskID: "t1",
		Nodes: []TaskGraphNode{
			{
				ID:         "n1",
				Type:       NodeTypeModel,
				Goal:       "properly done",
				Status:     NodeStatusCompleted,
				Acceptance: Acceptance{Criteria: "done", Verified: true},
				Attempts:   1,
			},
		},
	}
	RecoverRunningNodes(g)
	if g.Nodes[0].Status != NodeStatusCompleted {
		t.Fatalf("completed verified should stay completed, got %q", g.Nodes[0].Status)
	}
	if g.Nodes[0].Attempts != 1 {
		t.Fatalf("attempts should be preserved")
	}
}

func TestRecoverRunningNodes_AwaitingInputPreserved(t *testing.T) {
	g := &TaskGraph{
		ID:     "g1",
		TaskID: "t1",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeHumanReview, Goal: "waiting", Status: NodeStatusAwaitingInput},
		},
	}
	RecoverRunningNodes(g)
	if g.Nodes[0].Status != NodeStatusAwaitingInput {
		t.Fatalf("awaiting_input should be preserved, got %q", g.Nodes[0].Status)
	}
}

func TestRecoverRunningNodes_FailedBlockedNotChanged(t *testing.T) {
	g := &TaskGraph{
		ID:     "g1",
		TaskID: "t1",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeTool, Goal: "fail", Executor: "x", Status: NodeStatusFailed, FailureReason: "boom"},
			{ID: "n2", Type: NodeTypeTool, Goal: "blocked", Executor: "x", Status: NodeStatusBlocked, FailureReason: "waiting"},
		},
	}
	RecoverRunningNodes(g)
	if g.Nodes[0].Status != NodeStatusFailed || g.Nodes[0].FailureReason != "boom" {
		t.Fatalf("failed should stay failed, got %q/%q", g.Nodes[0].Status, g.Nodes[0].FailureReason)
	}
	if g.Nodes[1].Status != NodeStatusBlocked || g.Nodes[1].FailureReason != "waiting" {
		t.Fatalf("blocked should stay blocked, got %q/%q", g.Nodes[1].Status, g.Nodes[1].FailureReason)
	}
}

func TestRecoverRunningNodes_HighRiskMutationRequiresConfirmation(t *testing.T) {
	g := &TaskGraph{
		ID:     "g1",
		TaskID: "t1",
		Nodes: []TaskGraphNode{
			{
				ID:     "deploy",
				Type:   NodeTypeTool,
				Mode:   NodeModeTool,
				Goal:   "deploy",
				Status: NodeStatusRunning,
				Input: map[string]any{
					"risk":     "high",
					"mutation": true,
				},
			},
		},
	}
	RecoverRunningNodes(g)
	if g.Nodes[0].Status != NodeStatusAwaitingInput {
		t.Fatalf("high-risk interrupted node should await input, got %q", g.Nodes[0].Status)
	}
	if !strings.Contains(g.Nodes[0].FailureReason, "requires confirmation") {
		t.Fatalf("failure reason should explain confirmation, got %q", g.Nodes[0].FailureReason)
	}
}

func TestStoreRoundTripPreservesGraphRecoveryState(t *testing.T) {
	store := NewStore(t.TempDir())
	state := State{Key: "cli:test"}
	task := state.StartTask("resume graph")
	task.Graph = &TaskGraph{
		ID:     "g1",
		TaskID: task.ID,
		Status: GraphStatusRunning,
		Nodes: []TaskGraphNode{
			{
				ID:            "done",
				Type:          NodeTypeModel,
				Mode:          NodeModeDirect,
				Goal:          "done",
				Status:        NodeStatusCompleted,
				Attempts:      1,
				ResultSummary: "finished",
				EvidenceRefs:  []EvidenceRef{{Kind: "trace", TraceID: "trace-1"}},
				Acceptance:    Acceptance{Criteria: "done", Verified: true, Reason: "ok"},
			},
			{
				ID:       "next",
				Type:     NodeTypeModel,
				Mode:     NodeModeReact,
				Goal:     "next",
				Status:   NodeStatusRunning,
				Depends:  []string{"done"},
				Attempts: 1,
				Acceptance: Acceptance{
					Criteria: "next done",
				},
			},
			{
				ID:     "review",
				Type:   NodeTypeHumanReview,
				Mode:   NodeModeHuman,
				Goal:   "review",
				Status: NodeStatusAwaitingInput,
			},
			{
				ID:            "blocked",
				Type:          NodeTypeTool,
				Mode:          NodeModeTool,
				Goal:          "blocked",
				Status:        NodeStatusBlocked,
				Executor:      "terminal.run",
				FailureReason: "permission denied",
			},
		},
	}
	state.Pending = &PendingAction{Kind: PendingKindHumanReview, TaskID: task.ID, GraphID: "g1", NodeID: "review", Question: "review?"}

	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("cli:test")
	if err != nil {
		t.Fatal(err)
	}
	loadedTask := loaded.TaskByID(task.ID)
	if loadedTask == nil || loadedTask.Graph == nil {
		t.Fatal("expected graph after reload")
	}
	RecoverRunningNodes(loadedTask.Graph)
	if got := loadedTask.Graph.NodeByID("done").Status; got != NodeStatusCompleted {
		t.Fatalf("completed verified node rerun risk, got %q", got)
	}
	if got := loadedTask.Graph.NodeByID("next").Status; got != NodeStatusPending {
		t.Fatalf("running node should become pending after reload, got %q", got)
	}
	if got := loadedTask.Graph.NodeByID("review").Status; got != NodeStatusAwaitingInput {
		t.Fatalf("awaiting input should be preserved, got %q", got)
	}
	if got := loadedTask.Graph.NodeByID("blocked").Status; got != NodeStatusBlocked {
		t.Fatalf("blocked node should not be scheduled, got %q", got)
	}
	if loaded.Pending == nil || loaded.Pending.NodeID != "review" {
		t.Fatalf("pending action not preserved: %#v", loaded.Pending)
	}
	if ready := ReadyNodes(loadedTask.Graph, 10); len(ready) != 1 || ready[0] != "next" {
		t.Fatalf("expected only next to be ready, got %#v", ready)
	}
}
