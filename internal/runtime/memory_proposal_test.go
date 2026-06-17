package runtime

import (
	"testing"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/session"
)

func TestHandleGraphHumanPendingRequiresNumericReply(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	task := state.StartTask("confirm write")
	task.Graph = newTestGraph(session.TaskGraphNode{
		ID:     "confirm",
		Type:   session.NodeTypeHumanConfirm,
		Goal:   "confirm write",
		Status: session.NodeStatusAwaitingInput,
	})
	state.Pending = &session.PendingAction{
		Kind:    session.PendingKindHumanConfirm,
		TaskID:  task.ID,
		GraphID: task.Graph.ID,
		NodeID:  "confirm",
	}

	resp, handled, err := rt.handleGraphHumanPending(&state, channel.InboundMessage{Channel: "cli", ThreadID: "test", Text: "yes"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected invalid non-numeric response to be handled")
	}
	if resp.Reply.Style != channel.StyleInputRequired {
		t.Fatalf("expected input_required reply, got %q", resp.Reply.Style)
	}
	if state.Pending == nil {
		t.Fatal("pending should remain after invalid reply")
	}
	if task.Graph.NodeByID("confirm").Status != session.NodeStatusAwaitingInput {
		t.Fatalf("node should remain awaiting input, got %q", task.Graph.NodeByID("confirm").Status)
	}
}

func TestHandleGraphHumanPendingNumericConfirmCompletesNode(t *testing.T) {
	rt := newTestRuntime(t)
	state := session.State{Key: "cli:test"}
	task := state.StartTask("confirm write")
	task.Graph = newTestGraph(session.TaskGraphNode{
		ID:     "confirm",
		Type:   session.NodeTypeHumanConfirm,
		Goal:   "confirm write",
		Status: session.NodeStatusAwaitingInput,
	})
	state.Pending = &session.PendingAction{
		Kind:    session.PendingKindHumanConfirm,
		TaskID:  task.ID,
		GraphID: task.Graph.ID,
		NodeID:  "confirm",
	}

	_, handled, err := rt.handleGraphHumanPending(&state, channel.InboundMessage{Channel: "cli", ThreadID: "test", Text: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if handled {
		t.Fatal("valid human pending response should fall through to graph execution")
	}
	if state.Pending != nil {
		t.Fatal("pending should clear after confirmation")
	}
	node := task.Graph.NodeByID("confirm")
	if node.Status != session.NodeStatusCompleted || !node.Acceptance.Verified {
		t.Fatalf("expected confirmed node completed and verified, got status=%q verified=%v", node.Status, node.Acceptance.Verified)
	}
}
