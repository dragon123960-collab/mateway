package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/session"
)

type finalizerCountingModel struct {
	calls *int
	text  string
}

func (m *finalizerCountingModel) Next(context.Context, agentcore.Context) (agentcore.Message, error) {
	*m.calls++
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: m.text}, nil
}

func TestFinalizeGraph_Completed_UsesModelForFinalAnswer(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = staticTextModel{text: "All configs loaded successfully. The application is ready."}

	g := &session.TaskGraph{
		ID:     "g1",
		TaskID: "t1",
		Nodes: []session.TaskGraphNode{
			{ID: "read", Type: session.NodeTypeTool, Goal: "read config", Status: session.NodeStatusCompleted, Executor: "file.read", ResultSummary: "config loaded", EvidenceRefs: []session.EvidenceRef{{Kind: "tool"}}, Acceptance: session.Acceptance{Verified: true}},
			{ID: "analyze", Type: session.NodeTypeModel, Goal: "analyze config", Status: session.NodeStatusCompleted, ResultSummary: "3 entries found", Acceptance: session.Acceptance{Verified: true}},
		},
	}
	vr := session.VerifyTaskGraph(g)
	result := rt.finalizeGraph(t.Context(), g, vr, nil)
	if result.Status != session.FinalizeCompleted {
		t.Fatalf("expected completed, got %q", result.Status)
	}
	if !strings.Contains(result.ReplyText, "All configs loaded successfully") {
		t.Fatalf("expected model-generated final answer, got %q", result.ReplyText)
	}
	if result.KeepTask {
		t.Fatal("completed should not keep task")
	}
}

func TestFinalizeGraph_Blocked_NoModelCall(t *testing.T) {
	var callCount int
	rt := newTestRuntime(t)
	rt.Model = &finalizerCountingModel{calls: &callCount, text: "unused"}

	g := &session.TaskGraph{
		ID:     "g2",
		TaskID: "t2",
		Nodes: []session.TaskGraphNode{
			{ID: "read", Type: session.NodeTypeTool, Goal: "read config", Status: session.NodeStatusBlocked, Executor: "file.read", FailureReason: "permission denied"},
		},
	}
	vr := session.VerifyTaskGraph(g)
	result := rt.finalizeGraph(t.Context(), g, vr, nil)
	if callCount > 0 {
		t.Fatal("model should not be called for blocked graph")
	}
	if result.Status != session.FinalizeBlocked {
		t.Fatalf("expected blocked, got %q", result.Status)
	}
}

func TestFinalizeGraph_Failed_RecordsFailure(t *testing.T) {
	rt := newTestRuntime(t)
	g := &session.TaskGraph{
		ID:     "g3",
		TaskID: "t3",
		Nodes: []session.TaskGraphNode{
			{ID: "run", Type: session.NodeTypeTool, Goal: "run command", Status: session.NodeStatusFailed, Executor: "terminal.run", FailureReason: "command not found"},
		},
	}
	vr := session.VerifyTaskGraph(g)
	result := rt.finalizeGraph(t.Context(), g, vr, nil)
	if result.Status != session.FinalizeFailed {
		t.Fatalf("expected failed, got %q", result.Status)
	}
	if !strings.Contains(result.ReplyText, "command not found") {
		t.Fatal("reply missing failure reason")
	}
}

func TestFinalizeAndRespond_Completed_SavesStateAndClearsTask(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = staticTextModel{text: "All done."}
	msg := inbound("cli:test", "hi")

	state := &session.State{Key: "cli:test", ActiveTask: "t1"}
	task := state.StartTask("analyze config")
	state.ActiveTask = task.ID

	g := &session.TaskGraph{
		ID:     "g1",
		TaskID: task.ID,
		Nodes: []session.TaskGraphNode{
			{ID: "n1", Type: session.NodeTypeModel, Goal: "answer", Status: session.NodeStatusCompleted, ResultSummary: "done", Acceptance: session.Acceptance{Verified: true}},
		},
	}

	if err := rt.Store.Save(*state); err != nil {
		t.Fatal(err)
	}

	vr := session.VerifyTaskGraph(g)
	resp, err := rt.FinalizeAndRespond(t.Context(), msg, state, g, vr, nil)
	if err != nil {
		t.Fatal(err)
	}

	if state.ActiveTask != "" {
		t.Fatal("completed should clear ActiveTask")
	}
	if resp.Reply.Text == "" {
		t.Fatal("response should have text")
	}
	if resp.Reply.Style != "" {
		t.Fatalf("completed reply should have empty style, got %q", resp.Reply.Style)
	}
	if resp.Failed {
		t.Fatal("completed response should not be failed")
	}
}

