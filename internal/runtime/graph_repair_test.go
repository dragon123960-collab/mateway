package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/session"
)

// dispatchModel routes model calls to different behaviors based on the system
// prompt, so a single test can exercise node execution, the node-level model
// verifier, the task-level verifier, and the replan generator simultaneously.
type dispatchModel struct {
	nodeVerifier  func(prompt string) string
	taskVerifier  func(prompt string) string
	replan        func(prompt string) string
	nodeExecution func(prompt string) string
	captured      []string
}

func (m *dispatchModel) Next(_ context.Context, ctx agentcore.Context) (agentcore.Message, error) {
	sp := ctx.SystemPrompt
	user := ""
	if len(ctx.Messages) > 0 {
		user = ctx.Messages[len(ctx.Messages)-1].Content
	}
	switch {
	case strings.Contains(sp, "verification judge"):
		m.captured = append(m.captured, "NODE_VERIFIER:"+user)
		if m.nodeVerifier != nil {
			return msg(m.nodeVerifier(user)), nil
		}
		return msg(`{"status":"passed","reason":"ok","confidence":"high"}`), nil
	case strings.Contains(sp, "task-graph acceptance judge"):
		m.captured = append(m.captured, "TASK_VERIFIER:"+user)
		if m.taskVerifier != nil {
			return msg(m.taskVerifier(user)), nil
		}
		return msg(`{"status":"passed","reason":"ok","confidence":"high"}`), nil
	case strings.Contains(sp, "node-level replan generator"):
		m.captured = append(m.captured, "REPLAN:"+user)
		if m.replan != nil {
			return msg(m.replan(user)), nil
		}
		return msg(`{"task":{"goal":"repair","acceptance":"fixed"},"nodes":[{"id":"repair-x","type":"subtask","mode":"direct","goal":"fixed","depends":[],"outputs":["repair_result"],"acceptance":"fixed"}]}`), nil
	default:
		m.captured = append(m.captured, "EXEC:"+user)
		if m.nodeExecution != nil {
			return msg(m.nodeExecution(user)), nil
		}
		return msg("node output"), nil
	}
}

func msg(content string) agentcore.Message {
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: content}
}

func taskWithGraph(goal string, nodes ...session.TaskGraphNode) (*session.TaskNode, *session.State, *session.TaskGraph) {
	g := &session.TaskGraph{
		ID:     "graph-test",
		TaskID: "task-test",
		Status: session.GraphStatusPlanned,
		Nodes:  nodes,
	}
	task := &session.TaskNode{ID: g.TaskID, Goal: goal, Graph: g}
	state := &session.State{Tasks: []session.TaskNode{*task}, ActiveTask: task.ID}
	return task, state, g
}

func completedNodeMissing(id string, outputKey string) session.TaskGraphNode {
	return session.TaskGraphNode{
		ID:            id,
		Type:          session.NodeTypeModel,
		Mode:          session.NodeModeDirect,
		Goal:          id,
		Status:        session.NodeStatusCompleted,
		ResultSummary: "result",
		Output:        map[string]any{"text": "partial"},
		Acceptance:    session.Acceptance{Criteria: "produces " + outputKey, Verified: true},
	}
}

// --- task-level model verifier orchestration ---

func TestVerifyTaskGraph_ModelPassed_OverridesNeedsRepair(t *testing.T) {
	rt := newTestRuntime(t)
	tm := &dispatchModel{taskVerifier: func(string) string {
		return `{"status":"passed","reason":"acceptance satisfied","confidence":"high"}`
	}}
	rt.Model = tm

	g := &session.TaskGraph{ID: "g", TaskID: "t", Nodes: []session.TaskGraphNode{
		completedNodeMissing("n1", "summary"),
	}}
	contract := &session.TaskContract{TaskAcceptance: "produce summary", FinalOutput: []string{"summary"}}

	res := rt.verifyTaskGraph(t.Context(), g, contract, nil)
	if res.Status != session.GraphStatusCompleted {
		t.Fatalf("expected model to override to completed, got %q (%s)", res.Status, res.Reason)
	}
}

