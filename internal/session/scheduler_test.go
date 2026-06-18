package session

import (
	"slices"
	"testing"
)

func TestReadyNodes_NilGraph(t *testing.T) {
	if nodes := ReadyNodes(nil, 10); nodes != nil {
		t.Fatal("expected nil for nil graph")
	}
}

func TestReadyNodes_ZeroMaxParallel(t *testing.T) {
	g := &TaskGraph{
		ID:     "g1",
		TaskID: "t1",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "x", Status: NodeStatusPending},
		},
	}
	if nodes := ReadyNodes(g, 0); nodes != nil {
		t.Fatal("expected nil for zero maxParallel")
	}
}

func TestReadyNodes_NegativeMaxParallel(t *testing.T) {
	g := &TaskGraph{
		ID:     "g1",
		TaskID: "t1",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "x", Status: NodeStatusPending},
		},
	}
	if nodes := ReadyNodes(g, -1); nodes != nil {
		t.Fatal("expected nil for negative maxParallel")
	}
}

func TestReadyNodes_NoDependencyReady(t *testing.T) {
	g := &TaskGraph{
		ID:     "g1",
		TaskID: "t1",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "answer", Status: NodeStatusPending},
		},
	}
	ready := ReadyNodes(g, 10)
	if len(ready) != 1 || ready[0] != "n1" {
		t.Fatalf("expected [n1], got %v", ready)
	}
}

func TestReadyNodes_DependencyNotComplete(t *testing.T) {
	g := &TaskGraph{
		ID:     "g2",
		TaskID: "t2",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeTool, Goal: "read", Executor: "file.read", Status: NodeStatusPending},
			{ID: "b", Type: NodeTypeModel, Goal: "analyze", Depends: []string{"a"}, Status: NodeStatusPending},
		},
	}
	ready := ReadyNodes(g, 10)
	// Only "a" has no deps; "b" depends on "a" which is still pending
	if len(ready) != 1 || ready[0] != "a" {
		t.Fatalf("expected [a], got %v", ready)
	}
}

func TestReadyNodes_DependencyCompleted(t *testing.T) {
	g := &TaskGraph{
		ID:     "g2",
		TaskID: "t2",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeTool, Goal: "read", Executor: "file.read", Status: NodeStatusCompleted, Acceptance: Acceptance{Verified: true}},
			{ID: "b", Type: NodeTypeModel, Goal: "analyze", Depends: []string{"a"}, Status: NodeStatusPending},
		},
	}
	ready := ReadyNodes(g, 10)
	if len(ready) != 1 || ready[0] != "b" {
		t.Fatalf("expected [b], got %v", ready)
	}
}

func TestReadyNodes_DependencyCompletedButUnverifiedBlocks(t *testing.T) {
	g := &TaskGraph{
		ID:     "g-unverified",
		TaskID: "t-unverified",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeModel, Goal: "done", Status: NodeStatusCompleted, Acceptance: Acceptance{Verified: false}},
			{ID: "b", Type: NodeTypeModel, Goal: "dependent", Depends: []string{"a"}, Status: NodeStatusPending},
		},
	}
	if ready := ReadyNodes(g, 10); len(ready) != 0 {
		t.Fatalf("unverified completed dependency should block, got %v", ready)
	}
}

func TestReadyNodes_DependencySkipped(t *testing.T) {
	g := &TaskGraph{
		ID:     "g2",
		TaskID: "t2",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeTool, Goal: "read", Executor: "file.read", Status: NodeStatusSkipped},
			{ID: "b", Type: NodeTypeModel, Goal: "analyze", Depends: []string{"a"}, Status: NodeStatusPending},
		},
	}
	ready := ReadyNodes(g, 10)
	if len(ready) != 1 || ready[0] != "b" {
		t.Fatalf("expected [b] when dep is skipped, got %v", ready)
	}
}

func TestReadyNodes_DependencyRunning(t *testing.T) {
	g := &TaskGraph{
		ID:     "g3",
		TaskID: "t3",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeTool, Goal: "read", Executor: "file.read", Status: NodeStatusRunning},
			{ID: "b", Type: NodeTypeModel, Goal: "analyze", Depends: []string{"a"}, Status: NodeStatusPending},
		},
	}
	ready := ReadyNodes(g, 10)
	if len(ready) != 0 {
		t.Fatalf("expected no ready nodes when dep is running, got %v", ready)
	}
}

