package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateTaskGraph_NilGraph(t *testing.T) {
	errs := ValidateTaskGraph(nil)
	if errs.IsValid() {
		t.Fatal("expected error for nil graph")
	}
}

func TestValidateTaskGraph_EmptyGraphID(t *testing.T) {
	g := &TaskGraph{TaskID: "task-1", Status: GraphStatusPlanned, Nodes: []TaskGraphNode{
		{ID: "n1", Type: NodeTypeModel, Goal: "answer"},
	}}
	errs := ValidateTaskGraph(g)
	if errs.IsValid() {
		t.Fatal("expected error for empty graph ID")
	}
}

func TestValidateTaskGraph_EmptyTaskID(t *testing.T) {
	g := &TaskGraph{ID: "g1", Status: GraphStatusPlanned, Nodes: []TaskGraphNode{
		{ID: "n1", Type: NodeTypeModel, Goal: "answer"},
	}}
	errs := ValidateTaskGraph(g)
	if errs.IsValid() {
		t.Fatal("expected error for empty task ID")
	}
}

func TestValidateTaskGraph_InvalidGraphStatus(t *testing.T) {
	g := &TaskGraph{ID: "g1", TaskID: "task-1", Status: "bogus", Nodes: []TaskGraphNode{
		{ID: "n1", Type: NodeTypeModel, Goal: "answer"},
	}}
	errs := ValidateTaskGraph(g)
	if errs.IsValid() {
		t.Fatal("expected error for invalid graph status")
	}
}

func TestValidateTaskGraph_NoNodes(t *testing.T) {
	g := &TaskGraph{ID: "g1", TaskID: "task-1", Status: GraphStatusPlanned}
	errs := ValidateTaskGraph(g)
	if errs.IsValid() {
		t.Fatal("expected error for empty nodes")
	}
}

func TestValidateTaskGraph_SingleNodeModel(t *testing.T) {
	g := &TaskGraph{
		ID:     "g1",
		TaskID: "task-1",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "answer the question", Status: NodeStatusPending},
		},
	}
	errs := ValidateTaskGraph(g)
	if !errs.IsValid() {
		t.Fatalf("expected valid simple graph, got: %v", errs)
	}
}

func TestValidateTaskGraph_DiamondDependency(t *testing.T) {
	g := &TaskGraph{
		ID:     "g2",
		TaskID: "task-2",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeTool, Goal: "read file", Executor: "file.read", Status: NodeStatusPending},
			{ID: "b", Type: NodeTypeTool, Goal: "parse json", Executor: "bash", Depends: []string{"a"}, Status: NodeStatusPending},
			{ID: "c", Type: NodeTypeTool, Goal: "parse yaml", Executor: "bash", Depends: []string{"a"}, Status: NodeStatusPending},
			{ID: "d", Type: NodeTypeModel, Goal: "merge results", Depends: []string{"b", "c"}, Status: NodeStatusPending},
		},
	}
	errs := ValidateTaskGraph(g)
	if !errs.IsValid() {
		t.Fatalf("expected valid diamond graph, got: %v", errs)
	}
}

func TestValidateTaskGraph_ChainDependency(t *testing.T) {
	g := &TaskGraph{
		ID:     "g3",
		TaskID: "task-3",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeTool, Goal: "step 1", Executor: "file.read", Status: NodeStatusPending},
			{ID: "n2", Type: NodeTypeModel, Goal: "step 2", Depends: []string{"n1"}, Status: NodeStatusPending},
			{ID: "n3", Type: NodeTypeModel, Goal: "step 3", Depends: []string{"n2"}, Status: NodeStatusPending},
		},
	}
	errs := ValidateTaskGraph(g)
	if !errs.IsValid() {
		t.Fatalf("expected valid chain graph, got: %v", errs)
	}
}

func TestValidateTaskGraph_DuplicateNodeID(t *testing.T) {
	g := &TaskGraph{
		ID:     "g4",
		TaskID: "task-4",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "a"},
			{ID: "n1", Type: NodeTypeTool, Goal: "b", Executor: "x"},
		},
	}
	errs := ValidateTaskGraph(g)
	if errs.IsValid() {
		t.Fatal("expected error for duplicate node ID")
	}
}

