package runtime

import (
	"testing"
	"time"

	"github.com/dongping/mateway/internal/session"
)

func makeTask(id, goal, status string) session.TaskNode {
	now := time.Now()
	return session.TaskNode{
		ID:        id,
		Goal:      goal,
		Status:    status,
		CreatedAt: now,
		UpdatedAt: now,
		Execution: session.ExecutionFrame{
			ID:           "frame-" + id,
			Mode:         "agent_loop",
			Status:       status,
			OriginalTask: goal,
			UpdatedAt:    now,
		},
	}
}

func TestDetermineContinuation_NoState_NewGraph(t *testing.T) {
	dec := determineContinuation(session.State{}, "hello")
	if dec.Action != ActionNewGraph {
		t.Fatalf("expected new_graph, got %s (%s)", dec.Action, dec.Reason)
	}
}

func TestDetermineContinuation_PendingAction_Priority(t *testing.T) {
	state := session.State{
		ActiveTask: "task-1",
		Tasks: []session.TaskNode{
			makeTask("task-1", "build feature", "running"),
		},
		Pending: &session.PendingAction{
			Kind:   session.PendingKindTaskPlanConfirm,
			TaskID: "task-1",
		},
	}
	dec := determineContinuation(state, "1")
	if dec.Action != ActionAnswerPending {
		t.Fatalf("expected answer_pending, got %s", dec.Action)
	}
	if dec.TaskID != "task-1" {
		t.Fatalf("expected task-1, got %s", dec.TaskID)
	}
}

func TestDetermineContinuation_PendingWithoutTaskID_NoPriority(t *testing.T) {
	state := session.State{
		ActiveTask: "task-1",
		Tasks: []session.TaskNode{
			makeTask("task-1", "build feature", "running"),
		},
		Pending: &session.PendingAction{
			Kind: "unknown",
		},
	}
	dec := determineContinuation(state, "hello")
	if dec.Action == ActionAnswerPending {
		t.Fatalf("should not answer_pending when TaskID is empty")
	}
}

func TestDetermineContinuation_AwaitUserInput_ShortConfirmation(t *testing.T) {
	state := session.State{
		ActiveTask: "task-1",
		Tasks: []session.TaskNode{
			makeTask("task-1", "build feature", "await_user_input"),
		},
	}
	for _, text := range []string{"yes", "ok", "1", "continue", "继续"} {
		dec := determineContinuation(state, text)
		if dec.Action != ActionResumeNode {
			t.Fatalf("text=%q: expected resume_node, got %s", text, dec.Action)
		}
		if dec.TaskID != "task-1" {
			t.Fatalf("text=%q: expected task-1, got %s", text, dec.TaskID)
		}
	}
}

func TestDetermineContinuation_AwaitUserInput_SameTaskFollowup(t *testing.T) {
	state := session.State{
		ActiveTask: "task-1",
		Tasks: []session.TaskNode{
			{
				ID:     "task-1",
				Goal:   "build authentication feature",
				Status: "await_user_input",
				Execution: session.ExecutionFrame{
					ID:     "frame-task-1",
					Mode:   "agent_loop",
					Status: "awaiting_user_input",
				},
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			},
		},
	}
	dec := determineContinuation(state, "set up the auth routes")
	if dec.Action != ActionResumeNode {
		t.Fatalf("expected resume_node, got %s (%s)", dec.Action, dec.Reason)
	}
}

func TestDetermineContinuation_AwaitUserInput_ExplicitNewTask(t *testing.T) {
	state := session.State{
		ActiveTask: "task-1",
		Tasks: []session.TaskNode{
			makeTask("task-1", "build authentication", "await_user_input"),
		},
	}
	for _, text := range []string{"/new", "/new write tests for parser"} {
		dec := determineContinuation(state, text)
		if dec.Action != ActionNewGraph {
			t.Fatalf("text=%q: expected new_graph, got %s", text, dec.Action)
		}
	}
}

func TestDetermineContinuation_AwaitUserInput_DefaultResume(t *testing.T) {
	state := session.State{
		ActiveTask: "task-1",
		Tasks: []session.TaskNode{
			makeTask("task-1", "build authentication", "await_user_input"),
		},
	}
	dec := determineContinuation(state, "create a new deployment script for production")
	if dec.Action != ActionResumeNode {
		t.Fatalf("expected resume_node unless /new is explicit, got %s (%s)", dec.Action, dec.Reason)
	}
}

func TestDetermineContinuation_BlockedTask_ResumeSignal(t *testing.T) {
	state := session.State{
		ActiveTask: "task-2",
		Tasks: []session.TaskNode{
			makeTask("task-2", "deploy service", "failed"),
		},
	}
	for _, text := range []string{"continue", "重试", "retry", "fixed it", "authorized", "done, try again"} {
		dec := determineContinuation(state, text)
		if dec.Action != ActionResumeNode {
			t.Fatalf("text=%q: expected resume_node, got %s", text, dec.Action)
		}
	}
}

