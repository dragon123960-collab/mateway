package model

import (
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/tool"
)

func TestBuildSystemPromptIncludesToolContract(t *testing.T) {
	prompt := buildSystemPrompt("base", []agentcore.Tool{tool.FileReadTool{}})
	for _, want := range []string{"Use when:", "Do not use when:", "Output contract:", "Evidence:", "Acceptance:", "Soft failure signals:", "Parallel mode:", "Reuse policy:", "Confirmation boundary:"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if !strings.Contains(prompt, "Current date:") || !strings.Contains(prompt, "Asia/Shanghai") {
		t.Fatalf("prompt missing current date context:\n%s", prompt)
	}
}

func TestParseToolCallTextAllowsWhitespaceInMarkers(t *testing.T) {
	call, ok := parseToolCallText(`[ TOOL_CALL]
{"id":" call_1","name":"web.search","args":{"query":"mateway"}}
[/TOOL_CALL ]`)
	if !ok {
		t.Fatal("expected tool call")
	}
	if call.ID != "call_1" || call.Name != "web.search" || call.Args["query"] != "mateway" {
		t.Fatalf("call = %#v", call)
	}
}