func TestReadyNodes_DiamondGraph(t *testing.T) {
	g := &TaskGraph{
		ID:     "g4",
		TaskID: "t4",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeTool, Goal: "read", Executor: "file.read", Status: NodeStatusPending},
			{ID: "b", Type: NodeTypeTool, Goal: "parse json", Executor: "bash", Depends: []string{"a"}, Status: NodeStatusPending},
			{ID: "c", Type: NodeTypeTool, Goal: "parse yaml", Executor: "bash", Depends: []string{"a"}, Status: NodeStatusPending},
			{ID: "d", Type: NodeTypeModel, Goal: "merge", Depends: []string{"b", "c"}, Status: NodeStatusPending},
		},
	}
	// Only "a" is ready initially
	ready := ReadyNodes(g, 10)
	if len(ready) != 1 || ready[0] != "a" {
		t.Fatalf("expected [a] initially, got %v", ready)
	}

	// Complete "a" → "b" and "c" become ready
	g.NodeByID("a").SetCompleted(true, "done")
	ready = ReadyNodes(g, 10)
	if len(ready) != 2 {
		t.Fatalf("expected [b c], got %v", ready)
	}
	if !slices.Contains(ready, "b") || !slices.Contains(ready, "c") {
		t.Fatalf("expected [b c], got %v", ready)
	}

	// Complete "b" only → "d" not ready yet (c still pending)
	g.NodeByID("b").SetCompleted(true, "done")
	ready = ReadyNodes(g, 10)
	if len(ready) != 1 || ready[0] != "c" {
		t.Fatalf("expected [c] when only b is done, got %v", ready)
	}

	// Complete "c" → "d" becomes ready
	g.NodeByID("c").SetCompleted(true, "done")
	ready = ReadyNodes(g, 10)
	if len(ready) != 1 || ready[0] != "d" {
		t.Fatalf("expected [d] when b and c are done, got %v", ready)
	}
}

func TestReadyNodes_MaxParallelLimits(t *testing.T) {
	g := &TaskGraph{
		ID:     "g5",
		TaskID: "t5",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "a", Status: NodeStatusPending},
			{ID: "n2", Type: NodeTypeModel, Goal: "b", Status: NodeStatusPending},
			{ID: "n3", Type: NodeTypeModel, Goal: "c", Status: NodeStatusPending},
		},
	}
	ready := ReadyNodes(g, 2)
	if len(ready) != 2 {
		t.Fatalf("max=2 expected 2 ready, got %d: %v", len(ready), ready)
	}
	ready = ReadyNodes(g, 1)
	if len(ready) != 1 {
		t.Fatalf("max=1 expected 1 ready, got %d: %v", len(ready), ready)
	}
	ready = ReadyNodes(g, 100)
	if len(ready) != 3 {
		t.Fatalf("max=100 expected 3 ready, got %d: %v", len(ready), ready)
	}
}

func TestReadyNodes_CompletedNotReadyAgain(t *testing.T) {
	g := &TaskGraph{
		ID:     "g6",
		TaskID: "t6",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "done", Status: NodeStatusCompleted},
			{ID: "n2", Type: NodeTypeModel, Goal: "pending", Status: NodeStatusPending},
		},
	}
	ready := ReadyNodes(g, 10)
	if len(ready) != 1 || ready[0] != "n2" {
		t.Fatalf("expected [n2], completed n1 should not be ready, got %v", ready)
	}
}

func TestReadyNodes_FailedDependencyBlocks(t *testing.T) {
	g := &TaskGraph{
		ID:     "g7",
		TaskID: "t7",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeTool, Goal: "read", Executor: "file.read", Status: NodeStatusFailed, FailureReason: "file not found"},
			{ID: "b", Type: NodeTypeModel, Goal: "analyze", Depends: []string{"a"}, Status: NodeStatusPending},
			{ID: "c", Type: NodeTypeModel, Goal: "independent", Status: NodeStatusPending},
		},
	}
	ready := ReadyNodes(g, 10)
	if len(ready) != 1 || ready[0] != "c" {
		t.Fatalf("expected [c] only, failed dep blocks b, got %v", ready)
	}
}

func TestReadyNodes_BlockedDependencyBlocks(t *testing.T) {
	g := &TaskGraph{
		ID:     "g7b",
		TaskID: "t7b",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeTool, Goal: "read", Executor: "file.read", Status: NodeStatusBlocked},
			{ID: "b", Type: NodeTypeModel, Goal: "analyze", Depends: []string{"a"}, Status: NodeStatusPending},
		},
	}
	ready := ReadyNodes(g, 10)
	if len(ready) != 0 {
		t.Fatalf("expected no ready, blocked dep blocks b, got %v", ready)
	}
}