func TestDetermineContinuation_BlockedTask_DefaultNewGraph(t *testing.T) {
	state := session.State{
		ActiveTask: "task-2",
		Tasks: []session.TaskNode{
			makeTask("task-2", "deploy service", "failed"),
		},
	}
	for _, text := range []string{
		"tell me a joke",
		"write a new deployment script for production",
	} {
		dec := determineContinuation(state, text)
		if dec.Action != ActionNewGraph {
			t.Fatalf("text=%q: expected new_graph as default for failed task, got %s", text, dec.Action)
		}
	}
}

func TestDetermineContinuation_RunningTask_ShortConfirmation(t *testing.T) {
	state := session.State{
		ActiveTask: "task-3",
		Tasks: []session.TaskNode{
			makeTask("task-3", "analyze code", "running"),
		},
	}
	for _, text := range []string{"ok", "go ahead", "go", "1"} {
		dec := determineContinuation(state, text)
		if dec.Action != ActionContinueGraph {
			t.Fatalf("text=%q: expected continue_graph, got %s", text, dec.Action)
		}
	}
}

func TestDetermineContinuation_RunningTask_DefaultContinue(t *testing.T) {
	state := session.State{
		ActiveTask: "task-3",
		Tasks: []session.TaskNode{
			{
				ID:     "task-3",
				Goal:   "analyze code quality",
				Status: "running",
				Execution: session.ExecutionFrame{
					ID:     "frame-task-3",
					Mode:   "agent_loop",
					Status: "running",
				},
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			},
		},
	}
	dec := determineContinuation(state, "also check dependency versions")
	if dec.Action != ActionContinueGraph {
		t.Fatalf("expected continue_graph by default for running task, got %s", dec.Action)
	}
}

func TestDetermineContinuation_RunningTask_UnrelatedActionContinuesUnlessExplicitNew(t *testing.T) {
	state := session.State{
		ActiveTask: "task-3",
		Tasks: []session.TaskNode{
			makeTask("task-3", "analyze code quality", "running"),
		},
	}
	dec := determineContinuation(state, "build a docker image from scratch")
	if dec.Action != ActionContinueGraph {
		t.Fatalf("expected continue_graph unless /new is explicit, got %s (%s)", dec.Action, dec.Reason)
	}
}

func TestDetermineContinuation_RunningTask_ExplicitNewTask(t *testing.T) {
	state := session.State{
		ActiveTask: "task-3",
		Tasks: []session.TaskNode{
			makeTask("task-3", "analyze code", "running"),
		},
	}
	dec := determineContinuation(state, "/new write tests for parser")
	if dec.Action != ActionNewGraph {
		t.Fatalf("expected new_graph for explicit new task, got %s", dec.Action)
	}
}

func TestDetermineContinuation_CompletedTask_NotReactivated(t *testing.T) {
	state := session.State{
		Tasks: []session.TaskNode{
			makeTask("task-4", "setup project", "completed"),
		},
	}
	dec := determineContinuation(state, "add a new module")
	if dec.Action != ActionNewGraph {
		t.Fatalf("expected new_graph for completed task, got %s", dec.Action)
	}
	if len(dec.ContextRefs) != 1 || dec.ContextRefs[0] != "task-4" {
		t.Fatalf("expected new graph to carry recent completed task context, got %v", dec.ContextRefs)
	}
}

func TestDetermineContinuation_CompletedTask_Reference(t *testing.T) {
	state := session.State{
		Tasks: []session.TaskNode{
			makeTask("task-4", "setup project structure", "completed"),
		},
	}
	dec := determineContinuation(state, "follow up on the project structure")
	if dec.Action != ActionReferenceCompleted {
		t.Fatalf("expected reference_completed, got %s", dec.Action)
	}
	if len(dec.ContextRefs) == 0 || dec.ContextRefs[0] != "task-4" {
		t.Fatalf("expected context refs to contain task-4, got %v", dec.ContextRefs)
	}
}

func TestDetermineContinuation_CompletedTask_DefaultCarriesContext(t *testing.T) {
	state := session.State{
		Tasks: []session.TaskNode{
			makeTask("task-4", "setup project structure", "completed"),
		},
	}
	for _, text := range []string{
		"基于刚才的结果总结一句话",
		"继续上次的项目设置",
		"tell me a story about dragons",
	} {
		dec := determineContinuation(state, text)
		if dec.Action != ActionNewGraph {
			t.Fatalf("text=%q: expected new_graph with recent context, got %s", text, dec.Action)
		}
		if len(dec.ContextRefs) == 0 || dec.ContextRefs[0] != "task-4" {
			t.Fatalf("text=%q: expected context refs to contain task-4, got %v", text, dec.ContextRefs)
		}
	}
}

func TestDetermineContinuation_CompletedTask_NotReferenced(t *testing.T) {
	state := session.State{
		Tasks: []session.TaskNode{
			makeTask("task-4", "setup project", "completed"),
		},
	}
	dec := determineContinuation(state, "tell me a story about dragons")
	if dec.Action != ActionNewGraph {
		t.Fatalf("expected new_graph for unrelated text to completed task, got %s", dec.Action)
	}
	if len(dec.ContextRefs) != 1 || dec.ContextRefs[0] != "task-4" {
		t.Fatalf("expected unrelated new graph to still carry recent context, got %v", dec.ContextRefs)
	}
}

