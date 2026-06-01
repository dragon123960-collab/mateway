package gateway

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/channel/bridge"
	"github.com/dongping/mateway/internal/channel/feishu"
	"github.com/dongping/mateway/internal/channel/openclawcompat"
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
	dedupe := newInboundDedupe(30 * time.Minute)
	starters := enabledChannelStarters(ctx, cfg, dedupe)
	if len(starters) == 0 {
		return fmt.Errorf("no enabled channel")
	}
	return runStarters(ctx, starters)
}

type channelRuntime struct {
	Runtime runtime.Runtime
	Dedupe  *inboundDedupe
}

type channelStarter func(context.Context, channelRuntime) error

type channelSpec struct {
	Name    string
	Enabled bool
	Start   channelStarter
}

func enabledChannelStarters(ctx context.Context, cfg Config, dedupe *inboundDedupe) []func() error {
	rt := channelRuntime{Runtime: cfg.Runtime, Dedupe: dedupe}
	specs := builtinChannelSpecs(cfg)
	starters := make([]func() error, 0, len(specs))
	for _, spec := range specs {
		if !spec.Enabled || spec.Start == nil {
			continue
		}
		spec := spec
		starters = append(starters, func() error {
			return spec.Start(ctx, rt)
		})
	}
	return starters
}

func builtinChannelSpecs(cfg Config) []channelSpec {
	if cfg.Config == nil {
		return nil
	}
	return []channelSpec{
		feishuChannelSpec(cfg.Config.Channels.Feishu),
		bridgeChannelSpec(cfg.Config.Channels.Bridge),
		openClawCompatChannelSpec(cfg.Config.Channels.OpenClawCompat),
	}
}

func feishuChannelSpec(channelCfg config.FeishuConfig) channelSpec {
	return channelSpec{
		Name:    "feishu",
		Enabled: channelCfg.Enabled,
		Start: func(ctx context.Context, rt channelRuntime) error {
			sender := feishu.NewSender(channelCfg)
			return feishu.StartWebSocket(ctx, channelCfg, func(eventCtx context.Context, msg channel.InboundMessage) error {
				if shouldIgnoreInbound(channelCfg, msg) || prepareInbound(&msg, rt.Dedupe) {
					return nil
				}
				go runFeishuMessage(rt.Runtime, sender, msg)
				return nil
			})
		},
	}
}

func bridgeChannelSpec(channelCfg config.BridgeConfig) channelSpec {
	return channelSpec{
		Name:    "bridge",
		Enabled: channelCfg.Enabled,
		Start: func(ctx context.Context, rt channelRuntime) error {
			return bridge.Start(ctx, channelCfg, func(eventCtx context.Context, event bridge.Event) (bridge.Reply, error) {
				msg := event.ToInbound()
				if shouldIgnoreGeneric(msg) || prepareInbound(&msg, rt.Dedupe) {
					return bridge.Reply{}, nil
				}
				resp, err := runRuntimeMessage(eventCtx, rt.Runtime, msg)
				if err != nil {
					return bridge.Reply{}, err
				}
				return bridge.OutboundToReply(event, resp.Reply), nil
			})
		},
	}
}

func openClawCompatChannelSpec(channelCfg config.OpenClawCompatConfig) channelSpec {
	return channelSpec{
		Name:    "openclaw_compat",
		Enabled: channelCfg.Enabled,
		Start: func(ctx context.Context, rt channelRuntime) error {
			return openclawcompat.Start(ctx, channelCfg, func(eventCtx context.Context, msg channel.InboundMessage) (channel.OutboundMessage, error) {
				if shouldIgnoreGeneric(msg) || prepareInbound(&msg, rt.Dedupe) {
					return channel.OutboundMessage{}, nil
				}
				resp, err := runRuntimeMessage(eventCtx, rt.Runtime, msg)
				return resp.Reply, err
			})
		},
	}
}

func runStarters(ctx context.Context, starters []func() error) error {
	errCh := make(chan error, len(starters))
	for _, starter := range starters {
		go func(start func() error) {
			errCh <- start()
		}(starter)
	}
	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func prepareInbound(msg *channel.InboundMessage, dedupe *inboundDedupe) bool {
	if msg.SessionKey == "" {
		msg.SessionKey = SessionKey(*msg)
	}
	return dedupe.Seen(*msg)
}

func shouldIgnoreGeneric(msg channel.InboundMessage) bool {
	return strings.TrimSpace(msg.Text) == ""
}

func runRuntimeMessage(ctx context.Context, rt runtime.Runtime, msg channel.InboundMessage) (runtime.Response, error) {
	start := time.Now()
	runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if ctx != nil {
		go func() {
			<-ctx.Done()
			cancel()
		}()
	}
	runtimeStart := time.Now()
	resp, err := rt.Handle(runCtx, msg)
	runtimeDuration := time.Since(runtimeStart)
	if err != nil {
		return resp, err
	}
	_ = runtime.AppendTraceEvent(resp.TracePath, map[string]any{
		"type":                "gateway_done",
		"message_id":          msg.ID,
		"session_key":         msg.SessionKey,
		"runtime_duration_ms": runtimeDuration.Milliseconds(),
		"reply_duration_ms":   int64(0),
		"total_duration_ms":   time.Since(start).Milliseconds(),
		"reply_style":         resp.Reply.Style,
		"failed":              resp.Failed,
	})
	return resp, nil
}

func runFeishuMessage(rt runtime.Runtime, sender *feishu.Sender, msg channel.InboundMessage) {
	start := time.Now()
	runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cardAction := isCardAction(msg)
	if !cardAction {
		react(runCtx, sender, msg.ID, "SMILE")
	}
	ackMessageID := ""
	if !cardAction {
		id, ackErr := sender.ReplyWithID(runCtx, msg, channel.OutboundMessage{
			Channel:  msg.Channel,
			ThreadID: msg.ThreadID,
			Text:     "收到，开始处理。需要执行本地检查或安装时，我会在完成后更新这条回复。",
			Style:    "processing",
		}, msg.ID+":processing")
		if ackErr != nil {
			log.Printf("mateway gateway processing ack error message_id=%s session=%s: %v", msg.ID, msg.SessionKey, ackErr)
		}
		ackMessageID = id
	}
	runtimeStart := time.Now()
	resp, err := rt.Handle(runCtx, msg)
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
	if err := sendFinalReply(runCtx, sender, msg, ackMessageID, resp.Reply); err != nil {
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
}

func sendFinalReply(ctx context.Context, sender *feishu.Sender, msg channel.InboundMessage, ackMessageID string, reply channel.OutboundMessage) error {
	if sender == nil {
		return fmt.Errorf("feishu sender is required")
	}
	if strings.TrimSpace(ackMessageID) != "" {
		if err := sender.Update(ctx, ackMessageID, reply); err == nil {
			return nil
		}
	}
	return sender.Reply(ctx, msg, reply)
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