func TestFinalizeAndRespond_AwaitingInput_CreatesPendingAction(t *testing.T) {
	rt := newTestRuntime(t)
	msg := inbound("cli:test", "hi")
	state := &session.State{Key: "cli:test", ActiveTask: "t1"}

	g := &session.TaskGraph{
		ID:     "g-await",
		TaskID: "t1",
		Nodes: []session.TaskGraphNode{
			{ID: "review", Type: session.NodeTypeHumanReview, Goal: "review deployment", Status: session.NodeStatusAwaitingInput},
		},
	}

	vr := session.VerifyTaskGraph(g)
	resp, err := rt.FinalizeAndRespond(t.Context(), msg, state, g, vr, nil)
	if err != nil {
		t.Fatal(err)
	}

	if state.Pending == nil {
		t.Fatal("awaiting_input should create PendingAction")
	}
	if state.Pending.GraphID != g.ID {
		t.Fatalf("pending GraphID=%q, want %q", state.Pending.GraphID, g.ID)
	}
	if state.Pending.TaskID != g.TaskID {
		t.Fatalf("pending TaskID=%q, want %q", state.Pending.TaskID, g.TaskID)
	}
	if state.Pending.NodeID != "review" {
		t.Fatalf("pending NodeID=%q, want review", state.Pending.NodeID)
	}
	if state.ActiveTask != "t1" {
		t.Fatal("awaiting_input should keep ActiveTask")
	}
	if resp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected input_required style, got %q", resp.Reply.Style)
	}
}

func TestFinalizeAndRespond_AwaitingInput_PreservesExistingPending(t *testing.T) {
	rt := newTestRuntime(t)
	msg := inbound("cli:test", "hi")
	state := &session.State{
		Key: "cli:test",
		Pending: &session.PendingAction{
			Kind:     session.PendingKindHumanReview,
			TaskID:   "t1",
			NodeID:   "review",
			Question: "custom question",
		},
	}

	g := &session.TaskGraph{
		ID:     "g-await",
		TaskID: "t1",
		Nodes: []session.TaskGraphNode{
			{ID: "review", Type: session.NodeTypeHumanReview, Goal: "review deployment", Status: session.NodeStatusAwaitingInput},
		},
	}

	vr := session.VerifyTaskGraph(g)
	_, err := rt.FinalizeAndRespond(t.Context(), msg, state, g, vr, nil)
	if err != nil {
		t.Fatal(err)
	}

	if state.Pending == nil {
		t.Fatal("existing pending should be preserved")
	}
	if state.Pending.Question != "custom question" {
		t.Fatalf("existing question preserved, got %q", state.Pending.Question)
	}
	if state.Pending.GraphID != g.ID {
		t.Fatalf("pending GraphID should be backfilled, got %q", state.Pending.GraphID)
	}
}

func TestFinalizeAndRespond_Blocked_GraphBlockedTrace(t *testing.T) {
	rt := newTestRuntime(t)
	msg := inbound("cli:test", "hi")
	state := &session.State{Key: "cli:test", ActiveTask: "t1"}

	g := &session.TaskGraph{
		ID:     "g-block",
		TaskID: "t1",
		Nodes: []session.TaskGraphNode{
			{ID: "read", Type: session.NodeTypeTool, Goal: "read config", Status: session.NodeStatusBlocked, Executor: "file.read", FailureReason: "permission denied"},
		},
	}

	vr := session.VerifyTaskGraph(g)
	trace := newTestTraceRecorder(t)
	resp, err := rt.FinalizeAndRespond(t.Context(), msg, state, g, vr, trace)
	if err != nil {
		t.Fatal(err)
	}

	if state.ActiveTask != "t1" {
		t.Fatal("blocked should keep ActiveTask")
	}
	if resp.Reply.Style != channel.StyleError {
		t.Fatalf("expected error style, got %q", resp.Reply.Style)
	}
	if resp.Failed {
		t.Fatal("blocked response should not be marked Failed")
	}
}

func TestRunGraphTask_CompletedGraph_FullLifecycle(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = staticTextModel{text: "Task completed. All steps finished successfully."}
	msg := inbound("cli:test", "run graph task")

	state := &session.State{Key: "cli:test"}
	task := state.StartTask("test graph execution")
	state.ActiveTask = task.ID
	task.Graph = &session.TaskGraph{
		ID:     "g-lifecycle",
		TaskID: task.ID,
		Status: session.GraphStatusPlanned,
		Nodes: []session.TaskGraphNode{
			{ID: "think", Type: session.NodeTypeModel, Goal: "analyze input", Status: session.NodeStatusPending},
		},
	}

	if err := rt.Store.Save(*state); err != nil {
		t.Fatal(err)
	}

	resp, err := rt.runGraphTask(t.Context(), msg, state, task, "run graph task", nil)
	if err != nil {
		t.Fatal(err)
	}

	if resp.Failed {
		t.Fatal("completed graph should not be failed")
	}

	updatedTask := state.TaskByID(task.ID)
	if updatedTask == nil {
		t.Fatal("task should still exist")
	}
	if updatedTask.Status != "completed" {
		t.Fatalf("task should be completed, got %q", updatedTask.Status)
	}
	if updatedTask.Graph.Nodes[0].Status != session.NodeStatusCompleted {
		t.Fatalf("node should be completed, got %q", updatedTask.Graph.Nodes[0].Status)
	}
	if !updatedTask.Graph.Nodes[0].Acceptance.Verified {
		t.Fatal("node should be verified")
	}
}