func TestVerifyTaskGraph_ModelNeedsRepair_Preserved(t *testing.T) {
	rt := newTestRuntime(t)
	tm := &dispatchModel{taskVerifier: func(string) string {
		return `{"status":"needs_repair","reason":"missing summary","confidence":"medium","verifier_feedback":"add a summary section"}`
	}}
	rt.Model = tm

	g := &session.TaskGraph{ID: "g", TaskID: "t", Nodes: []session.TaskGraphNode{
		completedNodeMissing("n1", "summary"),
	}}
	contract := &session.TaskContract{TaskAcceptance: "produce summary", FinalOutput: []string{"summary"}}
	res := rt.verifyTaskGraph(t.Context(), g, contract, nil)
	if res.Status != session.GraphStatusNeedsRepair {
		t.Fatalf("expected needs_repair, got %q (%s)", res.Status, res.Reason)
	}
	if !strings.Contains(res.Reason, "summary") {
		t.Fatalf("reason should mention gap, got %q", res.Reason)
	}
}

func TestVerifyTaskGraph_RepairsPreservedAndIngested(t *testing.T) {
	rt := newTestRuntime(t)
	var capturedPrompt string
	tm := &dispatchModel{taskVerifier: func(prompt string) string {
		capturedPrompt = prompt
		return `{"status":"passed","reason":"ok","confidence":"high"}`
	}}
	rt.Model = tm

	g := &session.TaskGraph{ID: "g", TaskID: "t", Nodes: []session.TaskGraphNode{
		completedNodeMissing("n1", "summary"),
	}}
	g.RepairAttempts = []session.RepairAttempt{{
		Round:            1,
		RepairNodeID:     "repair-previous",
		VerifierFeedback: "prior gap alpha",
		Status:           session.RepairStatusFailed,
	}}
	contract := &session.TaskContract{TaskAcceptance: "produce summary", FinalOutput: []string{"summary"}}
	res := rt.verifyTaskGraph(t.Context(), g, contract, nil)
	if res.Status != session.GraphStatusCompleted {
		t.Fatalf("expected passed (override), got %q (%s)", res.Status, res.Reason)
	}
	if !strings.Contains(capturedPrompt, "Accumulated Repair Feedback") {
		t.Fatalf("task verifier prompt should include accumulated feedback")
	}
	if !strings.Contains(capturedPrompt, "prior gap alpha") {
		t.Fatalf("task verifier prompt should ingest prior round feedback")
	}
	if len(g.RepairAttempts) != 1 {
		t.Fatalf("repair history must be preserved, got %d attempts", len(g.RepairAttempts))
	}
}

// --- repair node append loop (via runGraphTask) ---

func taskVerifierSequence(statuses ...string) func(string) string {
	i := 0
	return func(string) string {
		s := "needs_repair"
		if i < len(statuses) {
			s = statuses[i]
			i++
		}
		switch s {
		case "passed":
			return `{"status":"passed","reason":"done","confidence":"high"}`
		case "blocked":
			return `{"status":"blocked","reason":"hard blocker","confidence":"high"}`
		default:
			return `{"status":"needs_repair","reason":"missing summary","confidence":"medium","verifier_feedback":"add summary"}`
		}
	}
}

func TestRunGraphTask_RepairLoopTwoRoundsThenPassed(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = &dispatchModel{
		taskVerifier: taskVerifierSequence("needs_repair", "needs_repair", "passed"),
	}
	_, state, g := taskWithGraph("produce summary", completedNodeMissing("n1", "summary"))
	state.Tasks[0].Execution.Contract = &session.TaskContract{TaskAcceptance: "produce summary", FinalOutput: []string{"summary"}}
	_ = g
	trace := newTestTraceRecorder(t)
	if _, err := rt.runGraphTask(t.Context(), inbound("cli:test", "go"), state, &state.Tasks[0], "go", trace); err != nil {
		t.Fatal(err)
	}
	finalGraph := state.Tasks[0].Graph
	if len(finalGraph.RepairAttempts) != 2 {
		t.Fatalf("expected 2 repair attempts, got %d", len(finalGraph.RepairAttempts))
	}
	if finalGraph.RepairAttempts[1].Status != session.RepairStatusPassed {
		t.Fatalf("expected last attempt passed, got %q", finalGraph.RepairAttempts[1].Status)
	}
	if !traceHasEvent(readTraceFile(t, trace.path), "task_repair_round") {
		t.Fatal("expected task_repair_round trace event")
	}
}