func TestDetermineContinuation_CompletedTask_ExplicitNewClearsContext(t *testing.T) {
	state := session.State{
		Tasks: []session.TaskNode{
			makeTask("task-4", "setup project", "completed"),
		},
	}
	dec := determineContinuation(state, "/new tell me a story about dragons")
	if dec.Action != ActionNewGraph {
		t.Fatalf("expected new_graph for explicit /new, got %s", dec.Action)
	}
	if len(dec.ContextRefs) != 0 {
		t.Fatalf("expected /new to clear context refs, got %v", dec.ContextRefs)
	}
}

func TestDetermineContinuation_HistoricalReference_DefaultCarriesContext(t *testing.T) {
	state := session.State{
		Tasks: []session.TaskNode{
			makeTask("task-5", "update dependencies", "completed"),
			makeTask("task-6", "run benchmarks", "completed"),
		},
	}
	for _, text := range []string{
		"what did I do previous time",
		"recall the earlier analysis",
		"历史上我们怎么处理的",
	} {
		dec := determineContinuation(state, text)
		if dec.Action != ActionNewGraph {
			t.Fatalf("text=%q: expected new_graph with recent context, got %s", text, dec.Action)
		}
		if len(dec.ContextRefs) != 1 || dec.ContextRefs[0] != "task-6" {
			t.Fatalf("text=%q: expected latest completed task context, got %v", text, dec.ContextRefs)
		}
	}
}

func TestDetermineContinuation_EmptyTextWithActiveTask(t *testing.T) {
	state := session.State{
		ActiveTask: "task-7",
		Tasks: []session.TaskNode{
			makeTask("task-7", "do something", "running"),
		},
	}
	dec := determineContinuation(state, "")
	if dec.Action != ActionContinueGraph {
		t.Fatalf("expected continue_graph for empty text with active task, got %s", dec.Action)
	}
}

func TestDetermineContinuation_EmptyTextNoState(t *testing.T) {
	dec := determineContinuation(session.State{}, "")
	if dec.Action != ActionNewGraph {
		t.Fatalf("expected new_graph for empty state and text, got %s", dec.Action)
	}
}

func TestDetermineContinuation_ActiveTask_ExplicitNewClearsActiveTask(t *testing.T) {
	state := session.State{
		ActiveTask: "task-8",
		Tasks: []session.TaskNode{
			makeTask("task-8", "old task", "running"),
		},
	}
	dec := determineContinuation(state, "/new write a completely new thing unrelated to old task")
	if dec.Action != ActionNewGraph {
		t.Fatalf("expected new_graph, got %s", dec.Action)
	}
}

func TestDetermineContinuation_ResumeNode_IncludesCurrentNodeID(t *testing.T) {
	state := session.State{
		ActiveTask: "task-9",
		Tasks: []session.TaskNode{
			{
				ID:     "task-9",
				Goal:   "review pull request",
				Status: "await_user_input",
				Execution: session.ExecutionFrame{
					ID:            "frame-task-9",
					Mode:          "agent_loop",
					Status:        "awaiting_user_input",
					CurrentNodeID: "node-review-1",
				},
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			},
		},
	}
	dec := determineContinuation(state, "approved")
	if dec.Action != ActionResumeNode {
		t.Fatalf("expected resume_node, got %s", dec.Action)
	}
	if dec.NodeID != "node-review-1" {
		t.Fatalf("expected NodeID node-review-1, got %s", dec.NodeID)
	}
}

func TestDetermineContinuation_BlockedWithNodeID(t *testing.T) {
	state := session.State{
		ActiveTask: "task-10",
		Tasks: []session.TaskNode{
			{
				ID:     "task-10",
				Goal:   "deploy to prod",
				Status: "failed",
				Execution: session.ExecutionFrame{
					ID:            "frame-task-10",
					Mode:          "agent_loop",
					Status:        "failed",
					CurrentNodeID: "node-deploy-3",
				},
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			},
		},
	}
	dec := determineContinuation(state, "retry deployment")
	if dec.Action != ActionResumeNode {
		t.Fatalf("expected resume_node, got %s", dec.Action)
	}
	if dec.NodeID != "node-deploy-3" {
		t.Fatalf("expected NodeID node-deploy-3, got %s", dec.NodeID)
	}
}

func TestContinuationDecision_TraceFields(t *testing.T) {
	dec := determineContinuation(session.State{}, "build a rocket")
	if dec.UserText != "build a rocket" {
		t.Fatalf("expected UserText preserved, got %q", dec.UserText)
	}
	if dec.Reason == "" {
		t.Fatal("expected non-empty Reason")
	}
	if dec.Action != ActionNewGraph {
		t.Fatalf("expected new_graph, got %s", dec.Action)
	}
}
