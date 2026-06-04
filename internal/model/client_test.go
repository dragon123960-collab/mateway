package model

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
)

func TestParseAnthropicResultUsage(t *testing.T) {
	result, err := parseAnthropicResult([]byte(`{
		"content":[{"type":"text","text":"hello"}],
		"usage":{"input_tokens":12,"output_tokens":5}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "hello" || result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 5 || result.Usage.TotalTokens != 17 {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestParseAnthropicResultToolUse(t *testing.T) {
	result, err := parseAnthropicResult([]byte(`{
		"content":[
			{"type":"text","text":"reading"},
			{"type":"tool_use","id":"toolu_1","name":"file_read","input":{"path":"README.md"}}
		],
		"usage":{"input_tokens":12,"output_tokens":5}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "reading" || len(result.ToolCalls) != 1 {
		t.Fatalf("unexpected result %#v", result)
	}
	if result.ToolCalls[0].ID != "toolu_1" || result.ToolCalls[0].Name != "file.read" || result.ToolCalls[0].Args["path"] != "README.md" {
		t.Fatalf("unexpected tool call %#v", result.ToolCalls[0])
	}
}

func TestParseOpenAIChatResultToolCalls(t *testing.T) {
	result, err := parseOpenAIResult([]byte(`{
		"choices":[{
			"message":{
				"role":"assistant",
				"content":null,
				"tool_calls":[{
					"id":"call_1",
					"type":"function",
					"function":{"name":"terminal_run","arguments":"{\"command\":\"go test ./...\"}"}
				}]
			}
		}],
		"usage":{"prompt_tokens":20,"completion_tokens":8,"total_tokens":28}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected tool call, got %#v", result)
	}
	if result.ToolCalls[0].Name != "terminal.run" || result.ToolCalls[0].Args["command"] != "go test ./..." {
		t.Fatalf("unexpected tool call %#v", result.ToolCalls[0])
	}
}

func TestToolParametersIncludesOptionalProperties(t *testing.T) {
	params := toolParameters(fakeTool{
		name:     "script.run",
		required: []string{"name"},
		properties: map[string]any{
			"name": map[string]any{"type": "string"},
			"args": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	})
	properties, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("missing properties: %#v", params)
	}
	args, ok := properties["args"].(map[string]any)
	if !ok || args["type"] != "array" {
		t.Fatalf("expected args array property, got %#v", properties["args"])
	}
	required, ok := params["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "name" {
		t.Fatalf("unexpected required list: %#v", params["required"])
	}
}

func TestParseOpenAIResponsesResultUsage(t *testing.T) {
	result := parseOpenAIResponsesResult([]byte(`{
		"output_text":"hello",
		"usage":{"input_tokens":20,"output_tokens":8,"total_tokens":28}
	}`))
	if result.Text != "hello" || result.Usage.InputTokens != 20 || result.Usage.OutputTokens != 8 || result.Usage.TotalTokens != 28 {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestNativeSystemPromptDoesNotIncludeTextToolProtocol(t *testing.T) {
	prompt := buildNativeSystemPrompt("base", []agentcore.Tool{fakeTool{name: "file.read", required: []string{"path"}}})
	if contains(prompt, "[TOOL_CALL]") {
		t.Fatalf("native prompt should not contain text protocol: %s", prompt)
	}
	if !contains(prompt, "native name: file_read") {
		t.Fatalf("expected native alias in prompt: %s", prompt)
	}
}

func TestTextSystemPromptKeepsFallbackToolProtocol(t *testing.T) {
	prompt := buildTextSystemPrompt("base", []agentcore.Tool{fakeTool{name: "file.read", required: []string{"path"}}})
	if !contains(prompt, "[TOOL_CALL]") || !contains(prompt, "file.read exactly") {
		t.Fatalf("expected fallback protocol in prompt: %s", prompt)
	}
}

func TestAnthropicMessagesIncludeNativeToolBlocks(t *testing.T) {
	messages := anthropicMessages([]Message{
		{Role: "assistant", ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "file.read", Args: map[string]any{"path": "README.md"}}}},
		{Role: "tool", ToolCallID: "call_1", Content: "ok"},
	})
	data, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !contains(text, `"type":"tool_use"`) || !contains(text, `"name":"file_read"`) || !contains(text, `"type":"tool_result"`) {
		t.Fatalf("unexpected anthropic messages %s", text)
	}
}

func TestOpenAIChatMessagesIncludeNativeToolCalls(t *testing.T) {
	messages := openAIChatMessages("", []Message{
		{Role: "assistant", ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "file.read", Args: map[string]any{"path": "README.md"}}}},
		{Role: "tool", ToolCallID: "call_1", Content: "ok"},
	})
	data, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !contains(text, `"tool_calls"`) || !contains(text, `"name":"file_read"`) || !contains(text, `"role":"tool"`) {
		t.Fatalf("unexpected openai chat messages %s", text)
	}
}

func TestOpenAIContentIncludesTextAndImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "image.png")
	if err := os.WriteFile(path, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0o600); err != nil {
		t.Fatal(err)
	}
	content := openAIContent(Message{
		Role:    "user",
		Content: "look",
		Parts: []agentcore.MessagePart{
			{Type: agentcore.PartText, Text: "look"},
			{Type: agentcore.PartImage, URI: "file://" + path, MimeType: "image/png"},
		},
	})
	data, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !contains(text, "input_text") || !contains(text, "input_image") || !contains(text, "data:image/png;base64,") {
		t.Fatalf("unexpected openai content %s", text)
	}
}

func TestOpenAIChatContentUsesChatCompletionImageShape(t *testing.T) {
	content := openAIChatContent(Message{
		Role: "user",
		Parts: []agentcore.MessagePart{
			{Type: agentcore.PartText, Text: "look"},
			{Type: agentcore.PartImage, URI: "data:image/png;base64,abc"},
		},
	})
	data, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !contains(text, `"type":"text"`) || !contains(text, `"type":"image_url"`) || !contains(text, `"url":"data:image/png;base64,abc"`) {
		t.Fatalf("unexpected openai chat content %s", text)
	}
}

func TestClientRejectsImageWhenModelIsTextOnly(t *testing.T) {
	client := Client{Config: config.ModelConfig{Name: "text-only", Modalities: []string{"text"}}}
	_, err := client.Generate(context.Background(), "", []Message{{Role: "user", Parts: []agentcore.MessagePart{{Type: agentcore.PartImage, URI: "data:image/png;base64,abc"}}}})
	if err == nil || !contains(err.Error(), "does not support image") {
		t.Fatalf("expected image support error, got %v", err)
	}
}

func TestParseToolCallTextReturnsMultipleCalls(t *testing.T) {
	text := `[TOOL_CALL]
{"id":"call_1","name":"file.read","args":{"path":"agent.md"}}
[/TOOL_CALL]
[TOOL_CALL]
{"id":"call_2","name":"file.read","args":{"path":"user.md"}}
[/TOOL_CALL]`
	calls := parseToolCallText(text)
	if len(calls) != 2 {
		t.Fatalf("expected two tool calls, got %#v", calls)
	}
	if calls[0].ID != "call_1" || calls[0].Name != "file.read" || calls[0].Args["path"] != "agent.md" {
		t.Fatalf("unexpected first call %#v", calls[0])
	}
	if calls[1].ID != "call_2" || calls[1].Name != "file.read" || calls[1].Args["path"] != "user.md" {
		t.Fatalf("unexpected second call %#v", calls[1])
	}
}

func TestParseToolCallTextReturnsMultipleObjectsInOneBlock(t *testing.T) {
	text := `[TOOL_CALL]
{"id":"call_1","name":"file.read","args":{"path":"agent.md"}}
{"id":"call_2","name":"file.read","args":{"path":"user.md"}}
{"id":"call_3","name":"file.read","args":{"path":"tools.md"}}
[/TOOL_CALL]`
	calls := parseToolCallText(text)
	if len(calls) != 3 {
		t.Fatalf("expected three tool calls, got %#v", calls)
	}
	for i, want := range []string{"agent.md", "user.md", "tools.md"} {
		if calls[i].Name != "file.read" || calls[i].Args["path"] != want {
			t.Fatalf("call %d = %#v", i, calls[i])
		}
	}
}

func TestParseToolCallTextReturnsNamedToolCallTag(t *testing.T) {
	text := `我来先列出目录。
<tool_call>project.index
{"path": "/Users/dongping/.mateway/workspace/agents/main"}`
	calls := parseToolCallText(text)
	if len(calls) != 1 {
		t.Fatalf("expected one tool call, got %#v", calls)
	}
	if calls[0].ID != "call_1" || calls[0].Name != "project.index" || calls[0].Args["path"] != "/Users/dongping/.mateway/workspace/agents/main" {
		t.Fatalf("unexpected call %#v", calls[0])
	}
}

func TestAnthropicContentIncludesTextAndImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "image.png")
	if err := os.WriteFile(path, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0o600); err != nil {
		t.Fatal(err)
	}
	content := anthropicContent(Message{
		Role:    "user",
		Content: "look",
		Parts: []agentcore.MessagePart{
			{Type: agentcore.PartText, Text: "look"},
			{Type: agentcore.PartImage, URI: "file://" + path, MimeType: "image/png"},
		},
	})
	data, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !contains(text, `"type":"image"`) || !contains(text, `"media_type":"image/png"`) || !contains(text, `"type":"text"`) {
		t.Fatalf("unexpected anthropic content %s", text)
	}
}

func contains(text, sub string) bool {
	return strings.Contains(text, sub)
}

type fakeTool struct {
	name       string
	required   []string
	properties map[string]any
}

func (t fakeTool) Name() string        { return t.name }
func (t fakeTool) Description() string { return "fake tool" }
func (t fakeTool) Schema() agentcore.Schema {
	return agentcore.Schema{Required: t.required, Properties: t.properties}
}
func (t fakeTool) Risk() agentcore.Risk { return agentcore.RiskSafeRead }
func (t fakeTool) Run(context.Context, agentcore.ToolCall) agentcore.ToolResult {
	return agentcore.ToolResult{}
}
