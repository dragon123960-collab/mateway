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

func TestParseOpenAIResponsesResultUsage(t *testing.T) {
	result := parseOpenAIResponsesResult([]byte(`{
		"output_text":"hello",
		"usage":{"input_tokens":20,"output_tokens":8,"total_tokens":28}
	}`))
	if result.Text != "hello" || result.Usage.InputTokens != 20 || result.Usage.OutputTokens != 8 || result.Usage.TotalTokens != 28 {
		t.Fatalf("unexpected result %#v", result)
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
