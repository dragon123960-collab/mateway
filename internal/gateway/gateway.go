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
	"github.com/dongping/mateway/internal/channel/weixin"
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
	Home    string
}

type channelStarter func(context.Context, channelRuntime) error

type channelSpec struct {
	Name    string
	Enabled bool
	Start   channelStarter
}

func enabledChannelStarters(ctx context.Context, cfg Config, dedupe *inboundDedupe) []func() error {
	rt := channelRuntime{Runtime: cfg.Runtime, Dedupe: dedupe, Home: cfg.Config.App.Home}
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
		weixinChannelSpec(cfg.Config.Channels.Weixin),
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
				downloaded, err := sender.DownloadMessageImages(eventCtx, msg, rt.Home)
				if err != nil {
					log.Printf("mateway gateway feishu media download error message_id=%s session=%s: %v", msg.ID, msg.SessionKey, err)
					_ = sender.Reply(eventCtx, msg, channel.OutboundMessage{Channel: msg.Channel, ThreadID: msg.ThreadID, Text: "图片下载失败：" + err.Error(), Style: "error"})
					return nil
				}
				msg = downloaded
				go runFeishuMessage(rt.Runtime, sender, msg)
				return nil
			})
		},
	}
}

func weixinChannelSpec(channelCfg config.WeixinConfig) channelSpec {
	return channelSpec{
		Name:    "weixin",
		Enabled: channelCfg.Enabled,
		Start: func(ctx context.Context, rt channelRuntime) error {
			return weixin.Start(ctx, channelCfg, rt.Home, func(eventCtx context.Context, msg channel.InboundMessage) (channel.OutboundBatch, error) {
				if shouldIgnoreGeneric(msg) || prepareInbound(&msg, rt.Dedupe) {
					return channel.OutboundBatch{}, nil
				}
				resp, err := runRuntimeMessage(eventCtx, rt.Runtime, msg)
				return channel.OutboundBatch{Reply: resp.Reply, FollowUps: resp.FollowUps}, err
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
	return !msg.HasContent()
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
		"follow_up_count":     len(resp.FollowUps),
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
	if shouldSendProcessingAck(rt, msg) {
		id, ackErr := sender.ReplyWithID(runCtx, msg, channel.OutboundMessage{
			Channel:  msg.Channel,
			ThreadID: msg.ThreadID,
			Text:     "收到，开始处理。",
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
	for _, followUp := range resp.FollowUps {
		if strings.TrimSpace(followUp.Text) == "" {
			continue
		}
		if err := sender.Reply(runCtx, msg, followUp); err != nil {
			log.Printf("mateway gateway follow-up reply error message_id=%s session=%s: %v", msg.ID, msg.SessionKey, err)
		}
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

func shouldSendProcessingAck(rt runtime.Runtime, msg channel.InboundMessage) bool {
	if isCardAction(msg) {
		return false
	}
	state, err := rt.Store.Load(msg.SessionKey)
	if err == nil && state.Pending != nil {
		return false
	}
	return !isSlashCommand(msg.Text)
}

func isSlashCommand(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "/")
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
	if !msg.HasContent() {
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(msg.Channel), "feishu") {
		return false
	}
	messageType := strings.TrimSpace(msg.Metadata["message_type"])
	if messageType != "" && messageType != "text" && messageType != "image" && !isCardAction(msg) {
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
	case "approval_pending", "input_required", "clarify", "partial":
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