func TestValidateTaskGraph_EmptyNodeID(t *testing.T) {
	g := &TaskGraph{
		ID:     "g5",
		TaskID: "task-5",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "", Type: NodeTypeModel, Goal: "a"},
			{ID: "n2", Type: NodeTypeModel, Goal: "b"},
		},
	}
	errs := ValidateTaskGraph(g)
	if errs.IsValid() {
		t.Fatal("expected error for empty node ID")
	}
}

func TestValidateTaskGraph_EmptyGoal(t *testing.T) {
	g := &TaskGraph{
		ID:     "g6",
		TaskID: "task-6",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: ""},
		},
	}
	errs := ValidateTaskGraph(g)
	if errs.IsValid() {
		t.Fatal("expected error for empty node goal")
	}
}

func TestValidateTaskGraph_InvalidNodeType(t *testing.T) {
	g := &TaskGraph{
		ID:     "g7",
		TaskID: "task-7",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: "unknown", Goal: "a"},
		},
	}
	errs := ValidateTaskGraph(g)
	if errs.IsValid() {
		t.Fatal("expected error for invalid node type")
	}
}

func TestValidateTaskGraph_InvalidNodeStatus(t *testing.T) {
	g := &TaskGraph{
		ID:     "g8",
		TaskID: "task-8",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "a", Status: "bogus"},
		},
	}
	errs := ValidateTaskGraph(g)
	if errs.IsValid() {
		t.Fatal("expected error for invalid node status")
	}
}

func TestValidateTaskGraph_EmptyNodeStatus(t *testing.T) {
	g := &TaskGraph{
		ID:     "g8b",
		TaskID: "task-8b",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "a", Status: ""},
		},
	}
	errs := ValidateTaskGraph(g)
	if errs.IsValid() {
		t.Fatal("expected error for empty node status")
	}
}

func TestValidateTaskGraph_SelfDependency(t *testing.T) {
	g := &TaskGraph{
		ID:     "g9",
		TaskID: "task-9",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "a", Depends: []string{"n1"}},
		},
	}
	errs := ValidateTaskGraph(g)
	if errs.IsValid() {
		t.Fatal("expected error for self dependency")
	}
}

func TestValidateTaskGraph_UnknownDependency(t *testing.T) {
	g := &TaskGraph{
		ID:     "g10",
		TaskID: "task-10",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "a", Depends: []string{"missing"}},
		},
	}
	errs := ValidateTaskGraph(g)
	if errs.IsValid() {
		t.Fatal("expected error for unknown dependency")
	}
}

func TestValidateTaskGraph_SimpleCycle(t *testing.T) {
	g := &TaskGraph{
		ID:     "g11",
		TaskID: "task-11",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeModel, Goal: "a", Depends: []string{"b"}},
			{ID: "b", Type: NodeTypeModel, Goal: "b", Depends: []string{"a"}},
		},
	}
	errs := ValidateTaskGraph(g)
	if errs.IsValid() {
		t.Fatal("expected error for cycle")
	}
}

func TestValidateTaskGraph_ThreeNodeCycle(t *testing.T) {
	g := &TaskGraph{
		ID:     "g12",
		TaskID: "task-12",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeModel, Goal: "a", Depends: []string{"c"}},
			{ID: "b", Type: NodeTypeModel, Goal: "b", Depends: []string{"a"}},
			{ID: "c", Type: NodeTypeModel, Goal: "c", Depends: []string{"b"}},
		},
	}
	errs := ValidateTaskGraph(g)
	if errs.IsValid() {
		t.Fatal("expected error for 3-node cycle")
	}
}

func TestValidateTaskGraph_ToolMissingExecutor(t *testing.T) {
	g := &TaskGraph{
		ID:     "g13",
		TaskID: "task-13",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeTool, Goal: "do something"},
		},
	}
	errs := ValidateTaskGraph(g)
	if errs.IsValid() {
		t.Fatal("expected error for tool node missing executor")
	}
}

