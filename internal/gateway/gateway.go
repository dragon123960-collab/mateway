package gateway

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/channel/feishu"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/runtime"
)

type Config struct {
	Config  *config.Root
	Runtime runtime.Runtime
}

func Serve(ctx context.Context, cfg Config) error {
	if cfg.Config == nil {
		return fmt.Errorf("gateway config is required")
	}
	lock, err := AcquireInstanceLock(cfg.Config.App.Home)
	if err != nil {
		return err
	}
	defer lock.Close()
	if !cfg.Config.Channels.Feishu.Enabled {
		return fmt.Errorf("no enabled channel: feishu is disabled")
	}
	sender := feishu.NewSender(cfg.Config.Channels.Feishu)
	dedupe := newInboundDedupe(30 * time.Minute)
	return feishu.StartWebSocket(ctx, cfg.Config.Channels.Feishu, func(eventCtx context.Context, msg channel.InboundMessage) error {
		if msg.SessionKey == "" {
			msg.SessionKey = SessionKey(msg)
		}
		if shouldIgnoreInbound(cfg.Config.Channels.Feishu, msg) || dedupe.Seen(msg) {
			return nil
		}
		go func() {
			start := time.Now()
			runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			cardAction := isCardAction(msg)
			if !cardAction {
				react(runCtx, sender, msg.ID, "SMILE")
			}
			runtimeStart := time.Now()
			resp, err := cfg.Runtime.Handle(runCtx, msg)
			runtimeDuration := time.Since(runtimeStart)
			if err != nil {
				log.Printf("mateway gateway runtime error message_id=%s session=%s: %v", msg.ID, msg.SessionKey, err)
				if !cardAction {
					react(runCtx, sender, msg.ID, "CROSS_MARK")
				}
				_ = sender.Reply(runCtx, msg, channel.OutboundMessage{Channel: msg.Channel, ThreadID: msg.ThreadID, Text: "处理失败：" + err.Error(), Style: "error"})
				return
			}
			replyStart := time.Now()
			if err := sender.Reply(runCtx, msg, resp.Reply); err != nil {
				log.Printf("mateway gateway reply error message_id=%s session=%s: %v", msg.ID, msg.SessionKey, err)
				_ = runtime.AppendTraceEvent(resp.TracePath, map[string]any{
					"type":                "gateway_done",
					"message_id":          msg.ID,
					"session_key":         msg.SessionKey,
					"runtime_duration_ms": runtimeDuration.Milliseconds(),
					"reply_duration_ms":   time.Since(replyStart).Milliseconds(),
					"total_duration_ms":   time.Since(start).Milliseconds(),
					"reply_error":         err.Error(),
				})
				if !cardAction {
					react(runCtx, sender, msg.ID, "CROSS_MARK")
				}
				return
			}
			replyDuration := time.Since(replyStart)
			if !cardAction {
				react(runCtx, sender, msg.ID, reactionForReply(resp.Reply))
			}
			_ = runtime.AppendTraceEvent(resp.TracePath, map[string]any{
				"type":                "gateway_done",
				"message_id":          msg.ID,
				"session_key":         msg.SessionKey,
				"runtime_duration_ms": runtimeDuration.Milliseconds(),
				"reply_duration_ms":   replyDuration.Milliseconds(),
				"total_duration_ms":   time.Since(start).Milliseconds(),
				"reply_style":         resp.Reply.Style,
				"failed":              resp.Failed,
			})
		}()
		return nil
	})
}

func SessionKey(msg channel.InboundMessage) string {
	channelName := strings.TrimSpace(msg.Channel)
	if channelName == "" {
		channelName = "unknown"
	}
	threadID := strings.TrimSpace(msg.ThreadID)
	if threadID == "" {
		threadID = strings.TrimSpace(msg.UserID)
	}
	if threadID == "" {
		threadID = strings.TrimSpace(msg.ID)
	}
	if threadID == "" {
		threadID = "default"
	}
	return channelName + ":" + threadID
}

func shouldIgnoreInbound(cfg config.FeishuConfig, msg channel.InboundMessage) bool {
	if strings.TrimSpace(msg.Text) == "" {
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(msg.Channel), "feishu") {
		return false
	}
	messageType := strings.TrimSpace(msg.Metadata["message_type"])
	if messageType != "" && messageType != "text" && !isCardAction(msg) {
		return true
	}
	senderType := strings.ToLower(strings.TrimSpace(msg.Metadata["sender_type"]))
	if senderType == "app" || senderType == "bot" || senderType == "self" {
		return true
	}
	return cfg.MentionRequiredGroup && msg.Metadata["chat_type"] == "group" && msg.Metadata["is_mentioned"] != "true"
}

func isCardAction(msg channel.InboundMessage) bool {
	return msg.Metadata["message_type"] == "interactive" && strings.TrimSpace(msg.Metadata["card_action"]) != ""
}

func reactionForReply(reply channel.OutboundMessage) string {
	switch strings.TrimSpace(reply.Style) {
	case "approval_pending", "input_required", "clarify":
		return "EYES"
	case "error", "cancelled":
		return "CROSS_MARK"
	default:
		return "DONE"
	}
}

func react(ctx context.Context, sender *feishu.Sender, messageID, emoji string) {
	if sender == nil || strings.TrimSpace(messageID) == "" || strings.TrimSpace(emoji) == "" {
		return
	}
	if err := sender.React(ctx, messageID, emoji); err != nil {
		log.Printf("mateway gateway reaction error message_id=%s emoji=%s: %v", messageID, emoji, err)
	}
}

type inboundDedupe struct {
	mu   sync.Mutex
	ttl  time.Duration
	seen map[string]time.Time
}

func newInboundDedupe(ttl time.Duration) *inboundDedupe {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &inboundDedupe{ttl: ttl, seen: map[string]time.Time{}}
}

func (d *inboundDedupe) Seen(msg channel.InboundMessage) bool {
	key := inboundDedupeKey(msg)
	if key == "" {
		return false
	}
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	for existing, at := range d.seen {
		if now.Sub(at) > d.ttl {
			delete(d.seen, existing)
		}
	}
	if _, ok := d.seen[key]; ok {
		return true
	}
	d.seen[key] = now
	return false
}

func inboundDedupeKey(msg channel.InboundMessage) string {
	channelName := strings.TrimSpace(msg.Channel)
	if channelName == "" {
		channelName = "unknown"
	}
	id := strings.TrimSpace(msg.ID)
	if id == "" {
		return ""
	}
	if action := strings.TrimSpace(msg.Metadata["card_action"]); action != "" {
		id += ":" + action + ":" + strings.TrimSpace(msg.UserID)
	}
	return channelName + ":" + id
}
