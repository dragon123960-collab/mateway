package session

import "testing"

func TestApplyLocalReplan_PreservesCompletedUpstream(t *testing.T) {
	g := &TaskGraph{
		ID:     "g1",
		TaskID: "t1",
		Status: GraphStatusFailed,
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeModel, Mode: NodeModeDirect, Goal: "collect", Status: NodeStatusCompleted, ResultSummary: "done", Acceptance: Acceptance{Verified: true}},
			{ID: "b", Type: NodeTypeModel, Mode: NodeModeDirect, Goal: "bad", Status: NodeStatusFailed, Depends: []string{"a"}, FailureReason: "bad output"},
			{ID: "c", Type: NodeTypeModel, Mode: NodeModeDirect, Goal: "downstream", Status: NodeStatusPending, Depends: []string{"b"}},
		},
	}

	errs := ApplyLocalReplan(g, LocalReplanRequest{
		FailedNodeID: "b",
		ReplacementNodes: []TaskGraphNode{
			{ID: "b2", Type: NodeTypeModel, Mode: NodeModeDirect, Goal: "replacement", Depends: []string{"a"}},
			{ID: "c2", Type: NodeTypeModel, Mode: NodeModeDirect, Goal: "new downstream", Depends: []string{"b2"}},
		},
	})
	if !errs.IsValid() {
		t.Fatalf("expected valid replan, got %v", errs)
	}

	if a := g.NodeByID("a"); a == nil || a.Status != NodeStatusCompleted || !a.Acceptance.Verified {
		t.Fatalf("completed upstream not preserved: %#v", a)
	}
	if g.NodeByID("b") != nil || g.NodeByID("c") != nil {
		t.Fatalf("old failed/downstream nodes should be removed, got %v", g.NodeIDs())
	}
	if g.NodeByID("b2") == nil || g.NodeByID("c2") == nil {
		t.Fatalf("replacement nodes missing, got %v", g.NodeIDs())
	}
}

func TestApplyLocalReplan_DoesNotBypassBlockedDownstream(t *testing.T) {
	g := &TaskGraph{
		ID:     "g1",
		TaskID: "t1",
		Status: GraphStatusFailed,
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeModel, Mode: NodeModeDirect, Goal: "collect", Status: NodeStatusCompleted, Acceptance: Acceptance{Verified: true}},
			{ID: "b", Type: NodeTypeModel, Mode: NodeModeDirect, Goal: "bad", Status: NodeStatusFailed, Depends: []string{"a"}},
			{ID: "c", Type: NodeTypeModel, Mode: NodeModeDirect, Goal: "needs input", Status: NodeStatusBlocked, FailureReason: "permission needed"},
		},
	}

	errs := ApplyLocalReplan(g, LocalReplanRequest{
		FailedNodeID:     "b",
		ReplacementNodes: []TaskGraphNode{{ID: "b2", Type: NodeTypeModel, Mode: NodeModeDirect, Goal: "replacement", Depends: []string{"a"}}},
	})
	if !errs.IsValid() {
		t.Fatalf("expected valid replan, got %v", errs)
	}
	if c := g.NodeByID("c"); c == nil || c.Status != NodeStatusBlocked {
		t.Fatalf("blocked node should be preserved, got %#v", c)
	}
}