func TestReadyNodes_AwaitingInputDependencyBlocks(t *testing.T) {
	g := &TaskGraph{
		ID:     "g7c",
		TaskID: "t7c",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeHumanReview, Goal: "review", Status: NodeStatusAwaitingInput},
			{ID: "b", Type: NodeTypeModel, Goal: "next", Depends: []string{"a"}, Status: NodeStatusPending},
		},
	}
	ready := ReadyNodes(g, 10)
	if len(ready) != 0 {
		t.Fatalf("expected no ready, awaiting dep blocks b, got %v", ready)
	}
}

func TestReadyNodes_MixedStatuses(t *testing.T) {
	g := &TaskGraph{
		ID:     "g8",
		TaskID: "t8",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeTool, Goal: "a", Executor: "tool.x", Status: NodeStatusCompleted, Acceptance: Acceptance{Verified: true}},
			{ID: "b", Type: NodeTypeTool, Goal: "b", Executor: "tool.x", Status: NodeStatusSkipped},
			{ID: "c", Type: NodeTypeModel, Goal: "c", Depends: []string{"a", "b"}, Status: NodeStatusPending},
			{ID: "d", Type: NodeTypeTool, Goal: "d", Executor: "tool.x", Status: NodeStatusFailed},
			{ID: "e", Type: NodeTypeModel, Goal: "e", Depends: []string{"c", "d"}, Status: NodeStatusPending},
		},
	}
	ready := ReadyNodes(g, 10)
	// "c" is ready (a completed, b skipped); "e" blocked by "d" failed
	if len(ready) != 1 || ready[0] != "c" {
		t.Fatalf("expected [c], got %v", ready)
	}
}

func TestReadyNodes_EmptyGraph(t *testing.T) {
	g := &TaskGraph{
		ID:     "g9",
		TaskID: "t9",
		Status: GraphStatusPlanned,
		Nodes:  []TaskGraphNode{},
	}
	ready := ReadyNodes(g, 10)
	if ready != nil {
		t.Fatalf("expected nil for empty graph, got %v", ready)
	}
}

func TestUpdateGraphStatus_NilGraph(t *testing.T) {
	if s := UpdateGraphStatus(nil); s != "" {
		t.Fatalf("expected empty string for nil graph, got %q", s)
	}
}

func TestUpdateGraphStatus_AllCompleted(t *testing.T) {
	g := &TaskGraph{
		ID:     "g1",
		TaskID: "t1",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "x", Status: NodeStatusCompleted, Acceptance: Acceptance{Verified: true}},
			{ID: "n2", Type: NodeTypeModel, Goal: "y", Status: NodeStatusCompleted, Acceptance: Acceptance{Verified: true}},
		},
	}
	s := UpdateGraphStatus(g)
	if s != GraphStatusCompleted {
		t.Fatalf("expected completed, got %q", s)
	}
	if g.Status != GraphStatusCompleted {
		t.Fatal("UpdateGraphStatus must set g.Status")
	}
}

func TestUpdateGraphStatus_UnverifiedCompletedIsRunning(t *testing.T) {
	g := &TaskGraph{
		ID:     "g-unverified",
		TaskID: "t-unverified",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "x", Status: NodeStatusCompleted, Acceptance: Acceptance{Verified: false}},
		},
	}
	if s := UpdateGraphStatus(g); s != GraphStatusRunning {
		t.Fatalf("unverified completed node should keep graph running, got %q", s)
	}
}

func TestUpdateGraphStatus_MixedCompletedAndSkipped(t *testing.T) {
	g := &TaskGraph{
		ID:     "g2",
		TaskID: "t2",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "x", Status: NodeStatusCompleted, Acceptance: Acceptance{Verified: true}},
			{ID: "n2", Type: NodeTypeModel, Goal: "y", Status: NodeStatusSkipped},
		},
	}
	s := UpdateGraphStatus(g)
	if s != GraphStatusCompleted {
		t.Fatalf("expected completed with mixed completed/skipped, got %q", s)
	}
}

