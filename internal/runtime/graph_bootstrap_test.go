package runtime

import (
	"testing"

	"github.com/dongping/mateway/internal/session"
)

func TestFallbackGraphFromContract_DoesNotCreateFakeToolNodes(t *testing.T) {
	task := &session.TaskNode{
		ID:   "task-1",
		Goal: "write a file",
	}
	contract := session.TaskContract{
		RequiresTools: true,
		RequiredTools: []string{"file.write"},
		PlanItems: []session.TaskPlanItem{
			{ID: "write", Title: "write file", Tool: "file.write"},
		},
		ExpectedOutcome: "file is written",
	}

	g := fallbackGraphFromContract(task, contract, "write hello to /tmp/hello.txt")
	if len(g.Nodes) != 1 {
		t.Fatalf("expected conservative one-node fallback, got %d nodes: %#v", len(g.Nodes), g.Nodes)
	}
	n := g.Nodes[0]
	if n.Type != session.NodeTypeModel {
		t.Fatalf("expected model fallback node, got %q", n.Type)
	}
	if n.Executor != "" {
		t.Fatalf("fallback model node should not have executor, got %q", n.Executor)
	}
	if n.Status != session.NodeStatusBlocked {
		t.Fatalf("tool fallback should be blocked, got %q", n.Status)
	}
	if n.FailureReason == "" {
		t.Fatal("tool fallback should explain why it is blocked")
	}
	if _, ok := n.Input["goal"]; ok {
		t.Fatalf("fallback must not create fake tool args: %#v", n.Input)
	}
}

func TestFallbackGraphFromContract_AllowsSimpleModelFallback(t *testing.T) {
	task := &session.TaskNode{
		ID:   "task-1",
		Goal: "answer a question",
	}
	contract := session.TaskContract{ExpectedOutcome: "plain answer"}

	g := fallbackGraphFromContract(task, contract, "what is Mateway")
	if len(g.Nodes) != 1 {
		t.Fatalf("expected one fallback node, got %d", len(g.Nodes))
	}
	n := g.Nodes[0]
	if n.Type != session.NodeTypeModel || n.Status != session.NodeStatusPending {
		t.Fatalf("simple fallback should remain executable model node, got %#v", n)
	}
}
