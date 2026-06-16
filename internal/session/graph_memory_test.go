package session

import (
	"testing"
	"time"
)

func TestBuildGraphMemorySummary(t *testing.T) {
	g := &TaskGraph{
		ID: "g1", TaskID: "t1", Status: GraphStatusCompleted,
		Nodes: []TaskGraphNode{
			{ID: "read", Type: NodeTypeTool, Goal: "read file", Status: NodeStatusCompleted, Executor: "file.read", ResultSummary: "file contents", Attempts: 1, EvidenceRefs: []EvidenceRef{{Kind: "tool", ToolName: "file.read"}}},
			{ID: "analyze", Type: NodeTypeModel, Goal: "analyze", Status: NodeStatusCompleted, ResultSummary: "analysis done", Attempts: 2},
			{ID: "deploy", Type: NodeTypeTool, Goal: "deploy", Status: NodeStatusFailed, Executor: "terminal.run", FailureReason: "permission denied", Attempts: 3},
		},
	}

	summary := BuildGraphMemorySummary(g, "test task")
	if summary == nil {
		t.Fatal("summary is nil")
	}
	if summary.GraphID != "g1" {
		t.Fatalf("expected g1, got %q", summary.GraphID)
	}
	if summary.Goal != "test task" {
		t.Fatalf("expected test task, got %q", summary.Goal)
	}
	if summary.Status != GraphStatusCompleted {
		t.Fatalf("expected completed, got %q", summary.Status)
	}
	if len(summary.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(summary.Nodes))
	}

	n1 := summary.Nodes[0]
	if n1.ID != "read" || n1.Type != NodeTypeTool || n1.Status != NodeStatusCompleted {
		t.Fatalf("node 0 mismatch: %+v", n1)
	}
	if n1.Attempts != 1 {
		t.Fatalf("expected attempts=1, got %d", n1.Attempts)
	}
	if len(n1.EvidenceRefs) != 1 {
		t.Fatalf("expected 1 evidence ref, got %d", len(n1.EvidenceRefs))
	}

	n2 := summary.Nodes[1]
	if n2.Attempts != 2 {
		t.Fatalf("expected attempts=2, got %d", n2.Attempts)
	}

	n3 := summary.Nodes[2]
	if n3.Status != NodeStatusFailed || n3.FailureReason != "permission denied" {
		t.Fatalf("node 2 mismatch: %+v", n3)
	}
	if n3.Attempts != 3 {
		t.Fatalf("expected attempts=3, got %d", n3.Attempts)
	}
}

func TestBuildGraphMemorySummary_NilGraph(t *testing.T) {
	if summary := BuildGraphMemorySummary(nil, ""); summary != nil {
		t.Fatal("expected nil for nil graph")
	}
}

func TestBuildGraphMemorySummary_EmptyNodes(t *testing.T) {
	g := &TaskGraph{ID: "g1", TaskID: "t1", Nodes: []TaskGraphNode{}}
	summary := BuildGraphMemorySummary(g, "empty")
	if len(summary.Nodes) != 0 {
		t.Fatalf("expected 0 nodes, got %d", len(summary.Nodes))
	}
}

func TestBuildGraphMemorySummary_VerifiedAt(t *testing.T) {
	now := time.Now()
	g := &TaskGraph{
		ID: "g1", TaskID: "t1", Status: GraphStatusCompleted,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "answer", Status: NodeStatusCompleted, VerifiedAt: now},
		},
	}
	summary := BuildGraphMemorySummary(g, "test")
	if !summary.Nodes[0].VerifiedAt.Equal(now) {
		t.Fatalf("VerifiedAt not preserved: %v vs %v", summary.Nodes[0].VerifiedAt, now)
	}
}
