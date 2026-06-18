package runtime

import (
	"context"
	"encoding/json"
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

func TestExecuteNode_ToolNodeEvidenceIncludesStructuredFields(t *testing.T) {
	rt := newTestRuntime(t)
	tmpDir := t.TempDir()
	target := filepath.Join(tmpDir, "input.txt")
	g := newTestGraph(session.TaskGraphNode{
		ID:       "write-input",
		Type:     session.NodeTypeTool,
		Goal:     "write input file",
		Status:   session.NodeStatusPending,
		Executor: "file.write",
		Input: map[string]any{
			"path":    target,
			"content": "alpha beta\ngamma delta\nepsilon",
		},
	})
	node := g.NodeByID("write-input")

	err := rt.executeNode(t.Context(), inbound("cli:test", "write file"), &session.State{}, g, node, "write file", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusCompleted {
		t.Fatalf("expected completed, got %q: %s", node.Status, node.FailureReason)
	}
	if len(node.EvidenceRefs) != 1 {
		t.Fatalf("expected 1 evidence ref, got %d", len(node.EvidenceRefs))
	}
	summary := node.EvidenceRefs[0].Summary
	for _, want := range []string{"path=" + target, "bytes=30", "sha256=", "content_preview=alpha beta"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("expected evidence summary to contain %q, got %q", want, summary)
		}
	}
}

func TestSummarizeToolEvidenceIncludesTerminalCommandWithoutOutput(t *testing.T) {
	summary := summarizeToolEvidence(agentcore.ToolResult{
		Content: "",
		Evidence: map[string]any{
			"command":               "mkdir -p /tmp/example",
			"decision":              "allowed",
			"policy_classification": "safe",
			"elapsed_ms":            int64(12),
			"output_truncated":      false,
		},
	})
	for _, want := range []string{
		"command completed successfully with no output",
		"command=mkdir -p /tmp/example",
		"decision=allowed",
		"elapsed_ms=12",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("expected summary to contain %q, got %q", want, summary)
		}
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
	if err := os.WriteFile(filepath.Join(dir, ".mateway", "metadata.yaml"), []byte(`adapter_version: "2"
source: "test"
installed_at: "2026-06-17T00:00:00Z"
tool_runtime: "mateway"
graph:
  mode: "adapted"
  type: "prompt"
  stage: "execution"
  granularity: "workflow"
`), 0o644); err != nil {
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
	if !strings.Contains(state.Pending.Question, "please review the deployment plan") {
		t.Fatalf("expected question text, got %q", state.Pending.Question)
	}
	if !strings.Contains(state.Pending.Question, "Reply 1 to confirm") {
		t.Fatalf("expected numeric confirmation guidance, got %q", state.Pending.Question)
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
	if !strings.Contains(state.Pending.Question, "verify output matches requirements") {
		t.Fatalf("expected acceptance criteria as question, got %q", state.Pending.Question)
	}
	if !strings.Contains(state.Pending.Question, "Reply 1 to confirm") {
		t.Fatalf("expected numeric confirmation guidance, got %q", state.Pending.Question)
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

func TestBuildToolCallFromNode_PreservesStructuredArgs(t *testing.T) {
	node := &session.TaskGraphNode{
		ID:       "write",
		Executor: "file.write",
		Input: map[string]any{
			"path":    "/tmp/hello.txt",
			"content": "hello task graph",
		},
	}
	call := buildToolCallFromNode(node, "file.write")
	if call.Name != "file.write" {
		t.Fatalf("expected file.write, got %q", call.Name)
	}
	if call.Args["path"] != "/tmp/hello.txt" || call.Args["content"] != "hello task graph" {
		t.Fatalf("structured args not preserved: %#v", call.Args)
	}
}

func TestRenderModelNodeInputIncludesGoalAndDependencies(t *testing.T) {
	g := newTestGraph(
		session.TaskGraphNode{
			ID:            "run",
			Type:          session.NodeTypeTool,
			Status:        session.NodeStatusCompleted,
			ResultSummary: "3 5 /tmp/input.txt",
		},
		session.TaskGraphNode{
			ID:      "report",
			Type:    session.NodeTypeModel,
			Goal:    "Synthesize the run output and present the line/word counts",
			Depends: []string{"run"},
		},
	)
	node := g.NodeByID("report")

	input := renderModelNodeInput(g, node, "count the file")
	for _, want := range []string{
		"Current node goal:",
		"Synthesize the run output",
		"Original user request:",
		"count the file",
		"Completed dependency results:",
		"run: 3 5 /tmp/input.txt",
		"Produce only the output needed",
	} {
		if !strings.Contains(input, want) {
			t.Fatalf("expected model node input to contain %q, got %q", want, input)
		}
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

func TestExecuteNode_DirectNodeIncludesAgentProfilePrompt(t *testing.T) {
	var capturedSystemPrompt string
	var callCount int
	captureModel := captureModel{next: func(_ context.Context, c agentcore.Context) (agentcore.Message, error) {
		callCount++
		if callCount == 1 {
			capturedSystemPrompt = c.SystemPrompt
			return agentcore.Message{Role: agentcore.RoleAssistant, Content: "我是小代。"}, nil
		}
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: `{"status":"passed","reason":"identity answered","confidence":"high"}`}, nil
	}}
	rt := newTestRuntime(t)
	rt.Model = captureModel
	profileDir := filepath.Join(rt.home(), "workspace", "agents", "main")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "soul.md"), []byte("# Soul\n\n你是 小代，也是用户的个人 AI 工作助理。"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := newTestGraph(session.TaskGraphNode{
		ID:     "answer-name",
		Type:   session.NodeTypeSubtask,
		Mode:   session.NodeModeDirect,
		Goal:   "answer your name",
		Status: session.NodeStatusPending,
		Acceptance: session.Acceptance{
			Criteria: "answer with the assistant name",
		},
	})
	node := g.NodeByID("answer-name")

	err := rt.executeNode(t.Context(), inbound("cli:test", "你叫什么"), &session.State{}, g, node, "你叫什么", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(capturedSystemPrompt, "你是 小代") {
		t.Fatalf("system prompt should include agent soul identity, got:\n%s", capturedSystemPrompt)
	}
	if !strings.Contains(capturedSystemPrompt, "answer your name") {
		t.Fatal("system prompt missing node goal")
	}
}

func TestExecuteNode_DirectNodeIncludesContextHookMessages(t *testing.T) {
	var capturedMessages []agentcore.Message
	var callCount int
	captureModel := captureModel{next: func(_ context.Context, c agentcore.Context) (agentcore.Message, error) {
		callCount++
		if callCount == 1 {
			capturedMessages = append([]agentcore.Message(nil), c.Messages...)
			return agentcore.Message{Role: agentcore.RoleAssistant, Content: "use remembered preference"}, nil
		}
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: `{"status":"passed","reason":"preference used","confidence":"high"}`}, nil
	}}
	rt := newTestRuntime(t)
	rt.Model = captureModel
	rt.Hooks.Providers = append([]HookProvider{testContextHookProvider{text: "Relevant memory snippets:\n- user prefers bullet points"}}, rt.Hooks.Providers...)

	g := newTestGraph(session.TaskGraphNode{
		ID:     "answer",
		Type:   session.NodeTypeSubtask,
		Mode:   session.NodeModeDirect,
		Goal:   "answer with remembered preference",
		Status: session.NodeStatusPending,
		Acceptance: session.Acceptance{
			Criteria: "uses remembered preference",
		},
	})
	node := g.NodeByID("answer")

	err := rt.executeNode(t.Context(), inbound("cli:test", "use my preference"), &session.State{}, g, node, "use my preference", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(capturedMessages) == 0 {
		t.Fatal("expected model messages")
	}
	if capturedMessages[0].Role != agentcore.RoleSystem || !strings.Contains(capturedMessages[0].Content, "user prefers bullet points") {
		t.Fatalf("expected context hook system message first, got %#v", capturedMessages)
	}
}

func TestExecuteNode_DirectNodeIncludesReferencedTaskContext(t *testing.T) {
	var capturedMessages []agentcore.Message
	var callCount int
	captureModel := captureModel{next: func(_ context.Context, c agentcore.Context) (agentcore.Message, error) {
		callCount++
		if callCount == 1 {
			capturedMessages = append([]agentcore.Message(nil), c.Messages...)
			return agentcore.Message{Role: agentcore.RoleAssistant, Content: "历史结果说明 runtime 负责调度任务图。"}, nil
		}
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: `{"status":"passed","reason":"context used","confidence":"high"}`}, nil
	}}
	rt := newTestRuntime(t)
	rt.Model = captureModel

	historyGraph := &session.TaskGraph{
		ID:     "g-history",
		TaskID: "task-history",
		Status: session.GraphStatusCompleted,
		Nodes: []session.TaskGraphNode{{
			ID:            "inspect",
			Type:          session.NodeTypeSubtask,
			Mode:          session.NodeModeDirect,
			Goal:          "inspect runtime package",
			Status:        session.NodeStatusCompleted,
			ResultSummary: "runtime 负责调度 TaskGraph 并执行 node",
			Output:        map[string]any{"text": "runtime 负责调度 TaskGraph 并执行 node"},
			Acceptance:    session.Acceptance{Verified: true},
		}},
	}
	currentGraph := newTestGraph(session.TaskGraphNode{
		ID:     "summarize",
		Type:   session.NodeTypeSubtask,
		Mode:   session.NodeModeDirect,
		Goal:   "summarize previous result",
		Status: session.NodeStatusPending,
		Acceptance: session.Acceptance{
			Criteria: "uses previous task result",
		},
	})
	state := &session.State{Tasks: []session.TaskNode{
		{ID: "task-history", Goal: "inspect runtime", Status: "completed", Summary: "runtime package inspected", Graph: historyGraph},
		{ID: currentGraph.TaskID, Goal: "summarize previous result", Status: "running", Graph: currentGraph, Execution: session.ExecutionFrame{ContextRefs: []string{"task-history"}}},
	}}
	node := currentGraph.NodeByID("summarize")

	err := rt.executeNode(t.Context(), inbound("cli:test", "基于刚才的结果总结一句话"), state, currentGraph, node, "基于刚才的结果总结一句话", nil)
	if err != nil {
		t.Fatal(err)
	}
	var combined strings.Builder
	for _, msg := range capturedMessages {
		combined.WriteString(msg.Content)
		combined.WriteString("\n")
	}
	text := combined.String()
	if !strings.Contains(text, "[referenced_task_context]") || !strings.Contains(text, "runtime 负责调度 TaskGraph") {
		t.Fatalf("expected referenced task context in model messages, got:\n%s", text)
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
	rt.Config.Execution.ModelVerifier = "always"
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

func TestExecuteNode_ToolNode_DefaultVerifierSkipsModelWhenDeterministicPasses(t *testing.T) {
	rt := newTestRuntime(t)
	modelCalled := false
	rt.Model = captureModel{next: func(_ context.Context, _ agentcore.Context) (agentcore.Message, error) {
		modelCalled = true
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: `{"status":"blocked","reason":"should not run","confidence":"low"}`}, nil
	}}

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
	if modelCalled {
		t.Fatal("default verifier should not call model when deterministic verification passes")
	}
	if node.Status != session.NodeStatusCompleted {
		t.Fatalf("expected completed, got %q", node.Status)
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
	rt.Config.Execution.ModelVerifier = "always"
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

func TestRunGraphTask_NodeVerifierRetryThenPasses(t *testing.T) {
	var executeCalls int
	var verifyCalls int
	rt := newTestRuntime(t)
	rt.Config.Execution.ModelVerifier = "always"
	rt.Model = captureModel{next: func(_ context.Context, c agentcore.Context) (agentcore.Message, error) {
		if c.SystemPrompt == modelVerifierSystemPrompt {
			verifyCalls++
			if verifyCalls == 1 {
				return agentcore.Message{Role: agentcore.RoleAssistant, Content: `{"status":"retry","reason":"missing detail","retryable":true,"feedback_for_next_attempt":"include detail","confidence":"medium"}`}, nil
			}
			return agentcore.Message{Role: agentcore.RoleAssistant, Content: `{"status":"passed","reason":"detail included","confidence":"high"}`}, nil
		}
		executeCalls++
		if executeCalls == 1 {
			return agentcore.Message{Role: agentcore.RoleAssistant, Content: "too short"}, nil
		}
		if !strings.Contains(c.SystemPrompt, "include detail") {
			t.Fatalf("expected retry feedback in next attempt prompt, got %q", c.SystemPrompt)
		}
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: "answer with detail"}, nil
	}}
	g := newTestGraph(session.TaskGraphNode{
		ID:     "answer",
		Type:   session.NodeTypeModel,
		Mode:   session.NodeModeDirect,
		Goal:   "answer",
		Status: session.NodeStatusPending,
		Acceptance: session.Acceptance{
			Criteria: "include detail",
		},
	})
	task := &session.TaskNode{ID: g.TaskID, Goal: "answer", Graph: g}
	state := &session.State{Tasks: []session.TaskNode{*task}, ActiveTask: task.ID}
	trace := newTestTraceRecorder(t)

	if _, err := rt.runGraphTask(t.Context(), inbound("cli:test", "answer"), state, &state.Tasks[0], "answer", trace); err != nil {
		t.Fatal(err)
	}
	node := state.Tasks[0].Graph.NodeByID("answer")
	if node.Status != session.NodeStatusCompleted {
		t.Fatalf("expected completed after retry, got %q: %s", node.Status, node.FailureReason)
	}
	if node.Attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", node.Attempts)
	}
	if !node.Acceptance.Verified {
		t.Fatal("expected node verified")
	}
}

func TestRunGraphTask_NodeVerifierRetryExhausted(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Config.Execution.ModelVerifier = "always"
	rt.Model = captureModel{next: func(_ context.Context, c agentcore.Context) (agentcore.Message, error) {
		if c.SystemPrompt == modelVerifierSystemPrompt {
			return agentcore.Message{Role: agentcore.RoleAssistant, Content: `{"status":"retry","reason":"still missing","retryable":true,"confidence":"medium"}`}, nil
		}
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: "incomplete"}, nil
	}}
	g := newTestGraph(session.TaskGraphNode{
		ID:     "answer",
		Type:   session.NodeTypeModel,
		Mode:   session.NodeModeDirect,
		Goal:   "answer",
		Status: session.NodeStatusPending,
		Input:  map[string]any{"max_attempts": 2},
		Acceptance: session.Acceptance{
			Criteria: "must pass verifier",
		},
	})
	task := &session.TaskNode{ID: g.TaskID, Goal: "answer", Graph: g}
	state := &session.State{Tasks: []session.TaskNode{*task}, ActiveTask: task.ID}
	trace := newTestTraceRecorder(t)

	if _, err := rt.runGraphTask(t.Context(), inbound("cli:test", "answer"), state, &state.Tasks[0], "answer", trace); err != nil {
		t.Fatal(err)
	}
	if node := state.Tasks[0].Graph.NodeByID("answer"); node != nil {
		t.Fatalf("expected exhausted node to be replaced by local replan, got status %q", node.Status)
	}
	repair := state.Tasks[0].Graph.NodeByID("repair-answer")
	if repair == nil {
		t.Fatalf("expected repair node after local replan, nodes=%v", state.Tasks[0].Graph.NodeIDs())
	}
	if repair.Status != session.NodeStatusFailed {
		t.Fatalf("expected bounded repair failure after retry exhaustion, got %q", repair.Status)
	}
	if repair.Attempts != 2 {
		t.Fatalf("expected 2 repair attempts, got %d", repair.Attempts)
	}
	events := readTraceFile(t, trace.path)
	if !traceHasEvent(events, "node_retry_exhausted") {
		t.Fatal("expected node_retry_exhausted trace event")
	}
	if !traceHasEvent(events, "local_replan_applied") {
		t.Fatal("expected local_replan_applied trace event")
	}
	if !traceHasEvent(events, "local_replan_limit_reached") {
		t.Fatal("expected local_replan_limit_reached trace event")
	}
	if session.ReadyNodes(state.Tasks[0].Graph, 1) != nil {
		t.Fatalf("failed repair node should not be ready again")
	}
}

func TestExecuteNode_DirectMode_SingleModelCall(t *testing.T) {
	var callCount int
	model := captureModel{next: func(_ context.Context, _ agentcore.Context) (agentcore.Message, error) {
		callCount++
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: "direct answer"}, nil
	}}
	rt := newTestRuntime(t)
	rt.Model = model
	rt.Tools.Register(runtimeNamedTool{name: "file.read", content: "data"})

	g := newTestGraph(session.TaskGraphNode{
		ID:     "answer",
		Type:   session.NodeTypeModel,
		Mode:   session.NodeModeDirect,
		Goal:   "answer",
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("answer")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if callCount != 1 {
		t.Fatalf("expected 1 model call, got %d", callCount)
	}
	if node.Status != session.NodeStatusCompleted {
		t.Fatalf("expected completed, got %q", node.Status)
	}
	if node.ResultSummary == "" {
		t.Fatal("expected result summary")
	}
}

func TestExecuteNode_DirectMode_IgnoresToolCallsFromModel(t *testing.T) {
	var toolInvocations int
	tool := &countingTool{name: "file.read", content: "data", calls: &toolInvocations}
	model := captureModel{next: func(_ context.Context, _ agentcore.Context) (agentcore.Message, error) {
		return agentcore.Message{
			Role:    agentcore.RoleAssistant,
			Content: "",
			ToolCalls: []agentcore.ToolCall{{
				ID:   "call_1",
				Name: "file.read",
				Args: map[string]any{"path": "/etc/passwd"},
			}},
		}, nil
	}}
	rt := newTestRuntime(t)
	rt.Model = model
	rt.Tools.Register(tool)

	g := newTestGraph(session.TaskGraphNode{
		ID:     "answer",
		Type:   session.NodeTypeModel,
		Mode:   session.NodeModeDirect,
		Goal:   "answer",
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("answer")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusCompleted {
		t.Fatalf("direct mode should not fail when model returns tool calls, got %q: %s", node.Status, node.FailureReason)
	}
	if toolInvocations != 0 {
		t.Fatalf("direct mode must not invoke tools, got %d invocations", toolInvocations)
	}
	if !strings.Contains(node.ResultSummary, "ignored tool call") {
		t.Fatalf("expected summary to mention ignored tool call, got %q", node.ResultSummary)
	}
	if len(node.EvidenceRefs) != 0 {
		t.Fatalf("direct mode should not produce evidence refs, got %d", len(node.EvidenceRefs))
	}
}

func TestExecuteNode_DirectMode_NoToolRegistryInteraction(t *testing.T) {
	var toolsSeen []string
	model := captureModel{next: func(_ context.Context, c agentcore.Context) (agentcore.Message, error) {
		for _, t := range c.Tools {
			toolsSeen = append(toolsSeen, t.Name())
		}
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: "ok"}, nil
	}}
	rt := newTestRuntime(t)
	rt.Model = model
	rt.Tools.Register(runtimeNamedTool{name: "file.read", content: "data"})
	rt.Tools.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})

	g := newTestGraph(session.TaskGraphNode{
		ID:     "answer",
		Type:   session.NodeTypeModel,
		Mode:   session.NodeModeDirect,
		Goal:   "answer",
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("answer")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(toolsSeen) != 0 {
		t.Fatalf("expected no tools in direct mode, got %v", toolsSeen)
	}
}

func TestExecuteNode_ReactMode_UsesAgentCoreLoop(t *testing.T) {
	model := newReactStepModel(
		agentcore.Message{
			Role:    agentcore.RoleAssistant,
			Content: "let me read the file",
			ToolCalls: []agentcore.ToolCall{{
				ID:   "tc-1",
				Name: "file.read",
				Args: map[string]any{"path": "/tmp/x"},
			}},
		},
		agentcore.Message{
			Role:    agentcore.RoleAssistant,
			Content: "the file says: hello",
		},
	)
	rt := newTestRuntime(t)
	rt.Model = model
	rt.Tools.Register(runtimeNamedTool{name: "file.read", content: "hello"})

	g := newTestGraph(session.TaskGraphNode{
		ID:           "react",
		Type:         session.NodeTypeSubtask,
		Mode:         session.NodeModeReact,
		Goal:         "read and summarize",
		Status:       session.NodeStatusPending,
		AllowedTools: []string{"file.read"},
	})
	node := g.NodeByID("react")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 2 {
		t.Fatalf("expected 2 model calls (1 tool call + 1 final), got %d", model.calls)
	}
	if node.Status != session.NodeStatusCompleted {
		t.Fatalf("expected completed, got %q: %s", node.Status, node.FailureReason)
	}
	if node.ResultSummary == "" {
		t.Fatal("expected result summary")
	}
}

func TestExecuteNode_ReactMode_AllowedToolsFilter(t *testing.T) {
	model := newReactStepModel(
		agentcore.Message{
			Role:    agentcore.RoleAssistant,
			Content: "calling allowed",
			ToolCalls: []agentcore.ToolCall{{
				ID:   "tc-1",
				Name: "file.read",
				Args: map[string]any{"path": "/tmp/x"},
			}},
		},
		agentcore.Message{
			Role:    agentcore.RoleAssistant,
			Content: "done",
		},
	)
	rt := newTestRuntime(t)
	rt.Model = model
	rt.Tools.Register(runtimeNamedTool{name: "file.read", content: "hello"})
	rt.Tools.Register(runtimeNamedTool{name: "terminal.run", content: "ok"})

	g := newTestGraph(session.TaskGraphNode{
		ID:           "react",
		Type:         session.NodeTypeSubtask,
		Mode:         session.NodeModeReact,
		Goal:         "use only file.read",
		Status:       session.NodeStatusPending,
		AllowedTools: []string{"file.read"},
	})
	node := g.NodeByID("react")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(model.toolsSeen) < 1 {
		t.Fatal("expected at least one model call")
	}
	for _, tools := range model.toolsSeen {
		for _, toolName := range tools {
			if toolName == "terminal.run" {
				t.Fatalf("terminal.run should not be visible in allowed_tools filter, got %v", tools)
			}
		}
	}
}

func TestExecuteNode_ReactMode_ToolCallsBecomeEvidence(t *testing.T) {
	model := newReactStepModel(
		agentcore.Message{
			Role:    agentcore.RoleAssistant,
			Content: "reading",
			ToolCalls: []agentcore.ToolCall{{
				ID:   "tc-1",
				Name: "file.read",
				Args: map[string]any{"path": "/tmp/x"},
			}},
		},
		agentcore.Message{
			Role:    agentcore.RoleAssistant,
			Content: "got the contents",
		},
	)
	rt := newTestRuntime(t)
	rt.Model = model
	rt.Tools.Register(runtimeNamedTool{name: "file.read", content: "raw content"})

	g := newTestGraph(session.TaskGraphNode{
		ID:           "react",
		Type:         session.NodeTypeSubtask,
		Mode:         session.NodeModeReact,
		Goal:         "read and report",
		Status:       session.NodeStatusPending,
		AllowedTools: []string{"file.read"},
	})
	node := g.NodeByID("react")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(node.EvidenceRefs) != 1 {
		t.Fatalf("expected 1 evidence ref, got %d", len(node.EvidenceRefs))
	}
	if node.EvidenceRefs[0].ToolName != "file.read" {
		t.Fatalf("expected tool name file.read, got %q", node.EvidenceRefs[0].ToolName)
	}
	if node.EvidenceRefs[0].Kind != "tool" {
		t.Fatalf("expected kind=tool, got %q", node.EvidenceRefs[0].Kind)
	}
}

func TestExecuteNode_ReactMode_ToolPolicyStillApplies(t *testing.T) {
	model := newReactStepModel(
		agentcore.Message{
			Role:    agentcore.RoleAssistant,
			Content: "trying",
			ToolCalls: []agentcore.ToolCall{{
				ID:   "tc-1",
				Name: "file.read",
				Args: map[string]any{"path": "/etc/passwd"},
			}},
		},
		agentcore.Message{
			Role:    agentcore.RoleAssistant,
			Content: "got it",
		},
	)
	rt := newTestRuntime(t)
	rt.Model = model
	rt.Tools.Register(runtimeNamedTool{name: "file.read", content: "ok"})

	g := newTestGraph(session.TaskGraphNode{
		ID:           "react",
		Type:         session.NodeTypeSubtask,
		Mode:         session.NodeModeReact,
		Goal:         "try to read",
		Status:       session.NodeStatusPending,
		AllowedTools: []string{"file.read"},
	})
	node := g.NodeByID("react")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(node.EvidenceRefs) != 1 {
		t.Fatalf("expected 1 evidence ref (policy+redaction pipeline runs even for react), got %d", len(node.EvidenceRefs))
	}
	if node.EvidenceRefs[0].ToolName != "file.read" {
		t.Fatalf("expected tool name file.read, got %q", node.EvidenceRefs[0].ToolName)
	}
}

func TestExecuteNode_ReactMode_NoAllowedTools_RunsAsLoop(t *testing.T) {
	model := newReactStepModel(
		agentcore.Message{
			Role:    agentcore.RoleAssistant,
			Content: "answering directly",
		},
	)
	rt := newTestRuntime(t)
	rt.Model = model
	rt.Tools.Register(runtimeNamedTool{name: "file.read", content: "ok"})

	g := newTestGraph(session.TaskGraphNode{
		ID:     "react",
		Type:   session.NodeTypeSubtask,
		Mode:   session.NodeModeReact,
		Goal:   "answer without tools",
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("react")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 {
		t.Fatalf("expected 1 model call (no tools needed), got %d", model.calls)
	}
	if node.Status != session.NodeStatusCompleted {
		t.Fatalf("expected completed, got %q", node.Status)
	}
}

func TestExecuteNode_NodeStartedTraceEvent(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = staticTextModel{text: "answer"}

	g := newTestGraph(session.TaskGraphNode{
		ID:     "n1",
		Type:   session.NodeTypeModel,
		Mode:   session.NodeModeDirect,
		Goal:   "answer",
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("n1")

	tmpDir := t.TempDir()
	traceFile := filepath.Join(tmpDir, "trace.jsonl")
	trace := &traceRecorder{id: "test-trace", path: traceFile, base: map[string]any{}}

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", trace)
	if err != nil {
		t.Fatal(err)
	}
	if !node.Acceptance.Verified {
		t.Fatal("expected verified")
	}

	data, err := os.ReadFile(traceFile)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var foundNodeStarted, foundTaskID, foundGraphID, foundNodeID, foundAttempt bool
	for _, line := range lines {
		var payload map[string]any
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			t.Fatalf("bad json: %v", err)
		}
		if payload["type"] == "node_started" {
			foundNodeStarted = true
			if _, ok := payload["task_id"]; ok {
				foundTaskID = true
			}
			if _, ok := payload["graph_id"]; ok {
				foundGraphID = true
			}
			if _, ok := payload["node_id"]; ok {
				foundNodeID = true
			}
			if _, ok := payload["attempt"]; ok {
				foundAttempt = true
			}
		}
	}
	if !foundNodeStarted {
		t.Fatal("expected node_started event in trace")
	}
	if !foundTaskID {
		t.Fatal("expected task_id in node_started event")
	}
	if !foundGraphID {
		t.Fatal("expected graph_id in node_started event")
	}
	if !foundNodeID {
		t.Fatal("expected node_id in node_started event")
	}
	if !foundAttempt {
		t.Fatal("expected attempt in node_started event")
	}
}

func TestExecuteNode_ReactMode_ToolTraceHasRequiredFields(t *testing.T) {
	model := newReactStepModel(
		agentcore.Message{
			Role:    agentcore.RoleAssistant,
			Content: "reading",
			ToolCalls: []agentcore.ToolCall{{
				ID: "tc-1", Name: "file.read", Args: map[string]any{"path": "/tmp/x"},
			}},
		},
		agentcore.Message{Role: agentcore.RoleAssistant, Content: "done"},
	)
	rt := newTestRuntime(t)
	rt.Model = model
	rt.Tools.Register(runtimeNamedTool{name: "file.read", content: "data"})

	g := newTestGraph(session.TaskGraphNode{
		ID: "react", Type: session.NodeTypeSubtask, Mode: session.NodeModeReact,
		Goal: "read", Status: session.NodeStatusPending, AllowedTools: []string{"file.read"},
	})
	node := g.NodeByID("react")

	tmpDir := t.TempDir()
	traceFile := filepath.Join(tmpDir, "trace.jsonl")
	trace := &traceRecorder{id: "tt", path: traceFile, base: map[string]any{}}

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", trace)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(traceFile)
	if err != nil {
		t.Fatalf("failed to read trace file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	requiredFields := []string{"task_id", "graph_id", "node_id", "attempt"}
	for _, eventType := range []string{"node_tool_call", "node_tool_result"} {
		found := false
		for _, line := range lines {
			var evt map[string]any
			if err := json.Unmarshal([]byte(line), &evt); err != nil {
				t.Fatalf("bad trace json: %v", err)
			}
			if evt["type"] != eventType {
				continue
			}
			found = true
			for _, f := range requiredFields {
				if _, ok := evt[f]; !ok {
					t.Fatalf("%s missing field %q", eventType, f)
				}
			}
		}
		if !found {
			t.Fatalf("expected %s event in trace", eventType)
		}
	}
}

func TestExecuteNode_ToolNode_ToolTraceHasRequiredFields(t *testing.T) {
	rt := newTestRuntime(t)
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := newTestGraph(session.TaskGraphNode{
		ID: "readme", Type: session.NodeTypeTool, Goal: "read",
		Status: session.NodeStatusPending, Executor: "file.read",
		Input: map[string]any{"path": filepath.Join(tmpDir, "README.md")},
	})
	node := g.NodeByID("readme")

	traceFile := filepath.Join(tmpDir, "trace.jsonl")
	trace := &traceRecorder{id: "tt", path: traceFile, base: map[string]any{}}

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", trace)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(traceFile)
	if err != nil {
		t.Fatalf("failed to read trace file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	requiredFields := []string{"task_id", "graph_id", "node_id", "attempt"}
	for _, eventType := range []string{"node_tool_call", "node_tool_result"} {
		found := false
		for _, line := range lines {
			var evt map[string]any
			if err := json.Unmarshal([]byte(line), &evt); err != nil {
				t.Fatalf("bad trace json: %v", err)
			}
			if evt["type"] != eventType {
				continue
			}
			found = true
			for _, f := range requiredFields {
				if _, ok := evt[f]; !ok {
					t.Fatalf("%s missing field %q", eventType, f)
				}
			}
		}
		if !found {
			t.Fatalf("expected %s event in trace", eventType)
		}
	}
}

func TestExecuteNode_UsesTransitionTo_IncrementsAttempts(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = staticTextModel{text: "ok"}

	g := newTestGraph(session.TaskGraphNode{
		ID:       "n1",
		Type:     session.NodeTypeModel,
		Mode:     session.NodeModeDirect,
		Goal:     "answer",
		Status:   session.NodeStatusPending,
		Attempts: 0,
	})
	node := g.NodeByID("n1")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Attempts != 1 {
		t.Fatalf("expected attempts=1 (incremented by TransitionTo), got %d", node.Attempts)
	}
}

func TestExecuteNode_UnknownMode_FailsConcretely(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = staticTextModel{text: "ok"}

	g := newTestGraph(session.TaskGraphNode{
		ID:     "n1",
		Type:   session.NodeTypeModel,
		Mode:   "unsupported_mode",
		Goal:   "answer",
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("n1")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusFailed {
		t.Fatalf("expected failed for unsupported mode, got %q", node.Status)
	}
	if !strings.Contains(node.FailureReason, "unsupported mode") {
		t.Fatalf("expected reason to mention unsupported mode, got %q", node.FailureReason)
	}
}

func TestExecuteNode_ScriptMode_DelegatesToToolExecutor(t *testing.T) {
	rt := newTestRuntime(t)
	tmpDir := t.TempDir()
	target := filepath.Join(tmpDir, "script.txt")
	rt.Tools.Register(runtimeNamedTool{name: "script.run", content: "ran"})

	g := newTestGraph(session.TaskGraphNode{
		ID:       "script-node",
		Type:     session.NodeTypeTool,
		Mode:     session.NodeModeScript,
		Goal:     "run script",
		Status:   session.NodeStatusPending,
		Executor: "script.run",
		Input:    map[string]any{"path": target},
	})
	node := g.NodeByID("script-node")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusCompleted {
		t.Fatalf("expected completed for script mode, got %q: %s", node.Status, node.FailureReason)
	}
}

func TestExecuteNode_ModeEmptyTypeDispatchStillWorks(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = staticTextModel{text: "model answer"}

	g := newTestGraph(session.TaskGraphNode{
		ID:     "mode-empty",
		Type:   session.NodeTypeModel,
		Goal:   "answer",
		Status: session.NodeStatusPending,
	})
	node := g.NodeByID("mode-empty")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if node.Status != session.NodeStatusCompleted {
		t.Fatalf("expected completed via type dispatch, got %q", node.Status)
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

type testContextHookProvider struct {
	text string
}

func (p testContextHookProvider) Name() string { return "test_context" }

func (p testContextHookProvider) ContextHook(context.Context, ContextHookInput) (ContextHookResult, error) {
	return ContextHookResult{SystemContextSections: []ContextSection{{
		Name:    "test_context",
		Source:  "test",
		Content: p.text,
	}}}, nil
}

type reactStepModel struct {
	responses []agentcore.Message
	calls     int
	toolsSeen [][]string
}

func (m *reactStepModel) Next(_ context.Context, c agentcore.Context) (agentcore.Message, error) {
	toolNames := make([]string, 0, len(c.Tools))
	for _, t := range c.Tools {
		toolNames = append(toolNames, t.Name())
	}
	m.toolsSeen = append(m.toolsSeen, toolNames)
	if m.calls >= len(m.responses) {
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: "fallback final"}, nil
	}
	resp := m.responses[m.calls]
	m.calls++
	return resp, nil
}

func newReactStepModel(responses ...agentcore.Message) *reactStepModel {
	return &reactStepModel{responses: responses}
}

type countingTool struct {
	name    string
	content string
	calls   *int
}

func (t *countingTool) Name() string        { return t.name }
func (t *countingTool) Description() string { return "counting test tool" }
func (t *countingTool) Schema() agentcore.Schema {
	return agentcore.Schema{}
}
func (t *countingTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (t *countingTool) Run(_ context.Context, call agentcore.ToolCall) agentcore.ToolResult {
	if t.calls != nil {
		*t.calls++
	}
	return agentcore.ToolResult{ToolCallID: call.ID, Content: t.content, Evidence: map[string]any{"called": true}}
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
	if granularity == "atomic" {
		granularity = "subtask"
	}
	dir := filepath.Join(workspace, "skills", name)
	if err := os.MkdirAll(filepath.Join(dir, ".mateway"), 0o755); err != nil {
		t.Fatal(err)
	}
	metaContent := fmt.Sprintf(`adapter_version: "2"
source: "test"
installed_at: "2026-06-17T00:00:00Z"
tool_runtime: "mateway"
graph:
  mode: "adapted"
  type: "prompt"
  stage: "execution"
  granularity: "%s"
`, granularity)
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