func TestRunGraphTask_FailedNode_RespondsWithFailure(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = nil
	msg := inbound("cli:test", "run graph task")

	state := &session.State{Key: "cli:test"}
	task := state.StartTask("test failed graph")
	state.ActiveTask = task.ID
	task.Graph = &session.TaskGraph{
		ID:     "g-fail",
		TaskID: task.ID,
		Status: session.GraphStatusPlanned,
		Nodes: []session.TaskGraphNode{
			{ID: "bad-tool", Type: session.NodeTypeTool, Goal: "run", Status: session.NodeStatusPending, Executor: "nonexistent.tool"},
		},
	}

	if err := rt.Store.Save(*state); err != nil {
		t.Fatal(err)
	}

	resp, err := rt.runGraphTask(t.Context(), msg, state, task, "run graph task", nil)
	if err != nil {
		t.Fatal(err)
	}

	if !resp.Failed {
		t.Fatal("failed graph should set response.Failed=true")
	}
	if resp.Reply.Style != channel.StyleError {
		t.Fatalf("expected error style, got %q", resp.Reply.Style)
	}

	updatedTask := state.TaskByID(task.ID)
	if updatedTask == nil {
		t.Fatal("task should still exist")
	}
	if updatedTask.Status != "failed" {
		t.Fatalf("task should be failed, got %q", updatedTask.Status)
	}
}

func TestHandle_GraphTask_RunsGraphLifecycle(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = staticTextModel{text: "All done."}
	rt.ContractModel = contractJSONModel{json: `{"summary":"test","requires_tools":false,"required_tools":[],"required_evidence":[],"expected_outcome":"done","completion_policy":"answer directly"}`}

	state := &session.State{Key: "cli:test"}
	task := state.StartTask("test graph lifecycle via handle")
	state.ActiveTask = task.ID
	task.Graph = &session.TaskGraph{
		ID:     "g-handle",
		TaskID: task.ID,
		Status: session.GraphStatusPlanned,
		Nodes: []session.TaskGraphNode{
			{ID: "n1", Type: session.NodeTypeModel, Goal: "answer", Status: session.NodeStatusPending},
		},
	}

	if err := rt.Store.Save(*state); err != nil {
		t.Fatal(err)
	}

	resp, err := rt.Handle(t.Context(), inbound("cli:test", "run graph lifecycle"))
	if err != nil {
		t.Fatal(err)
	}

	if resp.Failed {
		t.Fatal("graph lifecycle via handle should not fail")
	}
	if resp.Reply.Text == "" {
		t.Fatal("response should have text")
	}

	updated := loadState(t, rt, "cli:test")
	updatedTask := updated.TaskByID(task.ID)
	if updatedTask == nil {
		t.Fatal("task should exist after handle")
	}
	if updatedTask.Status != "completed" {
		t.Fatalf("task should be completed via graph lifecycle, got %q", updatedTask.Status)
	}
	if updated.ActiveTask != "" {
		t.Fatalf("ActiveTask should be cleared, got %q", updated.ActiveTask)
	}
}

func TestRunGraphTask_RespectsDependencies(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = staticTextModel{text: "done"}
	msg := inbound("cli:test", "test deps")

	state := &session.State{Key: "cli:test"}
	task := state.StartTask("test dependencies")
	state.ActiveTask = task.ID
	task.Graph = &session.TaskGraph{
		ID:     "g-deps",
		TaskID: task.ID,
		Status: session.GraphStatusPlanned,
		Nodes: []session.TaskGraphNode{
			{ID: "a", Type: session.NodeTypeTool, Goal: "read file", Status: session.NodeStatusPending, Executor: "nonexistent.tool"},
			{ID: "b", Type: session.NodeTypeModel, Goal: "analyze", Status: session.NodeStatusPending, Depends: []string{"a"}},
		},
	}

	if err := rt.Store.Save(*state); err != nil {
		t.Fatal(err)
	}

	_, err := rt.runGraphTask(t.Context(), msg, state, task, "test deps", nil)
	if err != nil {
		t.Fatal(err)
	}

	g := task.Graph

	if g.Nodes[0].Status != session.NodeStatusFailed {
		t.Fatalf("node a should be failed (unknown tool), got %q", g.Nodes[0].Status)
	}
	if g.Nodes[1].Status != session.NodeStatusPending {
		t.Fatalf("node b should still be pending (dep failed), got %q", g.Nodes[1].Status)
	}
}

