package feishu

import (
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/channel"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestNormalizeMessageReceiveCapturesSenderType(t *testing.T) {
	messageID := "om_1"
	chatID := "oc_1"
	content := `{"text":"hello"}`
	messageType := "text"
	chatType := "p2p"
	senderType := "app"
	event := &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
		Sender: &larkim.EventSender{SenderType: &senderType},
		Message: &larkim.EventMessage{
			MessageId:   &messageID,
			ChatId:      &chatID,
			Content:     &content,
			MessageType: &messageType,
			ChatType:    &chatType,
		},
	}}
	msg := NormalizeMessageReceive(event)
	if msg.Metadata["sender_type"] != "app" {
		t.Fatalf("expected app sender type, got %q", msg.Metadata["sender_type"])
	}
	if msg.Text != "hello" {
		t.Fatalf("unexpected text %q", msg.Text)
	}
}

func TestRenderReplyMessageStripsToolCallEcho(t *testing.T) {
	_, content, err := renderReplyMessage(channel.OutboundMessage{
		Style: "reply",
		Text:  "[TOOL_CALL]\n{\"tool\":\"file.read\",\"args\":{\"path\":\"README.md\"}}\n[/TOOL_CALL]\n\n最终结论。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "TOOL_CALL") || strings.Contains(content, "file.read") {
		t.Fatalf("expected tool call stripped, got %s", content)
	}
	if !strings.Contains(content, "最终结论") {
		t.Fatalf("expected final conclusion, got %s", content)
	}
}

func TestRenderReplyMessageStripsMiniMaxToolCallEcho(t *testing.T) {
	_, content, err := renderReplyMessage(channel.OutboundMessage{
		Style: "reply",
		Text: `<minimax:tool_call>
file.read args: {"path": "/Users/dongping/project/mateway/docs/测试文档.md"} risk: "safe_read" requires_confirm: false
<minimax:tool_call>
file.read args: {"path": "/Users/dongping/project/mateway/docs/进度.md"} risk: "safe_read" requires_confirm: false
</minimax:tool_call>

当前 Mateway 的测试目标是验证 Agent 在复杂对话中保持上下文。`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "minimax:tool_call") || strings.Contains(content, "file.read") || strings.Contains(content, "requires_confirm") {
		t.Fatalf("expected minimax tool call stripped, got %s", content)
	}
	if !strings.Contains(content, "当前 Mateway") {
		t.Fatalf("expected final answer preserved, got %s", content)
	}
}

func TestRenderReplyMessageStripsBareJSONToolPlan(t *testing.T) {
	_, content, err := renderReplyMessage(channel.OutboundMessage{
		Style: "reply",
		Text: `[
  {
    "id": "step-2",
    "goal": "查看测试文档内容",
    "tool": "file.read",
    "args": {"path": "/tmp/测试文档.md"},
    "risk": "safe_read",
    "requires_confirm": false
  }
]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "file.read") || strings.Contains(content, `"tool"`) {
		t.Fatalf("expected json tool plan stripped, got %s", content)
	}
	if !strings.Contains(content, "已处理完成") {
		t.Fatalf("expected fallback text, got %s", content)
	}
}
