package cli

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/runtime"
)

func TestChatApproveToolCallReadsYes(t *testing.T) {
	var out bytes.Buffer
	state := chatState{in: strings.NewReader("y\n"), out: &out}
	state.reader = bufio.NewReader(state.in)
	decision, err := state.approveToolCall(t.Context(), runtime.ApprovalRequest{
		Reason: "terminal command requires approval: shell",
		ToolCall: agentcore.ToolCall{
			Name: "terminal.run",
			Args: map[string]any{"command": "echo ok; echo done"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Approved {
		t.Fatalf("expected approval, got %#v", decision)
	}
	if !strings.Contains(out.String(), "allow? [y/N]:") || !strings.Contains(out.String(), "echo ok") {
		t.Fatalf("unexpected prompt:\n%s", out.String())
	}
}

func TestChatApproveToolCallDefaultsNo(t *testing.T) {
	var out bytes.Buffer
	state := chatState{in: strings.NewReader("\n"), out: &out}
	state.reader = bufio.NewReader(state.in)
	decision, err := state.approveToolCall(t.Context(), runtime.ApprovalRequest{
		Reason:   "terminal command requires approval: unknown",
		ToolCall: agentcore.ToolCall{Name: "terminal.run", Args: map[string]any{"command": "custom-cli"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Approved {
		t.Fatalf("expected rejection, got %#v", decision)
	}
}
