package session

import (
	"strings"
	"testing"
)

func TestFinalizeGraph_Completed(t *testing.T) {
	g := &TaskGraph{
		ID:     "g1",
		TaskID: "t1",
		Nodes: []TaskGraphNode{
			{ID: "read", Type: NodeTypeTool, Goal: "read config", Status: NodeStatusCompleted, Executor: "file.read", ResultSummary: "config loaded", EvidenceRefs: []EvidenceRef{{Kind: "tool"}}, Acceptance: Acceptance{Verified: true}},
			{ID: "analyze", Type: NodeTypeModel, Goal: "analyze config", Status: NodeStatusCompleted, ResultSummary: "config has 3 entries", Acceptance: Acceptance{Verified: true}},
		},
	}
	vr := VerifyTaskGraph(g)
	result := FinalizeGraph(g, vr)
	if result.Status != FinalizeCompleted {
		t.Fatalf("expected completed, got %q", result.Status)
	}
	if !strings.Contains(result.ReplyText, "config loaded") {
		t.Fatal("reply missing read node result")
	}
	if !strings.Contains(result.ReplyText, "config has 3 entries") {
		t.Fatal("reply missing analyze node result")
	}
	if result.KeepTask {
		t.Fatal("completed should not keep active task")
	}
}

func TestFinalizeGraph_CompletedExcludesUnverified(t *testing.T) {
	g := &TaskGraph{
		ID:     "g2",
		TaskID: "t2",
		Nodes: []TaskGraphNode{
			{ID: "read", Type: NodeTypeTool, Goal: "read config", Status: NodeStatusCompleted, Executor: "file.read", ResultSummary: "config loaded", EvidenceRefs: []EvidenceRef{{Kind: "tool"}}, Acceptance: Acceptance{Criteria: "must be correct", Verified: true}},
			{ID: "unverified", Type: NodeTypeModel, Goal: "unverified output", Status: NodeStatusCompleted, ResultSummary: "should not appear", Acceptance: Acceptance{Criteria: "must verify", Verified: false}},
		},
	}
	vr := VerifyTaskGraph(g)
	if vr.Status != GraphStatusBlocked {
		t.Fatalf("task gate should block unverified criteria node, got %q", vr.Status)
	}
	result := FinalizeGraph(g, vr)
	if result.Status != FinalizeBlocked {
		t.Fatalf("expected blocked for unverified criteria, got %q", result.Status)
	}
	if strings.Contains(result.ReplyText, "should not appear") {
		t.Fatal("unverified node result should not appear in blocked reply")
	}
	if !strings.Contains(result.ReplyText, "acceptance criteria not verified") {
		t.Fatal("blocked reply should mention unverified criteria")
	}
}

func TestFinalizeGraph_Amainput(t *testing.T) {
	g := &TaskGraph{
		ID:     "g3",
		TaskID: "t3",
		Nodes: []TaskGraphNode{
			{ID: "read", Type: NodeTypeTool, Goal: "read config", Status: NodeStatusCompleted, Executor: "file.read", ResultSummary: "config loaded", EvidenceRefs: []EvidenceRef{{Kind: "tool"}}},
			{ID: "review", Type: NodeTypeHumanReview, Goal: "review deployment", Status: NodeStatusAwaitingInput},
		},
	}
	vr := VerifyTaskGraph(g)
	result := FinalizeGraph(g, vr)
	if result.Status != FinalizeAwaitingInput {
		t.Fatalf("expected awaiting_input, got %q", result.Status)
	}
	if !strings.Contains(result.ReplyText, "review deployment") {
		t.Fatal("reply missing review node")
	}
	if !result.KeepTask {
		t.Fatal("awaiting_input should keep active task")
	}
}

func TestFinalizeGraph_Blocked(t *testing.T) {
	g := &TaskGraph{
		ID:     "g4",
		TaskID: "t4",
		Nodes: []TaskGraphNode{
			{ID: "read", Type: NodeTypeTool, Goal: "read config", Status: NodeStatusBlocked, Executor: "file.read", FailureReason: "file not found"},
		},
	}
	vr := VerifyTaskGraph(g)
	result := FinalizeGraph(g, vr)
	if result.Status != FinalizeBlocked {
		t.Fatalf("expected blocked, got %q", result.Status)
	}
	if !strings.Contains(result.ReplyText, "file not found") {
		t.Fatal("reply missing blocker reason")
	}
	if !result.KeepTask {
		t.Fatal("blocked should keep active task")
	}
}