func TestRunGraphTask_StopsOnBlocked(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = nil
	msg := inbound("cli:test", "test blocked")

	state := &session.State{Key: "cli:test"}
	task := state.StartTask("test blocked chain")
	state.ActiveTask = task.ID
	task.Graph = &session.TaskGraph{
		ID:     "g-blocked-chain",
		TaskID: task.ID,
		Status: session.GraphStatusPlanned,
		Nodes: []session.TaskGraphNode{
			{ID: "bad", Type: session.NodeTypeTool, Goal: "read file", Status: session.NodeStatusPending, Executor: ""},
			{ID: "next", Type: session.NodeTypeModel, Goal: "analyze", Status: session.NodeStatusPending, Depends: []string{"bad"}},
		},
	}

	if err := rt.Store.Save(*state); err != nil {
		t.Fatal(err)
	}

	_, err := rt.runGraphTask(t.Context(), msg, state, task, "test blocked", nil)
	if err != nil {
		t.Fatal(err)
	}

	g := task.Graph

	if g.Nodes[0].Status != session.NodeStatusFailed {
		t.Fatalf("node bad should be failed (empty executor), got %q", g.Nodes[0].Status)
	}
	if g.Nodes[1].Status == session.NodeStatusCompleted {
		t.Fatal("node next should NOT be completed when dep failed")
	}
}

func TestFinalizeAndRespond_AwaitingInput_UpdatesTaskStatus(t *testing.T) {
	rt := newTestRuntime(t)
	msg := inbound("cli:test", "hi")
	state := &session.State{Key: "cli:test"}
	task := state.StartTask("awaiting task")
	state.ActiveTask = task.ID

	g := &session.TaskGraph{
		ID:     "g-await-status",
		TaskID: task.ID,
		Nodes: []session.TaskGraphNode{
			{ID: "review", Type: session.NodeTypeHumanReview, Goal: "review deployment", Status: session.NodeStatusAwaitingInput},
		},
	}

	if err := rt.Store.Save(*state); err != nil {
		t.Fatal(err)
	}

	vr := session.VerifyTaskGraph(g)
	resp, err := rt.FinalizeAndRespond(t.Context(), msg, state, g, vr, nil)
	if err != nil {
		t.Fatal(err)
	}

	if resp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected input_required style, got %q", resp.Reply.Style)
	}

	updatedTask := state.TaskByID(task.ID)
	if updatedTask == nil {
		t.Fatal("task should exist")
	}
	if updatedTask.Status != "await_user_input" {
		t.Fatalf("task status should be await_user_input, got %q", updatedTask.Status)
	}
	if state.ActiveTask != task.ID {
		t.Fatal("awaiting_input should keep ActiveTask")
	}
}

func TestExecuteNode_ModelNodeWithCriteria_CallsModelTwiceAndVerifies(t *testing.T) {
	var callCount int
	var verifierPrompt string
	rt := newTestRuntime(t)
	rt.Model = captureModel{next: func(_ context.Context, c agentcore.Context) (agentcore.Message, error) {
		callCount++
		if callCount == 1 {
			return agentcore.Message{Role: agentcore.RoleAssistant, Content: "The answer is 42."}, nil
		}
		verifierPrompt = c.Messages[0].Content
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: `{"status":"passed","reason":"output satisfies the criteria","confidence":"high"}`}, nil
	}}

	g := newTestGraph(session.TaskGraphNode{
		ID:     "answer",
		Type:   session.NodeTypeModel,
		Goal:   "answer the question",
		Status: session.NodeStatusPending,
		Acceptance: session.Acceptance{
			Criteria: "must provide a numeric answer",
		},
	})
	node := g.NodeByID("answer")

	err := rt.executeNode(t.Context(), inbound("cli:test", "hi"), &session.State{}, g, node, "hi", nil)
	if err != nil {
		t.Fatal(err)
	}

	if callCount != 2 {
		t.Fatalf("expected 2 model calls (1 execute + 1 verify), got %d", callCount)
	}
	if node.Status != session.NodeStatusCompleted {
		t.Fatalf("expected completed after verification, got %q", node.Status)
	}
	if !node.Acceptance.Verified {
		t.Fatal("expected Acceptance.Verified=true after model verifier passed")
	}
	if node.ResultSummary == "" {
		t.Fatal("expected result summary from model execution")
	}
	if !strings.Contains(verifierPrompt, "must provide a numeric answer") {
		t.Fatal("verifier prompt missing acceptance criteria")
	}
}
