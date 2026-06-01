package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
)

func TestSessionKeyUsesChannelNamespace(t *testing.T) {
	got := SessionKey(channel.InboundMessage{Channel: "feishu", ThreadID: "thread-1", UserID: "user-1"})
	if got != "feishu:thread-1" {
		t.Fatalf("SessionKey = %q", got)
	}
}

func TestInstanceLockRejectsSecondHolder(t *testing.T) {
	home := t.TempDir()
	lock, err := AcquireInstanceLock(home)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if RunningPIDFromLock(home) == 0 {
		t.Fatal("expected pid in lock")
	}
	second, err := AcquireInstanceLock(home)
	if err == nil {
		_ = second.Close()
		t.Fatal("expected second lock to fail")
	}
}

func TestServiceStatusUnsupportedOSStillIncludesLockLine(t *testing.T) {
	text, err := (ServiceManager{GOOS: "plan9"}).Status(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("expected unsupported os error")
	}
	if !strings.Contains(text, "mateway serve lock") {
		t.Fatalf("status text = %q", text)
	}
}

func TestShouldIgnoreFeishuSelfMessages(t *testing.T) {
	if !shouldIgnoreInbound(config.FeishuConfig{}, channel.InboundMessage{Channel: "feishu", Text: "hello", Metadata: map[string]string{"sender_type": "app", "message_type": "text"}}) {
		t.Fatal("expected app sender ignored")
	}
	if shouldIgnoreInbound(config.FeishuConfig{}, channel.InboundMessage{Channel: "cli", Text: "hello", Metadata: map[string]string{"sender_type": "app"}}) {
		t.Fatal("expected non-feishu app sender not ignored")
	}
}

func TestShouldIgnoreFeishuNonTextEvents(t *testing.T) {
	if !shouldIgnoreInbound(config.FeishuConfig{}, channel.InboundMessage{Channel: "feishu", Text: "reaction", Metadata: map[string]string{"message_type": "reaction"}}) {
		t.Fatal("expected non-text event ignored")
	}
	if shouldIgnoreInbound(config.FeishuConfig{}, channel.InboundMessage{Channel: "feishu", Text: "确认", Metadata: map[string]string{"message_type": "interactive", "card_action": "confirm"}}) {
		t.Fatal("expected card action accepted")
	}
}

func TestShouldIgnoreFeishuGroupWithoutMentionWhenRequired(t *testing.T) {
	cfg := config.FeishuConfig{MentionRequiredGroup: true}
	msg := channel.InboundMessage{Channel: "feishu", Text: "hello", Metadata: map[string]string{"message_type": "text", "chat_type": "group", "is_mentioned": "false"}}
	if !shouldIgnoreInbound(cfg, msg) {
		t.Fatal("expected group message without mention ignored")
	}
	msg.Metadata["is_mentioned"] = "true"
	if shouldIgnoreInbound(cfg, msg) {
		t.Fatal("expected mentioned group message accepted")
	}
}

func TestReactionForReply(t *testing.T) {
	cases := map[string]string{
		"approval_pending": "EYES",
		"input_required":   "EYES",
		"clarify":          "EYES",
		"error":            "CROSS_MARK",
		"cancelled":        "CROSS_MARK",
		"completed":        "DONE",
		"processing":       "DONE",
		"":                 "DONE",
	}
	for style, want := range cases {
		if got := reactionForReply(channel.OutboundMessage{Style: style}); got != want {
			t.Fatalf("style %q reaction = %q want %q", style, got, want)
		}
	}
}

func TestInboundDedupeUsesMessageID(t *testing.T) {
	dedupe := newInboundDedupe(time.Minute)
	msg := channel.InboundMessage{ID: "om_1", Channel: "feishu"}
	if dedupe.Seen(msg) {
		t.Fatal("first message should not be duplicate")
	}
	if !dedupe.Seen(msg) {
		t.Fatal("second message should be duplicate")
	}
	if dedupe.Seen(channel.InboundMessage{ID: "om_2", Channel: "feishu"}) {
		t.Fatal("different message should not be duplicate")
	}
}

func TestBuiltinChannelSpecsExposeEnabledChannels(t *testing.T) {
	cfg := Config{Config: &config.Root{}}
	cfg.Config.Channels.Feishu.Enabled = true
	cfg.Config.Channels.Weixin.Enabled = true
	specs := builtinChannelSpecs(cfg)
	var enabled []string
	for _, spec := range specs {
		if spec.Enabled {
			enabled = append(enabled, spec.Name)
		}
	}
	got := strings.Join(enabled, ",")
	if got != "feishu,weixin" {
		t.Fatalf("enabled specs = %q", got)
	}
}
