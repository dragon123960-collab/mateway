package channel

import "testing"

func TestOutboundBatchMessagesKeepsOrderAndSkipsEmptyText(t *testing.T) {
	batch := OutboundBatch{
		Reply: OutboundMessage{Text: "main"},
		FollowUps: []OutboundMessage{
			{Text: ""},
			{Text: "follow 1"},
			{Text: "follow 2"},
		},
	}

	messages := batch.Messages()
	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %#v", messages)
	}
	if messages[0].Text != "main" || messages[1].Text != "follow 1" || messages[2].Text != "follow 2" {
		t.Fatalf("messages out of order: %#v", messages)
	}
}

func TestOutboundBatchMessagesAllowsOnlyFollowUps(t *testing.T) {
	batch := OutboundBatch{
		FollowUps: []OutboundMessage{
			{Text: "follow"},
		},
	}

	messages := batch.Messages()
	if len(messages) != 1 || messages[0].Text != "follow" {
		t.Fatalf("expected only follow-up message, got %#v", messages)
	}
}
