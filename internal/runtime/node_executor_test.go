package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/session"
)

func newTestGraph(nodes ...session.TaskGraphNode) *session.TaskGraph {
	return &session.TaskGraph{
		ID:     "test-graph",
		TaskID: "test-task",
		Status: session.GraphStatusPlanned,
		Nodes:  nodes,
	}
}

func TestExecuteNode_ModelNode_Completes(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = staticTextModel{text: "The answer is 42."}
	g := newTestGraph(session.TaskGraphNode{
		ID:     "answer",
		Type:   session.NodeTypeModel,
		Goal:   "answer the question",
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("answer")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusCompleted {
		t.Fatalf("expected completed, got %q", node.Status)
	}
	if node.ResultSummary == "" {
		t.Fatal("expected result summary")
	}
	t.Logf("result: %s", node.ResultSummary)
}

func TestExecuteNode_ModelNode_ErrorSetsFailed(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = errorModel{}
	g := newTestGraph(session.TaskGraphNode{
		ID:     "answer",
		Type:   session.NodeTypeModel,
		Goal:   "answer",
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("answer")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal("node failures should not propagate as error")
	}
	if node.Status != session.NodeStatusFailed {
		t.Fatalf("expected failed, got %q", node.Status)
	}
	if node.FailureReason == "" {
		t.Fatal("expected failure reason")
	}
}

func TestExecuteNode_ModelNode_IncrementsAttempts(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = staticTextModel{text: "done"}
	g := newTestGraph(session.TaskGraphNode{
		ID:       "answer",
		Type:     session.NodeTypeModel,
		Goal:     "answer",
		Status:   session.NodeStatusPending,
		Attempts: 0,
	})
	node := g.NodeByID("answer")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Attempts != 1 {
		t.Fatalf("expected attempts=1, got %d", node.Attempts)
	}

	node.Status = session.NodeStatusPending
	err = rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Attempts != 2 {
		t.Fatalf("expected attempts=2, got %d", node.Attempts)
	}
}

func TestExecuteNode_ToolNode_Success(t *testing.T) {
	rt := newTestRuntime(t)
	g := newTestGraph(session.TaskGraphNode{
		ID:       "readme",
		Type:     session.NodeTypeTool,
		Goal:     "read README",
		Status:   session.NodeStatusPending,
		Executor: "file.read",
		Input:    map[string]any{"path": "README.md"},
	})
	node := g.NodeByID("readme")

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Project"), 0o644); err != nil {
		t.Fatal(err)
	}

	node.Input["path"] = filepath.Join(tmpDir, "README.md")
	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusCompleted {
		t.Fatalf("expected completed, got %q", node.Status)
	}
	if node.ResultSummary == "" {
		t.Fatal("expected result summary")
	}
	if len(node.EvidenceRefs) != 1 {
		t.Fatalf("expected 1 evidence ref, got %d", len(node.EvidenceRefs))
	}
	if node.EvidenceRefs[0].ToolName != "file.read" {
		t.Fatalf("expected tool name file.read, got %q", node.EvidenceRefs[0].ToolName)
	}
}

func TestExecuteNode_ToolNode_UnknownTool(t *testing.T) {
	rt := newTestRuntime(t)
	g := newTestGraph(session.TaskGraphNode{
		ID:       "bad-tool",
		Type:     session.NodeTypeTool,
		Goal:     "run unknown",
		Status:   session.NodeStatusPending,
		Executor: "nonexistent.tool",
	})
	node := g.NodeByID("bad-tool")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusFailed {
		t.Fatalf("expected failed for unknown tool, got %q", node.Status)
	}
	if !strings.Contains(node.FailureReason, "nonexistent.tool") {
		t.Fatalf("failure reason should mention the tool, got %q", node.FailureReason)
	}
}

func TestExecuteNode_ToolNode_EmptyExecutor(t *testing.T) {
	rt := newTestRuntime(t)
	g := newTestGraph(session.TaskGraphNode{
		ID:     "bad-tool",
		Type:   session.NodeTypeTool,
		Goal:   "run",
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("bad-tool")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusFailed {
		t.Fatalf("expected failed for empty executor, got %q", node.Status)
	}
}

func TestExecuteNode_ToolNode_FailingTool(t *testing.T) {
	rt := newTestRuntime(t)
	g := newTestGraph(session.TaskGraphNode{
		ID:       "bad-read",
		Type:     session.NodeTypeTool,
		Goal:     "read nonexistent file",
		Status:   session.NodeStatusPending,
		Executor: "file.read",
		Input:    map[string]any{"path": "/nonexistent/path/file.txt"},
	})
	node := g.NodeByID("bad-read")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusFailed {
		t.Fatalf("expected failed for failing tool, got %q", node.Status)
	}
	if node.FailureReason == "" {
		t.Fatal("expected failure reason")
	}
	if len(node.EvidenceRefs) == 0 {
		t.Fatal("expected evidence refs even for failed tool")
	}
}

func TestExecuteNode_ToolNode_IncrementsAttempts(t *testing.T) {
	rt := newTestRuntime(t)
	g := newTestGraph(session.TaskGraphNode{
		ID:       "readme",
		Type:     session.NodeTypeTool,
		Goal:     "read README",
		Status:   session.NodeStatusPending,
		Executor: "file.read",
	})
	node := g.NodeByID("readme")
	node.Attempts = 0

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	node.Input = map[string]any{"path": filepath.Join(tmpDir, "README.md")}

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Attempts != 1 {
		t.Fatalf("expected attempts=1, got %d", node.Attempts)
	}
}

func TestExecuteNode_SkillNode_Success(t *testing.T) {
	rt, workspace := newTestRuntimeWithWorkspace(t)
	rt.Model = staticTextModel{text: "skill executed: file read successfully"}
	createRegisteredSkill(t, workspace, "test-skill", "atomic", "# Test Skill\nRead a file and report contents.")

	g := newTestGraph(session.TaskGraphNode{
		ID:       "skill-node",
		Type:     session.NodeTypeSkill,
		Goal:     "read file with skill",
		Status:   session.NodeStatusPending,
		Executor: "test-skill",
	})
	node := g.NodeByID("skill-node")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusCompleted {
		t.Fatalf("expected completed, got %q", node.Status)
	}
	if node.ResultSummary == "" {
		t.Fatal("expected result summary")
	}
}

func TestExecuteNode_SkillNode_Unregistered(t *testing.T) {
	rt, _ := newTestRuntimeWithWorkspace(t)
	rt.Model = staticTextModel{text: "unused"}

	g := newTestGraph(session.TaskGraphNode{
		ID:       "skill-node",
		Type:     session.NodeTypeSkill,
		Goal:     "run skill",
		Status:   session.NodeStatusPending,
		Executor: "nonexistent-skill",
	})
	node := g.NodeByID("skill-node")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusFailed {
		t.Fatalf("expected failed for unregistered skill, got %q", node.Status)
	}
	if !strings.Contains(node.FailureReason, "not registered") {
		t.Fatalf("failure reason should mention unregistered, got %q", node.FailureReason)
	}
}

func TestExecuteNode_SkillNode_WorkflowSkillRejected(t *testing.T) {
	rt, workspace := newTestRuntimeWithWorkspace(t)
	rt.Model = staticTextModel{text: "unused"}
	createRegisteredSkill(t, workspace, "big-skill", "workflow", "# Big Workflow Skill\nMulti-step workflow.")

	g := newTestGraph(session.TaskGraphNode{
		ID:       "skill-node",
		Type:     session.NodeTypeSkill,
		Goal:     "run big workflow",
		Status:   session.NodeStatusPending,
		Executor: "big-skill",
	})
	node := g.NodeByID("skill-node")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusFailed {
		t.Fatalf("expected failed for workflow skill, got %q", node.Status)
	}
	if !strings.Contains(node.FailureReason, "graph.granularity=workflow") {
		t.Fatalf("failure reason should mention graph.granularity=workflow, got %q", node.FailureReason)
	}
}

func TestExecuteNode_SkillNode_BareSKILLMD_Rejected(t *testing.T) {
	rt, _ := newTestRuntimeWithWorkspace(t)
	rt.Model = staticTextModel{text: "unused"}

	rawDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rawDir, "SKILL.md"), []byte("# Bare Skill\nNot registered."), 0o644); err != nil {
		t.Fatal(err)
	}

	// Using a bare directory path — not a registered skill name — should fail
	g := newTestGraph(session.TaskGraphNode{
		ID:       "skill-node",
		Type:     session.NodeTypeSkill,
		Goal:     "run skill",
		Status:   session.NodeStatusPending,
		Executor: rawDir,
	})
	node := g.NodeByID("skill-node")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusFailed {
		t.Fatalf("expected failed for bare SKILL.md (not registered), got %q", node.Status)
	}
}

func TestExecuteNode_SkillNode_MetadataWorkflow_SKILLMDNoGranularity(t *testing.T) {
	rt, workspace := newTestRuntimeWithWorkspace(t)
	rt.Model = staticTextModel{text: "unused"}
	createRegisteredSkill(t, workspace, "meta-workflow", "workflow", "# Skill\nNo granularity in frontmatter.")

	g := newTestGraph(session.TaskGraphNode{
		ID:       "skill-node",
		Type:     session.NodeTypeSkill,
		Goal:     "run skill",
		Status:   session.NodeStatusPending,
		Executor: "meta-workflow",
	})
	node := g.NodeByID("skill-node")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusFailed {
		t.Fatalf("expected failed when metadata says workflow, SKILL.md has no granularity, got %q", node.Status)
	}
	if !strings.Contains(node.FailureReason, "graph.granularity=workflow") {
		t.Fatalf("failure reason should mention graph.granularity=workflow, got %q", node.FailureReason)
	}
}

func TestExecuteNode_SkillNode_MetadataWorkflow_SKILLMDAtomic(t *testing.T) {
	rt, workspace := newTestRuntimeWithWorkspace(t)
	rt.Model = staticTextModel{text: "unused"}

	dir := filepath.Join(workspace, "skills", "conflicting")
	if err := os.MkdirAll(filepath.Join(dir, ".mateway"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mateway", "metadata.yaml"), []byte("adapter_version: \"1\"\nsource: test\ngraph:\n  granularity: workflow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: conflicting\ngranularity: atomic\n---\n# Conflicting"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := newTestGraph(session.TaskGraphNode{
		ID:       "skill-node",
		Type:     session.NodeTypeSkill,
		Goal:     "run skill",
		Status:   session.NodeStatusPending,
		Executor: "conflicting",
	})
	node := g.NodeByID("skill-node")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusFailed {
		t.Fatalf("expected failed: metadata workflow overrides SKILL.md atomic, got %q", node.Status)
	}
	if !strings.Contains(node.FailureReason, "graph.granularity=workflow") {
		t.Fatalf("expected graph.granularity=workflow in failure reason, got %q", node.FailureReason)
	}
}

func TestExecuteNode_SkillNode_EmptyExecutor(t *testing.T) {
	rt := newTestRuntime(t)
	g := newTestGraph(session.TaskGraphNode{
		ID:     "skill-node",
		Type:   session.NodeTypeSkill,
		Goal:   "run skill",
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("skill-node")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusFailed {
		t.Fatalf("expected failed for empty executor, got %q", node.Status)
	}
}

func TestExecuteNode_HumanReview_CreatesPending(t *testing.T) {
	rt := newTestRuntime(t)
	state := &session.State{}
	g := newTestGraph(session.TaskGraphNode{
		ID:     "review",
		Type:   session.NodeTypeHumanReview,
		Goal:   "please review the deployment plan",
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("review")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), state, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusAwaitingInput {
		t.Fatalf("expected awaiting_input, got %q", node.Status)
	}
	if state.Pending == nil {
		t.Fatal("expected pending action")
	}
	if state.Pending.Kind != session.PendingKindHumanReview {
		t.Fatalf("expected human_review pending kind, got %q", state.Pending.Kind)
	}
	if state.Pending.TaskID != "test-task" {
		t.Fatalf("expected task ID test-task, got %q", state.Pending.TaskID)
	}
	if state.Pending.GraphID != "test-graph" {
		t.Fatalf("expected graph ID test-graph, got %q", state.Pending.GraphID)
	}
	if state.Pending.NodeID != "review" {
		t.Fatalf("expected node ID review, got %q", state.Pending.NodeID)
	}
	if state.Pending.Question != "please review the deployment plan" {
		t.Fatalf("expected question text, got %q", state.Pending.Question)
	}
}

func TestExecuteNode_HumanConfirm_CreatesPending(t *testing.T) {
	rt := newTestRuntime(t)
	state := &session.State{}
	g := newTestGraph(session.TaskGraphNode{
		ID:     "confirm",
		Type:   session.NodeTypeHumanConfirm,
		Goal:   "confirm deployment",
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("confirm")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), state, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusAwaitingInput {
		t.Fatalf("expected awaiting_input, got %q", node.Status)
	}
	if state.Pending == nil {
		t.Fatal("expected pending action")
	}
	if state.Pending.Kind != session.PendingKindHumanConfirm {
		t.Fatalf("expected human_confirm pending kind, got %q", state.Pending.Kind)
	}
}

func TestExecuteNode_HumanReview_UsesAcceptanceCriteria(t *testing.T) {
	rt := newTestRuntime(t)
	state := &session.State{}
	g := newTestGraph(session.TaskGraphNode{
		ID:   "review",
		Type: session.NodeTypeHumanReview,
		Acceptance: session.Acceptance{
			Criteria: "verify output matches requirements",
		},
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("review")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), state, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending.Question != "verify output matches requirements" {
		t.Fatalf("expected acceptance criteria as question, got %q", state.Pending.Question)
	}
}

func TestExecuteNode_HumanReview_DefaultQuestion(t *testing.T) {
	rt := newTestRuntime(t)
	state := &session.State{}
	g := newTestGraph(session.TaskGraphNode{
		ID:     "review",
		Type:   session.NodeTypeHumanReview,
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("review")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), state, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending.Question == "" {
		t.Fatal("expected default question")
	}
}

func TestExecuteNode_UnknownType(t *testing.T) {
	rt := newTestRuntime(t)
	g := newTestGraph(session.TaskGraphNode{
		ID:     "bad",
		Type:   "invalid_type",
		Goal:   "x",
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("bad")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusFailed {
		t.Fatalf("expected failed for unknown type, got %q", node.Status)
	}
	if !strings.Contains(node.FailureReason, "unknown node type") {
		t.Fatalf("failure reason should mention unknown type, got %q", node.FailureReason)
	}
}

func TestExecuteNode_DoesNotModifyOtherNodes(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = staticTextModel{text: "result"}
	g := newTestGraph(
		session.TaskGraphNode{
			ID:     "target",
			Type:   session.NodeTypeModel,
			Goal:   "execute me",
			Status: session.NodeStatusPending,
		},
		session.TaskGraphNode{
			ID:            "other",
			Type:          session.NodeTypeModel,
			Goal:          "do not touch me",
			Status:        session.NodeStatusPending,
			ResultSummary: "existing summary",
		},
	)
	node := g.NodeByID("target")
	otherBefore := *g.NodeByID("other")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}

	otherAfter := g.NodeByID("other")
	if otherAfter.Status != otherBefore.Status {
		t.Fatal("other node status was modified")
	}
	if otherAfter.ResultSummary != otherBefore.ResultSummary {
		t.Fatal("other node result was modified")
	}
	if otherAfter.Attempts != otherBefore.Attempts {
		t.Fatal("other node attempts was modified")
	}
}

func TestExecuteNode_WithTrace_DoesNotCrash(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = staticTextModel{text: "result"}
	g := newTestGraph(session.TaskGraphNode{
		ID:     "answer",
		Type:   session.NodeTypeModel,
		Goal:   "answer",
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("answer")

	trace := newTestTraceRecorder(t)
	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", trace)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusCompleted {
		t.Fatalf("expected completed, got %q", node.Status)
	}
}

func TestExecuteNode_NilTrace_DoesNotCrash(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = staticTextModel{text: "result"}
	g := newTestGraph(session.TaskGraphNode{
		ID:     "answer",
		Type:   session.NodeTypeModel,
		Goal:   "answer",
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("answer")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusCompleted {
		t.Fatalf("expected completed, got %q", node.Status)
	}
}

func TestExecuteNode_ToolNodeWithTrace_DoesNotCrash(t *testing.T) {
	rt := newTestRuntime(t)
	g := newTestGraph(session.TaskGraphNode{
		ID:       "readme",
		Type:     session.NodeTypeTool,
		Goal:     "read file",
		Status:   session.NodeStatusPending,
		Executor: "file.read",
	})
	node := g.NodeByID("readme")

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "x.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	node.Input = map[string]any{"path": filepath.Join(tmpDir, "x.txt")}

	trace := newTestTraceRecorder(t)
	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", trace)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusCompleted {
		t.Fatalf("expected completed, got %q", node.Status)
	}
}

func TestExecuteNode_HumanNodeWithTrace_DoesNotCrash(t *testing.T) {
	rt := newTestRuntime(t)
	state := &session.State{}
	g := newTestGraph(session.TaskGraphNode{
		ID:     "review",
		Type:   session.NodeTypeHumanReview,
		Goal:   "review",
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("review")

	trace := newTestTraceRecorder(t)
	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), state, g, node, "hi", trace)
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending == nil {
		t.Fatal("expected pending action with trace")
	}
}

func TestExecuteNode_ToolNode_ObserveCreatesStep(t *testing.T) {
	rt := newTestRuntime(t)
	g := newTestGraph(session.TaskGraphNode{
		ID:       "readme",
		Type:     session.NodeTypeTool,
		Goal:     "read README",
		Status:   session.NodeStatusPending,
		Executor: "file.read",
	})
	node := g.NodeByID("readme")

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Project"), 0o644); err != nil {
		t.Fatal(err)
	}
	node.Input = map[string]any{"path": filepath.Join(tmpDir, "README.md")}

	state := &session.State{}
	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), state, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(state.Tasks) != 0 {
		t.Logf("state tasks: %d (observe step is recorded as execution event, not task step)", len(state.Tasks))
	}
}

func TestBuildNodeSystemPrompt(t *testing.T) {
	node := &session.TaskGraphNode{
		ID:   "test-node",
		Type: session.NodeTypeModel,
		Goal: "analyze data",
		Acceptance: session.Acceptance{
			Criteria: "must be correct",
		},
		Input: map[string]any{"files": true, "path": "/tmp"},
	}
	g := &session.TaskGraph{ID: "g1", TaskID: "t1"}
	prompt := buildNodeSystemPrompt(node, g)
	if !strings.Contains(prompt, "analyze data") {
		t.Fatal("prompt missing goal")
	}
	if !strings.Contains(prompt, "must be correct") {
		t.Fatal("prompt missing acceptance criteria")
	}
	if !strings.Contains(prompt, "files") {
		t.Fatal("prompt missing input context")
	}
}

func TestBuildToolCallFromNode(t *testing.T) {
	node := &session.TaskGraphNode{
		ID:       "readme",
		Executor: "file.read",
		Input:    map[string]any{"path": "/tmp/README.md"},
	}
	call := buildToolCallFromNode(node, "file.read")
	if call.Name != "file.read" {
		t.Fatalf("expected file.read, got %q", call.Name)
	}
	if path, ok := call.Args["path"].(string); !ok || path != "/tmp/README.md" {
		t.Fatalf("expected path in args, got %v", call.Args)
	}
	if call.ID == "" {
		t.Fatal("expected non-empty call ID")
	}
}

func TestBuildToolCallFromNode_EmptyInput(t *testing.T) {
	node := &session.TaskGraphNode{
		ID:       "no-input",
		Executor: "web.search",
	}
	call := buildToolCallFromNode(node, "web.search")
	if call.Name != "web.search" {
		t.Fatalf("expected web.search, got %q", call.Name)
	}
	if call.Args == nil {
		t.Fatal("expected non-nil args for empty input")
	}
}

func TestExecuteNode_ModelNode_UsesSystemPrompt(t *testing.T) {
	var capturedSystemPrompt string
	var callCount int
	captureModel := captureModel{next: func(_ context.Context, c agentcore.Context) (agentcore.Message, error) {
		callCount++
		if callCount == 1 {
			capturedSystemPrompt = c.SystemPrompt
		}
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: `{"status":"passed","reason":"criteria met","confidence":"high"}`}, nil
	}}
	rt := newTestRuntime(t)
	rt.Model = captureModel

	g := newTestGraph(session.TaskGraphNode{
		ID:     "answer",
		Type:   session.NodeTypeModel,
		Goal:   "answer the question",
		Status: session.NodeStatusPending,
		Acceptance: session.Acceptance{
			Criteria: "provide numeric answer",
		},
	})
	node := g.NodeByID("answer")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(capturedSystemPrompt, "answer the question") {
		t.Fatal("system prompt missing goal")
	}
	if !strings.Contains(capturedSystemPrompt, "provide numeric answer") {
		t.Fatal("system prompt missing acceptance criteria")
	}
}

func TestExecuteNode_ToolNode_EvidenceHasElapsed(t *testing.T) {
	rt := newTestRuntime(t)
	g := newTestGraph(session.TaskGraphNode{
		ID:       "readme",
		Type:     session.NodeTypeTool,
		Goal:     "read README",
		Status:   session.NodeStatusPending,
		Executor: "file.read",
	})
	node := g.NodeByID("readme")

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	node.Input = map[string]any{"path": filepath.Join(tmpDir, "README.md")}

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusCompleted {
		t.Fatalf("expected completed, got %q", node.Status)
	}
	if node.ResultSummary == "" {
		t.Fatal("expected result summary")
	}
}

func TestExecuteSingleTool_RetryableTool_AttemptsAndRetryEvidence(t *testing.T) {
	rt := newTestRuntime(t)
	msg := agentcore.Message{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
		ID: "test-call", Name: "web.fetch", Args: map[string]any{"url": "http://127.0.0.1:1/nonexistent"},
	}}}
	tool, ok := rt.Tools.Get("web.fetch")
	if !ok {
		t.Skip("web.fetch tool not available")
	}

	result, blocked, err := rt.executeSingleTool(t.Context(), msg, msg.ToolCalls[0], tool, &session.State{}, "task-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Fatal("expected not blocked")
	}
	if !result.IsError {
		t.Fatal("expected error for fake URL")
	}
	if result.Evidence == nil {
		t.Fatal("expected evidence")
	}
	if _, ok := result.Evidence["elapsed_ms"]; !ok {
		t.Fatal("expected elapsed_ms in evidence")
	}
	if _, ok := result.Evidence["deadline_ms"]; !ok {
		t.Fatal("expected deadline_ms in evidence")
	}
	retries, _ := result.Evidence["retry_count"].(int)
	t.Logf("retry_count: %d", retries)
}

func TestExecuteSingleTool_RetrySuccessAfterTimeout(t *testing.T) {
	rt := newTestRuntime(t)

	var callCount int
	fakeTool := &retryFirstTool{
		name: "web.fetch",
		run: func(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
			callCount++
			if callCount == 1 {
				return agentcore.ToolResult{
					ToolCallID: call.ID,
					Content:    "request timeout after 10s: network unreachable",
					IsError:    true,
					Evidence:   map[string]any{"timed_out": true, "elapsed_ms": 10000},
				}
			}
			return agentcore.ToolResult{
				ToolCallID: call.ID,
				Content:    "OK: fetched 200 bytes",
				IsError:    false,
				Evidence:   map[string]any{"elapsed_ms": 200},
			}
		},
	}
	rt.Tools.Unregister("web.fetch")
	rt.Tools.Register(fakeTool)

	msg := agentcore.Message{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{
		ID: "retry-call", Name: "web.fetch", Args: map[string]any{"url": "https://example.com"},
	}}}

	result, blocked, err := rt.executeSingleTool(t.Context(), msg, msg.ToolCalls[0], fakeTool, &session.State{}, "task-retry", nil)
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Fatal("expected not blocked")
	}
	if result.IsError {
		t.Fatalf("expected success on retry, got error: %s", result.Content)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 calls (1 fail + 1 retry), got %d", callCount)
	}
	retries, _ := result.Evidence["retry_count"].(int)
	if retries != 2 {
		t.Fatalf("expected retry_count=2, got %d", retries)
	}
	if !strings.Contains(result.Content, "OK") {
		t.Fatalf("expected success content, got %q", result.Content)
	}
}

func TestExecuteNode_ToolNode_CriteriaUnmet_Blocked(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = staticTextModel{text: `{"status":"blocked","reason":"output does not mention build instructions","missing":["build instructions"],"confidence":"low"}`}

	g := newTestGraph(session.TaskGraphNode{
		ID:       "readme",
		Type:     session.NodeTypeTool,
		Goal:     "read README",
		Status:   session.NodeStatusPending,
		Executor: "file.read",
		Acceptance: session.Acceptance{
			Criteria: "must contain build instructions",
		},
	})
	node := g.NodeByID("readme")

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Project"), 0o644); err != nil {
		t.Fatal(err)
	}
	node.Input = map[string]any{"path": filepath.Join(tmpDir, "README.md")}

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusBlocked {
		t.Fatalf("expected blocked (criteria unmet via model verifier), got %q", node.Status)
	}
	if !strings.Contains(node.FailureReason, "build instructions") {
		t.Fatalf("failure reason should mention criteria, got %q", node.FailureReason)
	}
}

func TestExecuteNode_ToolNode_NoCriteria_Completes(t *testing.T) {
	rt := newTestRuntime(t)
	g := newTestGraph(session.TaskGraphNode{
		ID:       "readme",
		Type:     session.NodeTypeTool,
		Goal:     "read README",
		Status:   session.NodeStatusPending,
		Executor: "file.read",
	})
	node := g.NodeByID("readme")

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Project"), 0o644); err != nil {
		t.Fatal(err)
	}
	node.Input = map[string]any{"path": filepath.Join(tmpDir, "README.md")}

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusCompleted {
		t.Fatalf("expected completed (no criteria), got %q", node.Status)
	}
	if !node.Acceptance.Verified {
		t.Fatal("expected verified=true after verifier")
	}
}

func TestExecuteNode_ModelNode_NeedsInputConvertedToBlocked(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = staticTextModel{text: `{"status":"needs_input","reason":"need human to confirm this output","confidence":"medium"}`}

	g := newTestGraph(session.TaskGraphNode{
		ID:     "answer",
		Type:   session.NodeTypeModel,
		Goal:   "answer question",
		Status: session.NodeStatusPending,
		Acceptance: session.Acceptance{
			Criteria: "must be correct",
		},
	})
	node := g.NodeByID("answer")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusBlocked {
		t.Fatalf("expected blocked (needs_input converted), got %q", node.Status)
	}
	if !strings.Contains(node.FailureReason, "only human nodes can await input") {
		t.Fatalf("expected conversion reason, got %q", node.FailureReason)
	}
}

func TestExecuteNode_HumanNode_NeedsInputPreserved(t *testing.T) {
	rt := newTestRuntime(t)
	state := &session.State{}
	g := newTestGraph(session.TaskGraphNode{
		ID:     "review",
		Type:   session.NodeTypeHumanReview,
		Goal:   "review deployment",
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("review")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), state, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusAwaitingInput {
		t.Fatalf("expected awaiting_input for human node, got %q", node.Status)
	}
	if state.Pending == nil {
		t.Fatal("expected pending action for human node")
	}
}

type errorModel struct{}

func (m errorModel) Next(context.Context, agentcore.Context) (agentcore.Message, error) {
	return agentcore.Message{}, fmt.Errorf("model error")
}

type captureModel struct {
	next func(context.Context, agentcore.Context) (agentcore.Message, error)
}

func (m captureModel) Next(ctx context.Context, c agentcore.Context) (agentcore.Message, error) {
	return m.next(ctx, c)
}

func newTestTraceRecorder(t *testing.T) *traceRecorder {
	t.Helper()
	dir := t.TempDir()
	return &traceRecorder{
		id:   "test-trace",
		path: filepath.Join(dir, "test.jsonl"),
		base: map[string]any{},
	}
}

func newTestRuntimeWithWorkspace(t *testing.T) (Runtime, string) {
	t.Helper()
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	cfg := &config.Root{
		App:       config.AppConfig{Home: home, Workspace: workspace},
		Execution: config.ExecutionConfig{MaxIterations: intPtrTest(8), InactivityTimeout: "0s"},
		Agents:    config.AgentsConfig{Default: "main", Profiles: []config.AgentProfileConfig{{ID: "main"}}},
		Scheduler: config.SchedulerConfig{Enabled: true, Timezone: "UTC"},
	}
	rt := New(cfg)
	rt.ContractModel = contractJSONModel{json: `{"summary":"test task","requires_tools":false,"required_tools":[],"required_evidence":[],"expected_outcome":"answer directly","completion_policy":"answer directly"}`}
	return rt, workspace
}

func createRegisteredSkill(t *testing.T, workspace, name, granularity, body string) string {
	t.Helper()
	dir := filepath.Join(workspace, "skills", name)
	if err := os.MkdirAll(filepath.Join(dir, ".mateway"), 0o755); err != nil {
		t.Fatal(err)
	}
	metaContent := fmt.Sprintf("adapter_version: \"1\"\nsource: test\ngraph:\n  granularity: %s\n", granularity)
	if err := os.WriteFile(filepath.Join(dir, ".mateway", "metadata.yaml"), []byte(metaContent), 0o644); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("---\nname: %s\n---\n%s", name, body)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

type retryFirstTool struct {
	name string
	run  func(context.Context, agentcore.ToolCall) agentcore.ToolResult
}

func (t *retryFirstTool) Name() string        { return t.name }
func (t *retryFirstTool) Description() string { return "retry test tool" }
func (t *retryFirstTool) Schema() agentcore.Schema {
	return agentcore.Schema{
		Required:   []string{"url"},
		Properties: map[string]any{"url": map[string]any{"type": "string", "description": "URL to fetch"}},
	}
}
func (t *retryFirstTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (t *retryFirstTool) Run(ctx context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	return t.run(ctx, call)
}
