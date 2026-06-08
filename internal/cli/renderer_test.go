package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/channel"
)

func TestRendererSeparatesProcessAndAssistant(t *testing.T) {
	var out bytes.Buffer
	renderer := &Renderer{Out: &out}
	renderer.User("review project")
	renderer.Progress(channel.OutboundMessage{Progress: []channel.ProgressStep{{
		Title:   "model",
		Status:  "thinking",
		Summary: "waiting for model output",
	}}})
	renderer.Progress(channel.OutboundMessage{Progress: []channel.ProgressStep{{
		Tool:    "file.read",
		Status:  "running",
		Summary: "/tmp/README.md",
	}}})
	renderer.Reply(channel.OutboundBatch{Reply: channel.OutboundMessage{Text: "done"}})
	text := out.String()
	for _, want := range []string{"User\n│ review project", "→ Read /tmp/README.md", "\nAssistant\n", "done\n"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "[tool]") || strings.Contains(text, "[thinking]") || strings.Contains(text, "waiting for model output") {
		t.Fatalf("old flat labels should not render:\n%s", text)
	}
}

func TestRenderProcessEventShowsToolOutcome(t *testing.T) {
	line := renderProcessEvent(ProcessEvent{Type: "tool.completed", Tool: "terminal.run", Summary: "ok", DurationMS: 42}, false)
	if !strings.Contains(line, "✓ Run") || !strings.Contains(line, "ok") || !strings.Contains(line, "(42ms)") {
		t.Fatalf("unexpected line: %q", line)
	}
}

func TestRenderProcessEventHidesReadResultSummary(t *testing.T) {
	line := renderProcessEvent(ProcessEvent{Type: "tool.completed", Tool: "file.read", Summary: "# README", DurationMS: 12}, false)
	if line != "✓ Read (12ms)" {
		t.Fatalf("unexpected line: %q", line)
	}
}
