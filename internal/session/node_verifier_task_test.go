package session

import "testing"

func completedModelNode(id string, verified bool, output map[string]any) TaskGraphNode {
	return TaskGraphNode{
		ID:            id,
		Type:          NodeTypeModel,
		Mode:          NodeModeDirect,
		Goal:          id,
		Status:        NodeStatusCompleted,
		ResultSummary: "result for " + id,
		Output:        output,
		Acceptance:    Acceptance{Criteria: "done", Verified: verified},
	}
}

func TestVerifyTaskGraphWithContract_AllPass_Trivial(t *testing.T) {
	g := &TaskGraph{ID: "g", TaskID: "t", Nodes: []TaskGraphNode{
		completedModelNode("n1", true, map[string]any{"summary": "ok"}),
	}}
	contract := &TaskContract{TaskAcceptance: "task done", FinalOutput: []string{"summary"}}
	r := VerifyTaskGraphWithContract(g, contract)
	if r.Status != GraphStatusCompleted {
		t.Fatalf("expected completed, got %q (%s)", r.Status, r.Reason)
	}
}

func TestVerifyTaskGraphWithContract_MissingFinalOutput_NeedsRepair(t *testing.T) {
	g := &TaskGraph{ID: "g", TaskID: "t", Nodes: []TaskGraphNode{
		completedModelNode("n1", true, map[string]any{"text": "partial"}),
	}}
	contract := &TaskContract{TaskAcceptance: "produce summary", FinalOutput: []string{"summary"}}
	r := VerifyTaskGraphWithContract(g, contract)
	if r.Status != GraphStatusNeedsRepair {
		t.Fatalf("expected needs_repair, got %q (%s)", r.Status, r.Reason)
	}
	if len(r.MissingNodes) == 0 || r.MissingNodes[0] != "summary" {
		t.Fatalf("expected missing=[summary], got %v", r.MissingNodes)
	}
}

func TestVerifyTaskGraphWithContract_BlockedNode_Blocker(t *testing.T) {
	g := &TaskGraph{ID: "g", TaskID: "t", Nodes: []TaskGraphNode{
		{ID: "n1", Type: NodeTypeTool, Status: NodeStatusBlocked, FailureReason: "denied"},
	}}
	r := VerifyTaskGraphWithContract(g, &TaskContract{})
	if r.Status != GraphStatusBlocked {
		t.Fatalf("expected blocked, got %q", r.Status)
	}
}

func TestVerifyTaskGraphWithContract_AwaitingNode_AwaitingInput(t *testing.T) {
	g := &TaskGraph{ID: "g", TaskID: "t", Nodes: []TaskGraphNode{
		{ID: "n1", Type: NodeTypeHumanReview, Status: NodeStatusAwaitingInput},
	}}
	r := VerifyTaskGraphWithContract(g, &TaskContract{})
	if r.Status != GraphStatusAwaitingInput {
		t.Fatalf("expected awaiting_input, got %q", r.Status)
	}
}

func TestVerifyTaskGraphWithContract_PendingNode_Running(t *testing.T) {
	g := &TaskGraph{ID: "g", TaskID: "t", Nodes: []TaskGraphNode{
		{ID: "n1", Type: NodeTypeModel, Status: NodeStatusPending},
	}}
	r := VerifyTaskGraphWithContract(g, &TaskContract{})
	if r.Status != GraphStatusRunning {
		t.Fatalf("expected running, got %q", r.Status)
	}
}

func TestVerifyTaskGraphWithContract_FailedNode_Failed(t *testing.T) {
	g := &TaskGraph{ID: "g", TaskID: "t", Nodes: []TaskGraphNode{
		{ID: "n1", Type: NodeTypeModel, Status: NodeStatusFailed, FailureReason: "boom"},
	}}
	r := VerifyTaskGraphWithContract(g, &TaskContract{})
	if r.Status != GraphStatusFailed {
		t.Fatalf("expected failed, got %q", r.Status)
	}
}

func TestVerifyTaskGraphWithContract_NilContract_NoFinalOutputCheck(t *testing.T) {
	g := &TaskGraph{ID: "g", TaskID: "t", Nodes: []TaskGraphNode{
		completedModelNode("n1", true, map[string]any{"text": "ok"}),
	}}
	r := VerifyTaskGraphWithContract(g, nil)
	if r.Status != GraphStatusCompleted {
		t.Fatalf("nil contract should not enforce final outputs, got %q", r.Status)
	}
}

func TestVerifyTaskGraphWithContract_UnverifiedAcceptance_Blocker(t *testing.T) {
	g := &TaskGraph{ID: "g", TaskID: "t", Nodes: []TaskGraphNode{
		completedModelNode("n1", false, map[string]any{"summary": "ok"}),
	}}
	r := VerifyTaskGraphWithContract(g, &TaskContract{FinalOutput: []string{"summary"}})
	if r.Status != GraphStatusBlocked {
		t.Fatalf("expected blocked for unverified acceptance, got %q", r.Status)
	}
}