func TestValidateTaskGraph_ToolWithInputField(t *testing.T) {
	g := &TaskGraph{
		ID:     "g14",
		TaskID: "task-14",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeTool, Goal: "do", Input: map[string]any{"tool": "bash"}, Status: NodeStatusPending},
		},
	}
	errs := ValidateTaskGraph(g)
	if !errs.IsValid() {
		t.Fatalf("expected valid tool node with input.tool, got: %v", errs)
	}
}

func TestValidateTaskGraph_ToolWithExecutor(t *testing.T) {
	g := &TaskGraph{
		ID:     "g15",
		TaskID: "task-15",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeTool, Goal: "do", Executor: "file.read", Status: NodeStatusPending},
		},
	}
	errs := ValidateTaskGraph(g)
	if !errs.IsValid() {
		t.Fatalf("expected valid tool with executor, got: %v", errs)
	}
}

func TestValidateTaskGraph_SkillMissingExecutor(t *testing.T) {
	g := &TaskGraph{
		ID:     "g16",
		TaskID: "task-16",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeSkill, Goal: "run skill"},
		},
	}
	errs := ValidateTaskGraph(g)
	if errs.IsValid() {
		t.Fatal("expected error for skill node missing executor")
	}
}

func TestValidateTaskGraph_SkillWithInputField(t *testing.T) {
	g := &TaskGraph{
		ID:     "g17",
		TaskID: "task-17",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeSkill, Goal: "run", Input: map[string]any{"skill": "/path/to/skill"}, Status: NodeStatusPending},
		},
	}
	errs := ValidateTaskGraph(g)
	if !errs.IsValid() {
		t.Fatalf("expected valid skill node, got: %v", errs)
	}
}

func TestValidateTaskGraph_HumanMissingGoalAndCriteria(t *testing.T) {
	g := &TaskGraph{
		ID:     "g18",
		TaskID: "task-18",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeHumanReview},
		},
	}
	errs := ValidateTaskGraph(g)
	if errs.IsValid() {
		t.Fatal("expected error for human node missing goal/criteria")
	}
}

func TestValidateTaskGraph_HumanWithAcceptanceCriteria(t *testing.T) {
	g := &TaskGraph{
		ID:     "g19",
		TaskID: "task-19",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeHumanReview, Acceptance: Acceptance{Criteria: "review output"}, Status: NodeStatusPending},
		},
	}
	errs := ValidateTaskGraph(g)
	if !errs.IsValid() {
		t.Fatalf("expected valid human node with criteria, got: %v", errs)
	}
}

func TestValidateTaskGraph_HumanWithGoal(t *testing.T) {
	g := &TaskGraph{
		ID:     "g20",
		TaskID: "task-20",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeHumanConfirm, Goal: "confirm deployment", Status: NodeStatusPending},
		},
	}
	errs := ValidateTaskGraph(g)
	if !errs.IsValid() {
		t.Fatalf("expected valid human node with goal, got: %v", errs)
	}
}

func TestValidateTaskGraph_ValidTypeModeCombos(t *testing.T) {
	combos := []struct {
		nodeType string
		mode     string
	}{
		{NodeTypeSubtask, NodeModeDirect},
		{NodeTypeSubtask, NodeModeReact},
		{NodeTypeSkill, NodeModeSkill},
		{NodeTypeTool, NodeModeTool},
		{NodeTypeTool, NodeModeScript},
		{NodeTypeHumanReview, NodeModeHuman},
		{NodeTypeHumanConfirm, NodeModeHuman},
		{NodeTypeModel, ""},
		{NodeTypeTool, ""},
	}
	for _, c := range combos {
		t.Run(c.nodeType+"/"+c.mode, func(t *testing.T) {
			g := &TaskGraph{
				ID: "g", TaskID: "t", Status: GraphStatusPlanned,
				Nodes: []TaskGraphNode{
					{ID: "n1", Type: c.nodeType, Mode: c.mode, Goal: "test", Status: NodeStatusPending,
						Executor: "file.read", Input: map[string]any{"skill": "/x/y"}},
				},
			}
			errs := ValidateTaskGraph(g)
			if !errs.IsValid() {
				t.Fatalf("expected valid combo, got: %v", errs)
			}
		})
	}
}

