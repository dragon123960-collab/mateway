package bridge

import (
	"testing"
)

func TestEventToInboundUsesAccountChannelPeerSession(t *testing.T) {
	event := Event{
		ID:        "m1",
		Channel:   "wechat_personal",
		AccountID: "acct",
		PeerID:    "peer",
		UserID:    "user",
		ChatType:  "group",
		Text:      "hello",
	}
	msg := event.ToInbound()
	if msg.SessionKey != "wechat_personal:acct:peer" {
		t.Fatalf("SessionKey = %q", msg.SessionKey)
	}
	if msg.Channel != "wechat_personal" || msg.ThreadID != "peer" || msg.UserID != "user" {
		t.Fatalf("unexpected inbound: %#v", msg)
	}
	if msg.Metadata["account_id"] != "acct" || msg.Metadata["chat_type"] != "group" {
		t.Fatalf("metadata = %#v", msg.Metadata)
	}
}

func TestValidateEventRejectsUnsupportedAttachments(t *testing.T) {
	err := validateEvent(Event{ID: "m1", Channel: "demo", Text: "hello", Attachments: []Attachment{{Type: "image"}}})
	if err == nil {
		t.Fatal("expected unsupported attachment error")
	}
}
