package feishu

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/channel"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
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

func TestNormalizeMessageReceiveCapturesImagePart(t *testing.T) {
	messageID := "om_img"
	chatID := "oc_1"
	content := `{"image_key":"img_v2_abc"}`
	messageType := "image"
	event := &larkim.P2MessageReceiveV1{Event: &larkim.P2MessageReceiveV1Data{
		Message: &larkim.EventMessage{
			MessageId:   &messageID,
			ChatId:      &chatID,
			Content:     &content,
			MessageType: &messageType,
		},
	}}
	msg := NormalizeMessageReceive(event)
	if msg.Metadata["image_key"] != "img_v2_abc" {
		t.Fatalf("expected image key metadata, got %#v", msg.Metadata)
	}
	if len(msg.Parts) != 1 || msg.Parts[0].Type != channel.PartImage || msg.Parts[0].Metadata["image_key"] != "img_v2_abc" {
		t.Fatalf("unexpected image parts %#v", msg.Parts)
	}
}

func TestRenderReplyMessageStripsToolCallEcho(t *testing.T) {
	msgType, content, err := renderReplyMessage(channel.OutboundMessage{
		Style: "reply",
		Text:  "[TOOL_CALL]\n{\"tool\":\"file.read\",\"args\":{\"path\":\"README.md\"}}\n[/TOOL_CALL]\n\n最终结论。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if msgType != "interactive" {
		t.Fatalf("expected interactive card, got %q", msgType)
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
	if !strings.Contains(content, "Done.") {
		t.Fatalf("expected fallback text, got %s", content)
	}
}

func TestRenderReplyMessageStripsStructuredJSONBlock(t *testing.T) {
	_, content, err := renderReplyMessage(channel.OutboundMessage{
		Style: "reply",
		Text:  "```json\n{\"summary\":\"今晚看点\",\"matches\":[]}\n```",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "```json") || strings.Contains(content, `"matches"`) {
		t.Fatalf("expected structured json stripped, got %s", content)
	}
	if !strings.Contains(content, "Done.") {
		t.Fatalf("expected fallback text, got %s", content)
	}
}

func TestRenderReplyMessageUsesEnglishRuntimeText(t *testing.T) {
	_, content, err := renderReplyMessage(channel.OutboundMessage{
		Channel:  "feishu",
		ThreadID: "thread_123",
		Style:    "input_required",
		Text:     "Please provide the missing path.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "Mateway Needs More Information") || !strings.Contains(content, "Please reply directly with the missing information.") {
		t.Fatalf("expected English input-required card, got %s", content)
	}
}

func TestRenderReplyMessageUsesConfirmationFooter(t *testing.T) {
	_, content, err := renderReplyMessage(channel.OutboundMessage{
		Channel:  "feishu",
		ThreadID: "thread_123",
		Style:    "input_required",
		Text:     "Approve publish.\n\nReply 1 to confirm and continue, or 2 to cancel and block this task.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "Mateway Needs Confirmation") || !strings.Contains(content, "Reply with 1 to confirm and continue, or 2 to cancel.") {
		t.Fatalf("expected confirmation card, got %s", content)
	}
	if strings.Contains(content, "missing information") {
		t.Fatalf("confirmation card should not ask for missing information, got %s", content)
	}
}

func TestNormalizeCardActionMapsToConfirmMessage(t *testing.T) {
	openMessageID := "om_card"
	openChatID := "oc_card"
	userID := "ou_user"
	event := &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: userID},
			Context: &callback.Context{
				OpenMessageID: openMessageID,
				OpenChatID:    openChatID,
			},
			Action: &callback.CallBackAction{
				Value: map[string]any{
					"decision":            "confirm",
					"mateway_text":        "确认",
					"mateway_thread_id":   "thread_123",
					"mateway_session_key": "feishu:thread_123",
				},
			},
		},
	}
	msg := NormalizeCardAction(event)
	if msg.Text != "确认" {
		t.Fatalf("expected confirm text, got %q", msg.Text)
	}
	if msg.ThreadID != "thread_123" {
		t.Fatalf("expected thread id from card value, got %q", msg.ThreadID)
	}
	if msg.SessionKey != "feishu:thread_123" {
		t.Fatalf("expected session key from card value, got %q", msg.SessionKey)
	}
	if msg.UserID != userID {
		t.Fatalf("expected user id %q, got %q", userID, msg.UserID)
	}
	if msg.Metadata["card_action"] != "confirm" {
		t.Fatalf("expected card action confirm, got %#v", msg.Metadata)
	}
}

func TestRenderReplyMessageProducesValidCardJSON(t *testing.T) {
	_, content, err := renderReplyMessage(channel.OutboundMessage{
		Style: "input_required",
		Text:  "请告诉我要安装的软件名称。",
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("expected valid card json: %v\n%s", err, content)
	}
	if payload["header"] == nil || payload["elements"] == nil {
		t.Fatalf("expected header and elements in card payload, got %#v", payload)
	}
}

func TestRenderReplyMessageUsesPartialFooter(t *testing.T) {
	_, content, err := renderReplyMessage(channel.OutboundMessage{
		Style: "partial",
		Text:  "任务还没有完成。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "Status: partial") {
		t.Fatalf("expected partial footer, got %s", content)
	}
	if strings.Contains(content, "Status: completed") || strings.Contains(content, "DONE") {
		t.Fatalf("partial card should not look completed, got %s", content)
	}
}

func TestRenderReplyMessageIncludesProgress(t *testing.T) {
	_, content, err := renderReplyMessage(channel.OutboundMessage{
		Text:  "Processing...",
		Style: "processing",
		Progress: []channel.ProgressStep{
			{Tool: "file.read", Status: "accepted", Summary: "listed files", DurationMS: 42},
			{Tool: "terminal.run", Status: "failed", Summary: strings.Repeat("long ", 40), DurationMS: 1000, TimedOut: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Progress", "Read", "Run", "timed out"} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected %q in progress card, got %s", want, content)
		}
	}
	if strings.Contains(content, strings.Repeat("long ", 20)) {
		t.Fatalf("expected long progress summary to be truncated, got %s", content)
	}
}

func TestRenderReplyMessageHidesEmptyThinkingProgress(t *testing.T) {
	_, content, err := renderReplyMessage(channel.OutboundMessage{
		Text:  "Processing...",
		Style: "processing",
		Progress: []channel.ProgressStep{
			{Title: "model", Status: "thinking", Summary: "waiting for model output"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "Progress") || strings.Contains(content, "thinking") {
		t.Fatalf("empty thinking progress should be hidden, got %s", content)
	}
}

func TestRenderReplyMessageProgressTruncatesUTF8Safely(t *testing.T) {
	_, content, err := renderReplyMessage(channel.OutboundMessage{
		Text:  "Processing...",
		Style: "processing",
		Progress: []channel.ProgressStep{
			{Tool: "web.search", Status: "running", Summary: strings.Repeat("今晚世界杯赛程", 30)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "Search") || !strings.Contains(content, "今晚世界杯") {
		t.Fatalf("expected UTF-8 progress summary, got %s", content)
	}
}