func TestValidateTaskGraph_InvalidTypeModeCombos(t *testing.T) {
	combos := []struct {
		nodeType string
		mode     string
	}{
		{NodeTypeHumanConfirm, NodeModeReact},
		{NodeTypeHumanReview, NodeModeDirect},
		{NodeTypeSubtask, NodeModeSkill},
		{NodeTypeTool, NodeModeReact},
		{NodeTypeSkill, NodeModeDirect},
	}
	for _, c := range combos {
		t.Run(c.nodeType+"/"+c.mode, func(t *testing.T) {
			g := &TaskGraph{
				ID: "g", TaskID: "t", Status: GraphStatusPlanned,
				Nodes: []TaskGraphNode{
					{ID: "n1", Type: c.nodeType, Mode: c.mode, Goal: "test", Status: NodeStatusPending},
				},
			}
			errs := ValidateTaskGraph(g)
			if errs.IsValid() {
				t.Fatalf("%s/%s should be rejected", c.nodeType, c.mode)
			}
		})
	}
}

func TestValidateTaskGraph_UnknownMode(t *testing.T) {
	g := &TaskGraph{
		ID: "g", TaskID: "t", Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeSubtask, Mode: "bogus", Goal: "test", Status: NodeStatusPending},
		},
	}
	errs := ValidateTaskGraph(g)
	if errs.IsValid() {
		t.Fatal("expected error for unknown mode")
	}
}

func TestValidateTaskGraph_PreservesInput(t *testing.T) {
	original := &TaskGraph{
		ID:     "g21",
		TaskID: "task-21",
		Status: GraphStatusPlanned,
		Nodes: []TaskGraphNode{
			{ID: "n1", Type: NodeTypeModel, Goal: "answer", Input: map[string]any{"key": "value"}, EvidenceRefs: []EvidenceRef{{Kind: "trace", TraceID: "t1"}}},
		},
	}
	before := deepCopyGraph(t, original)
	_ = ValidateTaskGraph(original)
	if !graphsEqual(original, before) {
		t.Fatal("validator modified the input graph")
	}
}

func TestStatusValidators(t *testing.T) {
	for _, s := range []string{"planned", "running", "awaiting_input", "blocked", "failed", "completed"} {
		if !IsValidGraphStatus(s) {
			t.Fatalf("expected %q to be valid graph status", s)
		}
	}
	if IsValidGraphStatus("bogus") {
		t.Fatal("expected bogus to be invalid graph status")
	}

	for _, s := range []string{"pending", "ready", "running", "awaiting_input", "blocked", "failed", "completed", "skipped"} {
		if !IsValidNodeStatus(s) {
			t.Fatalf("expected %q to be valid node status", s)
		}
	}
	if IsValidNodeStatus("") {
		t.Fatal("expected empty string to be invalid node status")
	}
	if IsValidNodeStatus("bogus") {
		t.Fatal("expected bogus to be invalid node status")
	}
}

func TestHelperMethods(t *testing.T) {
	g := &TaskGraph{
		Nodes: []TaskGraphNode{
			{ID: "a", Type: NodeTypeModel, Goal: "x"},
			{ID: "b", Type: NodeTypeTool, Goal: "y", Executor: "z"},
		},
	}
	if n := g.NodeByID("a"); n == nil || n.Goal != "x" {
		t.Fatal("NodeByID failed for existing node")
	}
	if n := g.NodeByID("c"); n != nil {
		t.Fatal("NodeByID should return nil for missing node")
	}
	ids := g.NodeIDs()
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("NodeIDs returned %v", ids)
	}
}

