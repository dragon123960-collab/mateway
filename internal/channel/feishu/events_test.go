package feishu

import (
	"testing"

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
