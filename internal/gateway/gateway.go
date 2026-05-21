package gateway

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/dongping/mateway/internal/app"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/channel/feishu"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/heartbeat"
	"github.com/dongping/mateway/internal/schedule"
)

type Gateway struct {
	App       *app.App
	Sender    *feishu.Sender
	inflight  *messageDedupe
	sessions  *sessionLocks
	workerCtx context.Context
}

func New(a *app.App) Gateway {
	sender := feishu.NewSender(a.Config.Channels.Feishu)
	return Gateway{App: a, Sender: sender, inflight: newMessageDedupe(10 * time.Minute), sessions: newSessionLocks()}
}

func (g Gateway) Serve(ctx context.Context) error {
	g.workerCtx = ctx
	heartbeat.NewScheduler(g.App.Config).Start(ctx)
	schedule.NewScheduler(g.App.Config, func(ctx context.Context, msg channel.InboundMessage) (schedule.Response, error) {
		resp, err := g.App.Runtime.Handle(ctx, msg)
		return schedule.Response{Reply: resp.Reply, TraceID: resp.TraceID, Failed: resp.Failed}, err
	}).Start(ctx)
	return feishu.StartWebSocket(ctx, g.App.Config.Channels.Feishu, g.HandleInbound)
}

func (g Gateway) HandleInbound(ctx context.Context, msg channel.InboundMessage) error {
	if shouldIgnore(g.App.Config.Channels.Feishu, msg) {
		return nil
	}
	if msg.SessionKey == "" {
		msg.SessionKey = SessionKey(msg)
	}
	if !g.inflight.Begin(msg.ID) {
		g.App.Runtime.Logger.Event("gateway.duplicate_message", map[string]any{"channel": msg.Channel, "message_id": msg.ID, "session_key": msg.SessionKey})
		return nil
	}
	workerCtx := g.workerCtx
	if workerCtx == nil {
		workerCtx = context.Background()
	}
	go g.processInbound(workerCtx, msg)
	return nil
}

func (g Gateway) processInbound(ctx context.Context, msg channel.InboundMessage) {
	defer g.inflight.Done(msg.ID)
	unlock := g.sessions.Lock(msg.SessionKey)
	defer unlock()
	_ = g.Sender.React(ctx, msg.ID, "SMILE")
	rt := g.App.Runtime
	rt.Observer = nil
	resp, err := rt.Handle(ctx, msg)
	if err != nil {
		_ = g.Sender.React(ctx, msg.ID, "CROSS_MARK")
		_ = g.Sender.Reply(ctx, msg, channel.OutboundMessage{Channel: msg.Channel, ThreadID: msg.ThreadID, Text: "处理失败：" + err.Error(), Style: "error"})
		return
	}
	if err := g.Sender.Reply(ctx, msg, resp.Reply); err != nil {
		_ = g.Sender.React(ctx, msg.ID, "CROSS_MARK")
		return
	}
	if strings.TrimSpace(resp.Reply.Style) == "approval_pending" || strings.TrimSpace(resp.Reply.Style) == "input_required" {
		_ = g.Sender.React(ctx, msg.ID, "EYES")
	} else {
		_ = g.Sender.React(ctx, msg.ID, "DONE")
	}
}

func shouldIgnore(cfg config.FeishuConfig, msg channel.InboundMessage) bool {
	if strings.TrimSpace(msg.Text) == "" {
		return true
	}
	if msg.Metadata["message_type"] != "" && msg.Metadata["message_type"] != "text" {
		return true
	}
	if strings.EqualFold(msg.Metadata["sender_type"], "app") {
		return true
	}
	if msg.Channel == "feishu" && cfg.MentionRequiredGroup && msg.Metadata["chat_type"] == "group" && msg.Metadata["is_mentioned"] != "true" {
		return true
	}
	return false
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

type messageDedupe struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]dedupeEntry
}

type dedupeEntry struct {
	doneAt time.Time
	active bool
}

func newMessageDedupe(ttl time.Duration) *messageDedupe {
	return &messageDedupe{ttl: ttl, entries: map[string]dedupeEntry{}}
}

func (d *messageDedupe) Begin(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	for key, entry := range d.entries {
		if !entry.active && now.Sub(entry.doneAt) > d.ttl {
			delete(d.entries, key)
		}
	}
	if entry, ok := d.entries[id]; ok {
		if entry.active || now.Sub(entry.doneAt) <= d.ttl {
			return false
		}
	}
	d.entries[id] = dedupeEntry{active: true}
	return true
}

func (d *messageDedupe) Done(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.entries[id] = dedupeEntry{doneAt: time.Now()}
}

type sessionLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newSessionLocks() *sessionLocks {
	return &sessionLocks{locks: map[string]*sync.Mutex{}}
}

func (s *sessionLocks) Lock(sessionKey string) func() {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		sessionKey = "unknown:default"
	}
	s.mu.Lock()
	lock := s.locks[sessionKey]
	if lock == nil {
		lock = &sync.Mutex{}
		s.locks[sessionKey] = lock
	}
	s.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}