func TestJSONRoundTrip_SaveLoadGraph(t *testing.T) {
	dir := t.TempDir()
	store := Store{dir: filepath.Join(dir, "sessions")}

	task := &TaskNode{
		ID:     "task-graph-1",
		Goal:   "test graph persistence",
		Status: "running",
		Graph: &TaskGraph{
			ID:     "g1",
			TaskID: "task-graph-1",
			Status: GraphStatusPlanned,
			Nodes: []TaskGraphNode{
				{
					ID:         "n1",
					Type:       NodeTypeModel,
					Goal:       "answer",
					Status:     NodeStatusPending,
					Acceptance: Acceptance{Criteria: "correct answer", Verified: false},
				},
				{
					ID:       "n2",
					Type:     NodeTypeTool,
					Goal:     "read file",
					Status:   NodeStatusPending,
					Executor: "file.read",
					Depends:  []string{"n1"},
					EvidenceRefs: []EvidenceRef{
						{Kind: "trace", TraceID: "t1", ToolName: "file.read", Summary: "read ok"},
					},
				},
			},
		},
	}
	state := State{
		Key:        "test-key",
		ActiveTask: task.ID,
		Tasks:      []TaskNode{*task},
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load("test-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(loaded.Tasks))
	}
	loadedTask := loaded.Tasks[0]
	if loadedTask.Graph == nil {
		t.Fatal("graph was not persisted")
	}
	if loadedTask.Graph.ID != "g1" {
		t.Fatalf("graph ID not preserved: got %q", loadedTask.Graph.ID)
	}
	if loadedTask.Graph.TaskID != "task-graph-1" {
		t.Fatalf("task ID not preserved: got %q", loadedTask.Graph.TaskID)
	}
	if len(loadedTask.Graph.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(loadedTask.Graph.Nodes))
	}
	n1 := loadedTask.Graph.Nodes[0]
	if n1.ID != "n1" || n1.Type != NodeTypeModel || n1.Goal != "answer" {
		t.Fatalf("node n1 corrupted: %+v", n1)
	}
	n2 := loadedTask.Graph.Nodes[1]
	if n2.ID != "n2" || n2.Executor != "file.read" {
		t.Fatalf("node n2 corrupted: %+v", n2)
	}
	if len(n2.Depends) != 1 || n2.Depends[0] != "n1" {
		t.Fatalf("node n2 depends corrupted: %v", n2.Depends)
	}
	if len(n2.EvidenceRefs) != 1 || n2.EvidenceRefs[0].Summary != "read ok" {
		t.Fatalf("evidence refs corrupted: %v", n2.EvidenceRefs)
	}
	if !n1.Acceptance.CriteriaContains("correct") {
		t.Fatalf("acceptance criteria corrupted: %+v", n1.Acceptance)
	}
}