func TestRunGraphTask_RepairLoopExhaustedToBlocked(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = &dispatchModel{taskVerifier: taskVerifierSequence("needs_repair", "needs_repair", "needs_repair")}
	_, state, _ := taskWithGraph("produce summary", completedNodeMissing("n1", "summary"))
	state.Tasks[0].Execution.Contract = &session.TaskContract{TaskAcceptance: "produce summary", FinalOutput: []string{"summary"}}
	trace := newTestTraceRecorder(t)
	resp, err := rt.runGraphTask(t.Context(), inbound("cli:test", "go"), state, &state.Tasks[0], "go", trace)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Failed {
		t.Fatalf("expected blocked (failed) response, got %#v", resp)
	}
	finalGraph := state.Tasks[0].Graph
	if len(finalGraph.RepairAttempts) != 2 {
		t.Fatalf("expected 2 repair attempts before blocking, got %d", len(finalGraph.RepairAttempts))
	}
	if finalGraph.RepairAttempts[1].Status != session.RepairStatusFailed {
		t.Fatalf("expected last attempt failed, got %q", finalGraph.RepairAttempts[1].Status)
	}
	if !traceHasEvent(readTraceFile(t, trace.path), "task_repair_escalated_blocked") {
		t.Fatal("expected task_repair_escalated_blocked trace event")
	}
}

func TestRunGraphTask_MaxRepairRoundsZero_DegradesToBlocked(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Config.Execution.MaxRepairRounds = intPtrTest(0)
	rt.Model = &dispatchModel{taskVerifier: taskVerifierSequence("needs_repair")}
	_, state, _ := taskWithGraph("produce summary", completedNodeMissing("n1", "summary"))
	state.Tasks[0].Execution.Contract = &session.TaskContract{TaskAcceptance: "produce summary", FinalOutput: []string{"summary"}}
	trace := newTestTraceRecorder(t)
	resp, err := rt.runGraphTask(t.Context(), inbound("cli:test", "go"), state, &state.Tasks[0], "go", trace)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Failed {
		t.Fatalf("expected blocked response when MaxRepairRounds=0, got %#v", resp)
	}
	if len(state.Tasks[0].Graph.RepairAttempts) != 0 {
		t.Fatalf("expected no repair attempts when MaxRepairRounds=0, got %d", len(state.Tasks[0].Graph.RepairAttempts))
	}
	if !traceHasEvent(readTraceFile(t, trace.path), "task_repair_escalated_blocked") {
		t.Fatal("expected task_repair_escalated_blocked trace event")
	}
}

func TestRunGraphTask_MaxRepairRoundsZero_ModelPassedSucceeds(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Config.Execution.MaxRepairRounds = intPtrTest(0)
	rt.Model = &dispatchModel{taskVerifier: taskVerifierSequence("passed")}
	_, state, _ := taskWithGraph("produce summary", completedNodeMissing("n1", "summary"))
	state.Tasks[0].Execution.Contract = &session.TaskContract{TaskAcceptance: "produce summary", FinalOutput: []string{"summary"}}
	trace := newTestTraceRecorder(t)
	resp, err := rt.runGraphTask(t.Context(), inbound("cli:test", "go"), state, &state.Tasks[0], "go", trace)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("model passed with MaxRepairRounds=0 should succeed, got %#v", resp)
	}
	if len(state.Tasks[0].Graph.RepairAttempts) != 0 {
		t.Fatalf("no repair should be appended, got %d", len(state.Tasks[0].Graph.RepairAttempts))
	}
}