func TestUpdateGraphStatus_AwaitingInput(t *testing.T) {
	g := &TaskGraph{
		ID:     "g3",
		TaskID: "t3",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeHumanReview, Goal: "review", Status: NodeStatusAwaitingInput},
			{ID: "n2", Type: NodeTypeModel, Goal: "done", Status: NodeStatusCompleted},
		},
	}
	s := UpdateGraphStatus(g)
	if s != GraphStatusAwaitingInput {
		t.Fatalf("expected awaiting_input, got %q", s)
	}
}

func TestUpdateGraphStatus_AwaitingInputBeatsRunning(t *testing.T) {
	g := &TaskGraph{
		ID:     "g3b",
		TaskID: "t3b",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeHumanReview, Goal: "review", Status: NodeStatusAwaitingInput},
			{ID: "n2", Type: NodeTypeModel, Goal: "work", Status: NodeStatusRunning},
		},
	}
	s := UpdateGraphStatus(g)
	if s != GraphStatusAwaitingInput {
		t.Fatalf("awaiting_input beats running, got %q", s)
	}
}

func TestUpdateGraphStatus_BlockedWithRunning(t *testing.T) {
	g := &TaskGraph{
		ID:     "g4",
		TaskID: "t4",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeTool, Goal: "fail", Executor: "x", Status: NodeStatusBlocked},
			{ID: "n2", Type: NodeTypeModel, Goal: "work", Status: NodeStatusRunning},
		},
	}
	s := UpdateGraphStatus(g)
	if s != GraphStatusRunning {
		t.Fatalf("blocked + running = running, got %q", s)
	}
}

func TestUpdateGraphStatus_BlockedWithPendingReadyCandidate(t *testing.T) {
	g := &TaskGraph{
		ID:     "g5",
		TaskID: "t5",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeTool, Goal: "blocked", Executor: "x", Status: NodeStatusBlocked},
			{ID: "n2", Type: NodeTypeModel, Goal: "runnable", Status: NodeStatusPending},
		},
	}
	s := UpdateGraphStatus(g)
	if s != GraphStatusRunning {
		t.Fatalf("blocked + pending-ready should be running, got %q", s)
	}
}

func TestUpdateGraphStatus_BlockedWithPendingBlockedByDep(t *testing.T) {
	g := &TaskGraph{
		ID:     "g5b",
		TaskID: "t5b",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeTool, Goal: "blocked", Executor: "x", Status: NodeStatusBlocked},
			{ID: "b", Type: NodeTypeModel, Goal: "dep on blocked", Depends: []string{"a"}, Status: NodeStatusPending},
		},
	}
	s := UpdateGraphStatus(g)
	if s != GraphStatusBlocked {
		t.Fatalf("blocked + pending-blocked-by-dep should be blocked, got %q", s)
	}
}

func TestUpdateGraphStatus_FailedWithRunning(t *testing.T) {
	g := &TaskGraph{
		ID:     "g6",
		TaskID: "t6",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeTool, Goal: "fail", Executor: "x", Status: NodeStatusFailed},
			{ID: "n2", Type: NodeTypeModel, Goal: "work", Status: NodeStatusRunning},
		},
	}
	s := UpdateGraphStatus(g)
	if s != GraphStatusRunning {
		t.Fatalf("failed + running = running, got %q", s)
	}
}

func TestUpdateGraphStatus_FailedWithPendingReadyCandidate(t *testing.T) {
	g := &TaskGraph{
		ID:     "g7",
		TaskID: "t7",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeTool, Goal: "fail", Executor: "x", Status: NodeStatusFailed},
			{ID: "n2", Type: NodeTypeModel, Goal: "runnable", Status: NodeStatusPending},
		},
	}
	s := UpdateGraphStatus(g)
	if s != GraphStatusRunning {
		t.Fatalf("failed + pending-ready should be running, got %q", s)
	}
}

func TestUpdateGraphStatus_FailedWithPendingBlockedByDep(t *testing.T) {
	g := &TaskGraph{
		ID:     "g7b",
		TaskID: "t7b",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeTool, Goal: "failed", Executor: "x", Status: NodeStatusFailed},
			{ID: "b", Type: NodeTypeModel, Goal: "dep on failed", Depends: []string{"a"}, Status: NodeStatusPending},
		},
	}
	s := UpdateGraphStatus(g)
	if s != GraphStatusFailed {
		t.Fatalf("failed + pending-blocked-by-dep should be failed, got %q", s)
	}
}