func TestJSONRoundTrip_AcceptanceEvidence(t *testing.T) {
	data, err := json.Marshal(TaskGraphNode{
		ID:   "n1",
		Type: NodeTypeModel,
		Goal: "test",
		Acceptance: Acceptance{
			Criteria: "must be correct",
			Verified: true,
			Reason:   "passed checks",
		},
		EvidenceRefs: []EvidenceRef{
			{Kind: "trace", TraceID: "t1", TracePath: "/tmp/t1.jsonl", ToolName: "bash", Summary: "ok"},
			{Kind: "tool", ToolName: "file.read", Summary: "read 100 lines"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var node TaskGraphNode
	if err := json.Unmarshal(data, &node); err != nil {
		t.Fatal(err)
	}
	if !node.Acceptance.Verified || node.Acceptance.Criteria != "must be correct" {
		t.Fatalf("acceptance not round-tripped: %+v", node.Acceptance)
	}
	if len(node.EvidenceRefs) != 2 || node.EvidenceRefs[1].Summary != "read 100 lines" {
		t.Fatalf("evidence refs not round-tripped: %+v", node.EvidenceRefs)
	}
}

func TestTransitionTo_RunningIncrementsAttempts(t *testing.T) {
	n := &TaskGraphNode{ID: "n1", Status: NodeStatusPending, Attempts: 0}
	n.TransitionTo(NodeStatusRunning)
	if n.Status != NodeStatusRunning {
		t.Fatalf("expected running, got %q", n.Status)
	}
	if n.Attempts != 1 {
		t.Fatalf("expected attempts=1, got %d", n.Attempts)
	}
	n.TransitionTo(NodeStatusRunning)
	if n.Attempts != 2 {
		t.Fatalf("expected attempts=2, got %d", n.Attempts)
	}
}

func TestTransitionTo_VerifyingKeepsAttempts(t *testing.T) {
	n := &TaskGraphNode{ID: "n1", Status: NodeStatusRunning, Attempts: 1}
	n.TransitionTo(NodeStatusVerifying)
	if n.Status != NodeStatusVerifying {
		t.Fatalf("expected verifying, got %q", n.Status)
	}
	if n.Attempts != 1 {
		t.Fatalf("attempts should not change, got %d", n.Attempts)
	}
}

func TestSetCompleted_Verified(t *testing.T) {
	n := &TaskGraphNode{ID: "n1", Status: NodeStatusRunning, Acceptance: Acceptance{Criteria: "check"}}
	n.SetCompleted(true, "criteria met")
	if n.Status != NodeStatusCompleted {
		t.Fatalf("expected completed, got %q", n.Status)
	}
	if !n.Acceptance.Verified {
		t.Fatal("verified should be true")
	}
	if n.Acceptance.Reason != "criteria met" {
		t.Fatalf("reason: %q", n.Acceptance.Reason)
	}
	if n.VerifiedAt.IsZero() {
		t.Fatal("verified_at should be set")
	}
}

func TestSetCompleted_Unverified(t *testing.T) {
	n := &TaskGraphNode{ID: "n1", Status: NodeStatusRunning}
	n.SetCompleted(false, "")
	if n.Status != NodeStatusCompleted {
		t.Fatalf("expected completed, got %q", n.Status)
	}
	if n.Acceptance.Verified {
		t.Fatal("verified should be false")
	}
	if !n.VerifiedAt.IsZero() {
		t.Fatal("verified_at should not be set when not verified")
	}
}

func TestSetFailed(t *testing.T) {
	n := &TaskGraphNode{ID: "n1", Status: NodeStatusRunning}
	n.SetFailed("something broke")
	if n.Status != NodeStatusFailed {
		t.Fatalf("expected failed, got %q", n.Status)
	}
	if n.FailureReason != "something broke" {
		t.Fatalf("failure_reason: %q", n.FailureReason)
	}
}

func TestSetBlocked(t *testing.T) {
	n := &TaskGraphNode{ID: "n1", Status: NodeStatusRunning}
	n.SetBlocked("permission denied")
	if n.Status != NodeStatusBlocked {
		t.Fatalf("expected blocked, got %q", n.Status)
	}
	if n.FailureReason != "permission denied" {
		t.Fatalf("failure_reason: %q", n.FailureReason)
	}
}

func TestIsTerminal(t *testing.T) {
	for _, s := range []string{NodeStatusCompleted, NodeStatusFailed, NodeStatusBlocked, NodeStatusSkipped} {
		n := &TaskGraphNode{Status: s}
		if !n.IsTerminal() {
			t.Fatalf("%s should be terminal", s)
		}
	}
	for _, s := range []string{NodeStatusPending, NodeStatusReady, NodeStatusRunning, NodeStatusVerifying, NodeStatusRetrying, NodeStatusNeedsReplan, NodeStatusAwaitingInput} {
		n := &TaskGraphNode{Status: s}
		if n.IsTerminal() {
			t.Fatalf("%s should not be terminal", s)
		}
	}
}

func TestIsActive(t *testing.T) {
	for _, s := range []string{NodeStatusRunning, NodeStatusVerifying, NodeStatusRetrying} {
		n := &TaskGraphNode{Status: s}
		if !n.IsActive() {
			t.Fatalf("%s should be active", s)
		}
	}
	for _, s := range []string{NodeStatusPending, NodeStatusReady, NodeStatusCompleted, NodeStatusFailed, NodeStatusBlocked, NodeStatusNeedsReplan, NodeStatusAwaitingInput} {
		n := &TaskGraphNode{Status: s}
		if n.IsActive() {
			t.Fatalf("%s should not be active", s)
		}
	}
}

func TestValidTypeModeCombos(t *testing.T) {
	if !IsValidTypeModeCombo(NodeTypeSubtask, NodeModeDirect) {
		t.Fatal("subtask/direct should be valid")
	}
	if !IsValidTypeModeCombo(NodeTypeHumanConfirm, NodeModeHuman) {
		t.Fatal("human_confirm/human should be valid")
	}
	if IsValidTypeModeCombo(NodeTypeHumanConfirm, NodeModeReact) {
		t.Fatal("human_confirm/react should be invalid")
	}
	if !IsValidTypeModeCombo(NodeTypeTool, "") {
		t.Fatal("tool with empty mode should be valid (backward compat)")
	}
}

func TestJSONRoundTrip_NewStatusesAndModes(t *testing.T) {
	node := TaskGraphNode{
		ID:            "n1",
		Type:          NodeTypeSubtask,
		Mode:          NodeModeReact,
		Goal:          "analyze",
		Status:        NodeStatusVerifying,
		Attempts:      2,
		ResultSummary: "partial",
		AllowedTools:  []string{"file.read", "terminal.run"},
		EvidenceRefs:  []EvidenceRef{{Kind: "tool", ToolName: "file.read", Summary: "read 10 files"}},
		Acceptance:    Acceptance{Criteria: "complete", Verified: false},
	}
	data, err := json.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	var loaded TaskGraphNode
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Mode != NodeModeReact {
		t.Fatalf("mode not preserved: %q", loaded.Mode)
	}
	if loaded.Status != NodeStatusVerifying {
		t.Fatalf("status not preserved: %q", loaded.Status)
	}
	if loaded.Attempts != 2 {
		t.Fatalf("attempts not preserved: %d", loaded.Attempts)
	}
	if len(loaded.AllowedTools) != 2 {
		t.Fatalf("allowed_tools not preserved: %v", loaded.AllowedTools)
	}
	if len(loaded.EvidenceRefs) != 1 || loaded.EvidenceRefs[0].ToolName != "file.read" {
		t.Fatalf("evidence_refs not preserved: %v", loaded.EvidenceRefs)
	}
}

func TestJSONRoundTrip_StoreArchivePreservesGraph(t *testing.T) {
	dir := t.TempDir()
	store := Store{dir: filepath.Join(dir, "sessions")}

	task := TaskNode{
		ID:     "task-archive-1",
		Goal:   "archive test",
		Status: "completed",
		Graph: &TaskGraph{
			ID:     "ga1",
			TaskID: "task-archive-1",
			Status: GraphStatusCompleted,
			Nodes: []TaskGraphNode{
				{ID: "n1", Type: NodeTypeModel, Goal: "done", Status: NodeStatusCompleted},
			},
		},
	}
	state := State{
		Key:   "archive-key",
		Tasks: []TaskNode{task},
	}
	archivePath, err := store.Archive(state)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	var archived State
	if err := json.Unmarshal(data, &archived); err != nil {
		t.Fatal(err)
	}
	if len(archived.Tasks) != 1 || archived.Tasks[0].Graph == nil {
		t.Fatal("graph not preserved in archive")
	}
	if archived.Tasks[0].Graph.ID != "ga1" {
		t.Fatalf("graph ID not preserved in archive: got %q", archived.Tasks[0].Graph.ID)
	}
}

func deepCopyGraph(t *testing.T, g *TaskGraph) *TaskGraph {
	t.Helper()
	data, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	var copy TaskGraph
	if err := json.Unmarshal(data, &copy); err != nil {
		t.Fatal(err)
	}
	return &copy
}

func graphsEqual(a, b *TaskGraph) bool {
	adata, _ := json.Marshal(a)
	bdata, _ := json.Marshal(b)
	return string(adata) == string(bdata)
}

func (a Acceptance) CriteriaContains(sub string) bool {
	return containsAny(a.Criteria, sub)
}

func containsAny(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && containsSubstring(s, sub)
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