func TestRunGraphTask_RecoveryCompletesInFlightRepairAndPreservesHistory(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = &dispatchModel{
		taskVerifier: taskVerifierSequence("passed"),
		nodeExecution: func(prompt string) string {
			if !strings.Contains(prompt, "second gap") {
				t.Fatalf("expected recovered repair node execution prompt, got %q", prompt)
			}
			return "recovered repair output"
		},
	}
	completed := completedNodeMissing("n1", "summary")
	repair := buildRepairNode(&session.TaskGraph{
		ID:     "graph-test",
		TaskID: "task-test",
		Nodes:  []session.TaskGraphNode{completed},
		RepairAttempts: []session.RepairAttempt{{
			Round:            1,
			RepairNodeID:     "repair-1-graph-test",
			VerifierFeedback: "first gap",
			Status:           session.RepairStatusFailed,
		}},
	}, &session.TaskNode{ID: "task-test", Goal: "produce summary"}, "second gap", 2)
	repair.Status = session.NodeStatusRunning
	repair.Output = map[string]any{"summary": true}

	_, state, g := taskWithGraph("produce summary", completed, repair)
	g.RepairAttempts = []session.RepairAttempt{{
		Round:            1,
		RepairNodeID:     "repair-1-graph-test",
		VerifierFeedback: "first gap",
		Status:           session.RepairStatusFailed,
	}}
	state.Tasks[0].Execution.Contract = &session.TaskContract{TaskAcceptance: "produce summary", FinalOutput: []string{"summary"}}
	trace := newTestTraceRecorder(t)

	resp, err := rt.runGraphTask(t.Context(), inbound("cli:test", "resume"), state, &state.Tasks[0], "resume", trace)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Failed {
		t.Fatalf("expected recovered repair to pass, got %#v", resp)
	}
	finalGraph := state.Tasks[0].Graph
	if len(finalGraph.RepairAttempts) != 2 {
		t.Fatalf("expected recovered repair attempt recorded after prior history, got %d", len(finalGraph.RepairAttempts))
	}
	if finalGraph.RepairAttempts[0].VerifierFeedback != "first gap" {
		t.Fatalf("prior repair history was not preserved: %#v", finalGraph.RepairAttempts)
	}
	last := finalGraph.RepairAttempts[1]
	if last.RepairNodeID != repair.ID || last.Status != session.RepairStatusPassed || last.VerifierFeedback != "second gap" {
		t.Fatalf("unexpected recovered repair attempt: %#v", last)
	}
	if got := finalGraph.NodeByID(repair.ID).Status; got != session.NodeStatusCompleted {
		t.Fatalf("expected recovered repair node completed, got %q", got)
	}
	if !traceHasEvent(readTraceFile(t, trace.path), "graph_recovery_normalized") {
		t.Fatal("expected graph_recovery_normalized trace event")
	}
	if !traceHasEvent(readTraceFile(t, trace.path), "task_repair_round") {
		t.Fatal("expected task_repair_round trace event")
	}
}

// --- node-level model-driven replan ---

func TestGenerateReplacementNode_ModelDriven(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = &dispatchModel{replan: func(string) string {
		return `{"task":{"goal":"repair","acceptance":"covers gap"},"nodes":[{"id":"repair-analyze","type":"subtask","mode":"direct","goal":"produce corrected analysis","depends":["dep1"],"outputs":["repair_result"],"acceptance":"covers gap and goal"}]}`
	}}
	g := &session.TaskGraph{ID: "g", TaskID: "t", Nodes: []session.TaskGraphNode{
		{ID: "dep1", Type: session.NodeTypeModel, Status: session.NodeStatusCompleted, ResultSummary: "ctx", Acceptance: session.Acceptance{Verified: true}},
	}}
	failed := &session.TaskGraphNode{
		ID:            "analyze",
		Type:          session.NodeTypeSubtask,
		Mode:          session.NodeModeDirect,
		Goal:          "produce analysis",
		Status:        session.NodeStatusFailed,
		FailureReason: "verifier rejected",
		Depends:       []string{"dep1"},
	}
	node, err := rt.generateReplacementNode(t.Context(), g, failed, nil)
	if err != nil {
		t.Fatalf("expected model-driven replan, got error: %v", err)
	}
	if !strings.HasPrefix(node.ID, "repair-") {
		t.Fatalf("expected repair- prefixed id, got %q", node.ID)
	}
	if node.Goal != "produce corrected analysis" {
		t.Fatalf("expected model-generated goal, got %q", node.Goal)
	}
	if node.Acceptance.Criteria != "covers gap and goal" {
		t.Fatalf("expected model-generated acceptance, got %q", node.Acceptance.Criteria)
	}
	if depth := localReplanDepth(&node); depth != 1 {
		t.Fatalf("expected local_replan_depth=1, got %d", depth)
	}
	if strings.Join(node.Depends, ",") != "dep1" {
		t.Fatalf("expected depends preserved, got %v", node.Depends)
	}
}

func TestGenerateReplacementNode_InvalidJSONReturnsError(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Model = &dispatchModel{replan: func(string) string { return "not json" }}
	g := &session.TaskGraph{ID: "g", TaskID: "t"}
	failed := &session.TaskGraphNode{
		ID:     "analyze",
		Type:   session.NodeTypeSubtask,
		Mode:   session.NodeModeDirect,
		Goal:   "produce analysis",
		Status: session.NodeStatusFailed,
	}
	_, err := rt.generateReplacementNode(t.Context(), g, failed, nil)
	if err == nil {
		t.Fatal("expected error for invalid replan JSON")
	}
}