func TestUpdateGraphStatus_AllPending(t *testing.T) {
	g := &TaskGraph{
		ID:     "g8",
		TaskID: "t8",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "a", Status: NodeStatusPending},
			{ID: "n2", Type: NodeTypeModel, Goal: "b", Status: NodeStatusPending},
		},
	}
	s := UpdateGraphStatus(g)
	if s != GraphStatusRunning {
		t.Fatalf("expected running for all-pending graph, got %q", s)
	}
}

func TestUpdateGraphStatus_ReadyCountsAsActive(t *testing.T) {
	g := &TaskGraph{
		ID:     "g9",
		TaskID: "t9",
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeTool, Goal: "blocked", Executor: "x", Status: NodeStatusBlocked},
			{ID: "n2", Type: NodeTypeModel, Goal: "ready", Status: NodeStatusReady},
		},
	}
	s := UpdateGraphStatus(g)
	if s != GraphStatusRunning {
		t.Fatalf("blocked + ready = running (ready counts as active), got %q", s)
	}
}

func TestUpdateGraphStatus_EmptyGraph(t *testing.T) {
	g := &TaskGraph{
		ID:     "g10",
		TaskID: "t10",
		Status: GraphStatusPlanned,
		Nodes:  []TaskGraphNode{},
	}
	s := UpdateGraphStatus(g)
	if s != GraphStatusCompleted {
		t.Fatalf("empty graph should be completed, got %q", s)
	}
}

func TestScheduler_ResumeDoesNoRerunCompleted(t *testing.T) {
	g := &TaskGraph{
		ID:     "g-resume",
		TaskID: "t-resume",
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeModel, Goal: "already done", Status: NodeStatusCompleted, Acceptance: Acceptance{Verified: true}},
			{ID: "b", Type: NodeTypeModel, Goal: "was blocked", Status: NodeStatusPending, Depends: []string{"a"}},
			{ID: "c", Type: NodeTypeModel, Goal: "also pending", Status: NodeStatusPending},
		},
	}
	ready := ReadyNodes(g, 10)
	// "a" is completed so not ready; "b" depends on completed "a" → ready; "c" no deps → ready
	if len(ready) != 2 {
		t.Fatalf("expected [b c], got %v", ready)
	}
	if !slices.Contains(ready, "b") || !slices.Contains(ready, "c") {
		t.Fatalf("expected [b c], got %v", ready)
	}
	if slices.Contains(ready, "a") {
		t.Fatal("completed node 'a' should not be in ready")
	}
}

func TestScheduler_ComplexGraphTickByTick(t *testing.T) {
	g := &TaskGraph{
		ID:     "g-complex",
		TaskID: "t-complex",
		Nodes: []TaskGraphNode{
			{ID: "fetch", Type: NodeTypeTool, Goal: "fetch data", Executor: "web.fetch", Status: NodeStatusPending},
			{ID: "parse", Type: NodeTypeTool, Goal: "parse", Executor: "file.read", Status: NodeStatusPending, Depends: []string{"fetch"}},
			{ID: "analyze", Type: NodeTypeModel, Goal: "analyze", Status: NodeStatusPending, Depends: []string{"parse"}},
			{ID: "summary", Type: NodeTypeModel, Goal: "summarize", Status: NodeStatusPending, Depends: []string{"fetch"}},
		},
	}

	// Tick 0: only fetch is ready
	assertReady(t, g, 10, []string{"fetch"})

	// Tick 1: fetch completed → parse and summary ready
	g.NodeByID("fetch").SetCompleted(true, "done")
	assertReady(t, g, 10, []string{"parse", "summary"})

	// Tick 2: parse completed → analyze ready, summary still running
	g.NodeByID("parse").SetCompleted(true, "done")
	g.NodeByID("summary").Status = NodeStatusRunning
	assertReady(t, g, 10, []string{"analyze"})

	// Tick 3: analyze completed, summary completed → all done
	g.NodeByID("analyze").SetCompleted(true, "done")
	g.NodeByID("summary").SetCompleted(true, "done")
	assertReady(t, g, 10, []string{})

	// Graph status should be completed
	s := UpdateGraphStatus(g)
	if s != GraphStatusCompleted {
		t.Fatalf("expected completed after all done, got %q", s)
	}
}

func assertReady(t *testing.T, g *TaskGraph, max int, want []string) {
	t.Helper()
	got := ReadyNodes(g, max)
	if len(got) != len(want) {
		t.Fatalf("ReadyNodes: want %v, got %v", want, got)
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Fatalf("ReadyNodes: want %v, got %v", want, got)
		}
	}
}