func TestFinalizeGraph_Failed(t *testing.T) {
	g := &TaskGraph{
		ID:     "g5",
		TaskID: "t5",
		Nodes: []TaskGraphNode{
			{ID: "run", Type: NodeTypeTool, Goal: "run command", Status: NodeStatusFailed, Executor: "terminal.run", FailureReason: "command not found"},
		},
	}
	vr := VerifyTaskGraph(g)
	result := FinalizeGraph(g, vr)
	if result.Status != FinalizeFailed {
		t.Fatalf("expected failed, got %q", result.Status)
	}
	if !strings.Contains(result.ReplyText, "command not found") {
		t.Fatal("reply missing failure reason")
	}
	if result.KeepTask {
		t.Fatal("failed should not keep active task")
	}
}

func TestFinalizeGraph_Partial(t *testing.T) {
	g := &TaskGraph{
		ID:     "g6",
		TaskID: "t6",
		Nodes: []TaskGraphNode{
			{ID: "read", Type: NodeTypeTool, Goal: "read config", Status: NodeStatusCompleted, Executor: "file.read", ResultSummary: "config loaded", EvidenceRefs: []EvidenceRef{{Kind: "tool"}}},
			{ID: "analyze", Type: NodeTypeModel, Goal: "analyze config", Status: NodeStatusPending},
		},
	}
	vr := VerifyTaskGraph(g)
	result := FinalizeGraph(g, vr)
	if result.Status != FinalizePartial {
		t.Fatalf("expected partial, got %q", result.Status)
	}
	if !strings.Contains(result.ReplyText, "config loaded") {
		t.Fatal("reply missing completed result")
	}
	if !strings.Contains(result.ReplyText, "analyze") {
		t.Fatal("reply missing pending node")
	}
	if !result.KeepTask {
		t.Fatal("partial should keep active task")
	}
}

func TestFinalizeGraph_EmptyGraph(t *testing.T) {
	g := &TaskGraph{ID: "g7", TaskID: "t7", Nodes: []TaskGraphNode{}}
	vr := VerifyTaskGraph(g)
	result := FinalizeGraph(g, vr)
	if result.Status != FinalizeCompleted {
		t.Fatalf("expected completed for empty graph, got %q", result.Status)
	}
}

func TestFinalizeGraph_NilGraph(t *testing.T) {
	result := FinalizeGraph(nil, GraphVerificationResult{})
	if result.Status != FinalizeCompleted {
		t.Fatalf("expected completed for nil graph, got %q", result.Status)
	}
}

func TestFinalizeGraph_BlockedUnverifiedCriteria(t *testing.T) {
	g := &TaskGraph{
		ID:     "g8",
		TaskID: "t8",
		Nodes: []TaskGraphNode{
			{ID: "read", Type: NodeTypeTool, Goal: "read config", Status: NodeStatusCompleted, Executor: "file.read", ResultSummary: "data", EvidenceRefs: []EvidenceRef{{Kind: "tool"}}, Acceptance: Acceptance{Criteria: "must verify", Verified: false}},
		},
	}
	vr := VerifyTaskGraph(g)
	result := FinalizeGraph(g, vr)
	if result.Status != FinalizeBlocked {
		t.Fatalf("expected blocked for unverified criteria, got %q", result.Status)
	}
	if !strings.Contains(result.ReplyText, "acceptance criteria not verified") {
		t.Fatalf("reply should mention unverified criteria, got %q", result.ReplyText)
	}
}

func TestFinalizeGraph_CompletedSkipsEmptySummary(t *testing.T) {
	g := &TaskGraph{
		ID:     "g9",
		TaskID: "t9",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "answer", Status: NodeStatusSkipped, ResultSummary: "", Acceptance: Acceptance{Verified: true}},
			{ID: "n2", Type: NodeTypeModel, Goal: "verify", Status: NodeStatusCompleted, ResultSummary: "verified ok", Acceptance: Acceptance{Verified: true}},
		},
	}
	vr := VerifyTaskGraph(g)
	result := FinalizeGraph(g, vr)
	if result.Status != FinalizeCompleted {
		t.Fatalf("expected completed, got %q", result.Status)
	}
	if !strings.Contains(result.ReplyText, "verified ok") {
		t.Fatal("reply should include non-empty result")
	}
	if strings.Contains(result.ReplyText, "n1") {
		t.Fatal("reply should not include skipped node")
	}
}