func TestRunGraphTask_ModelDrivenReplanAppliesAndCompletes(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Config.Execution.ModelVerifier = "always"
	repairCalled := false
	rt.Model = &dispatchModel{
		nodeVerifier: func(prompt string) string {
			if strings.Contains(prompt, "full coverage") || strings.Contains(prompt, "produce complete coverage") {
				return `{"status":"passed","reason":"repair accepted","confidence":"high"}`
			}
			return `{"status":"failed","reason":"coverage missing","confidence":"high"}`
		},
		replan: func(string) string {
			repairCalled = true
			return `{"task":{"goal":"repair","acceptance":"coverage complete"},"nodes":[{"id":"repair-analyze","type":"subtask","mode":"direct","goal":"produce complete coverage","depends":[],"outputs":["repair_result"],"acceptance":"full coverage"}]}`
		},
		nodeExecution: func(string) string { return "completed result" },
	}
	_, state, _ := taskWithGraph("analyze", session.TaskGraphNode{
		ID:         "analyze",
		Type:       session.NodeTypeModel,
		Mode:       session.NodeModeDirect,
		Goal:       "analyze",
		Status:     session.NodeStatusPending,
		Acceptance: session.Acceptance{Criteria: "must cover everything"},
	})
	trace := newTestTraceRecorder(t)
	if _, err := rt.runGraphTask(t.Context(), inbound("cli:test", "analyze"), state, &state.Tasks[0], "analyze", trace); err != nil {
		t.Fatal(err)
	}
	if !repairCalled {
		t.Fatal("expected model-driven replan generator to be called")
	}
	finalGraph := state.Tasks[0].Graph
	if finalGraph.NodeByID("repair-analyze") == nil {
		t.Fatalf("expected repair-analyze node applied, nodes=%v", finalGraph.NodeIDs())
	}
	if finalGraph.NodeByID("repair-analyze").Status != session.NodeStatusCompleted {
		t.Fatalf("expected repair node completed, got %q", finalGraph.NodeByID("repair-analyze").Status)
	}
	if finalGraph.NodeByID("analyze") != nil {
		t.Fatal("failed node should be removed after local replan")
	}
}

func TestRunGraphTask_NodeReplanDepthCapEscalatesFailed(t *testing.T) {
	rt := newTestRuntime(t)
	rt.Config.Execution.ModelVerifier = "always"
	rt.Config.Execution.MaxNodeReplanDepth = intPtrTest(1)
	limitReached := false
	rt.Model = &dispatchModel{
		nodeVerifier: func(string) string {
			return `{"status":"replan","reason":"still wrong","confidence":"high"}`
		},
		replan: func(string) string {
			return `{"task":{"goal":"repair","acceptance":"x"},"nodes":[{"id":"repair-answer","type":"subtask","mode":"direct","goal":"fix","depends":[],"outputs":["repair_result"],"acceptance":"fixed"}]}`
		},
		nodeExecution: func(string) string { return "result" },
	}
	_, state, _ := taskWithGraph("answer", session.TaskGraphNode{
		ID:         "answer",
		Type:       session.NodeTypeModel,
		Mode:       session.NodeModeDirect,
		Goal:       "answer",
		Status:     session.NodeStatusPending,
		Acceptance: session.Acceptance{Criteria: "must pass"},
	})
	trace := newTestTraceRecorder(t)
	_, err := rt.runGraphTask(t.Context(), inbound("cli:test", "answer"), state, &state.Tasks[0], "answer", trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range readTraceFile(t, trace.path) {
		if e["type"] == "local_replan_limit_reached" {
			limitReached = true
		}
	}
	if !limitReached {
		t.Fatal("expected local_replan_limit_reached under cap=1")
	}
	finalGraph := state.Tasks[0].Graph
	var failedSeen bool
	for _, n := range finalGraph.Nodes {
		if strings.HasPrefix(n.ID, "repair-") && n.Status == session.NodeStatusFailed {
			failedSeen = true
		}
	}
	if !failedSeen {
		t.Fatalf("expected a repair node to escalate to Failed under cap=1, nodes=%v", finalGraph.NodeIDs())
	}
}
