package gateway

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/channel/feishu"
	"github.com/dongping/mateway/internal/channel/weixin"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/model"
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
	starters = append(starters, enabledHeartbeatStarters(ctx, cfg.Config)...)
	return starters
}

func builtinChannelSpecs(cfg Config) []channelSpec {
	if cfg.Config == nil {
		return nil
	}
	specs := feishuChannelSpecs(cfg.Config.Channels.Feishu)
	specs = append(specs, weixinChannelSpec(cfg.Config.Channels.Weixin))
	return specs
}

func feishuChannelSpecs(channelCfg config.FeishuConfig) []channelSpec {
	accounts := channelCfg.AccountConfigs()
	if len(accounts) == 0 {
		accounts = []config.FeishuConfig{channelCfg}
	}
	specs := make([]channelSpec, 0, len(accounts))
	for _, accountCfg := range accounts {
		name := "feishu"
		if accountID := strings.TrimSpace(accountCfg.DefaultAccount); accountID != "" && accountID != "default" {
			name = "feishu:" + accountID
		}
		specs = append(specs, feishuChannelSpec(name, accountCfg))
	}
	return specs
}

func feishuChannelSpec(name string, channelCfg config.FeishuConfig) channelSpec {
	return channelSpec{
		Name:    name,
		Enabled: channelCfg.Enabled,
		Start: func(ctx context.Context, rt channelRuntime) error {
			log.Printf("mateway feishu channel starting account=%s", strings.TrimSpace(channelCfg.DefaultAccount))
			sender := feishu.NewSender(channelCfg)
			return feishu.StartWebSocket(ctx, channelCfg, func(eventCtx context.Context, msg channel.InboundMessage) error {
				if shouldIgnoreInbound(channelCfg, msg) || prepareInbound(&msg, rt.Dedupe) {
					return nil
				}
				downloaded, err := sender.DownloadMessageImages(eventCtx, msg, rt.Home)
				if err != nil {
					log.Printf("mateway gateway feishu media download error message_id=%s session=%s: %v", msg.ID, msg.SessionKey, err)
					_ = sender.Reply(eventCtx, msg, channel.OutboundMessage{Channel: msg.Channel, ThreadID: msg.ThreadID, Text: gatewayText(rt.Runtime.Config, msg, "gateway.media_download_failed", map[string]string{"error": err.Error()}), Style: "error"})
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

func enabledHeartbeatStarters(ctx context.Context, cfg *config.Root) []func() error {
	if cfg == nil {
		return nil
	}
	cfg.NormalizeForUse()
	seen := map[string]bool{}
	var starters []func() error
	for _, profile := range cfg.Agents.Profiles {
		if !profile.Heartbeat.Enabled {
			continue
		}
		input := heartbeatInputForProfile(cfg, profile)
		key := strings.Join([]string{
			input.MemoryRoot,
			input.Interval.String(),
			strings.Join(sortedHeartbeatJobs(input.Jobs), ","),
		}, "|")
		if seen[key] {
			continue
		}
		seen[key] = true
		profileID := strings.TrimSpace(profile.ID)
		starters = append(starters, func() error {
			log.Printf("mateway heartbeat starting profile=%s interval=%s jobs=%s", profileID, input.Interval, strings.Join(memory.NormalizeHeartbeatJobs(input.Jobs), ","))
			return memory.ServeHeartbeat(ctx, input)
		})
	}
	return starters
}

func sortedHeartbeatJobs(jobs []string) []string {
	normalized := memory.NormalizeHeartbeatJobs(jobs)
	sort.Strings(normalized)
	return normalized
}

func heartbeatInputForProfile(cfg *config.Root, profile config.AgentProfileConfig) memory.HeartbeatServeInput {
	interval := 30 * time.Minute
	if parsed, err := time.ParseDuration(strings.TrimSpace(profile.Heartbeat.Interval)); err == nil && parsed > 0 {
		interval = parsed
	}
	workspace := strings.TrimSpace(cfg.App.Workspace)
	if workspace == "" {
		workspace = filepath.Join(cfg.App.Home, "workspace")
	}
	memoryRoot := strings.TrimSpace(cfg.Memory.Root)
	if memoryRoot == "" {
		memoryRoot = filepath.Join(workspace, "memory")
	}
	return memory.HeartbeatServeInput{
		Home:       cfg.App.Home,
		Workspace:  workspace,
		MemoryRoot: memoryRoot,
		IndexPath:  filepath.Join(cfg.App.Home, "indexes", "memory_index.json"),
		Interval:   interval,
		Jobs:       profile.Heartbeat.Jobs,
		Model:      heartbeatDistillModel(cfg, profile),
		OnResult: func(result memory.HeartbeatResult) {
			logHeartbeatResult(profile.ID, result)
		},
	}
}

func heartbeatDistillModel(cfg *config.Root, profile config.AgentProfileConfig) memory.DistillModel {
	if cfg == nil {
		return nil
	}
	var names []string
	names = append(names, profile.Model.Roles.Models("memory_distill")...)
	names = append(names, cfg.Model.Roles.Models("memory_distill")...)
	if strings.TrimSpace(profile.Model.Default) != "" {
		names = append(names, profile.Model.Default)
	}
	names = append(names, profile.Model.Fallbacks...)
	if strings.TrimSpace(cfg.Model.Default) != "" {
		names = append(names, cfg.Model.Default)
	}
	names = append(names, cfg.Model.Fallbacks...)
	var configs []config.ModelConfig
	seen := map[string]bool{}
	for _, name := range names {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		for _, candidate := range cfg.Models {
			if candidate.Enabled && strings.EqualFold(candidate.Name, key) && strings.TrimSpace(candidate.ResolvedAPIKey()) != "" {
				configs = append(configs, candidate)
				break
			}
		}
	}
	if len(configs) == 0 {
		return nil
	}
	return model.NewFallbackAgentModel(configs)
}

func logHeartbeatResult(profileID string, result memory.HeartbeatResult) {
	if result.Files > 0 || result.Entries > 0 || len(result.Issues) > 0 {
		log.Printf("mateway heartbeat profile=%s lint_index files=%d entries=%d issues=%d", profileID, result.Files, result.Entries, len(result.Issues))
	}
	if result.Distill.Scanned > 0 || result.Distill.Created > 0 || result.Distill.Skipped > 0 || result.Distill.Duplicates > 0 || len(result.Distill.Errors) > 0 {
		log.Printf("mateway heartbeat profile=%s memory_distill scanned=%d created=%d skipped=%d duplicates=%d errors=%d", profileID, result.Distill.Scanned, result.Distill.Created, result.Distill.Skipped, result.Distill.Duplicates, len(result.Distill.Errors))
	}
	if result.Learning.Scanned > 0 || result.Learning.Created > 0 || result.Learning.Skipped > 0 || result.Learning.Duplicates > 0 || len(result.Learning.Errors) > 0 {
		log.Printf("mateway heartbeat profile=%s learning_distill scanned=%d created=%d skipped=%d duplicates=%d errors=%d", profileID, result.Learning.Scanned, result.Learning.Created, result.Learning.Skipped, result.Learning.Duplicates, len(result.Learning.Errors))
	}
	if result.Skill.Scanned > 0 || result.Skill.Created > 0 || result.Skill.Skipped > 0 || result.Skill.Duplicates > 0 || len(result.Skill.Errors) > 0 {
		log.Printf("mateway heartbeat profile=%s skill_learning scanned=%d created=%d skipped=%d duplicates=%d errors=%d", profileID, result.Skill.Scanned, result.Skill.Created, result.Skill.Skipped, result.Skill.Duplicates, len(result.Skill.Errors))
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
	_ = runtime.AppendTraceEvent(resp.TracePath, gatewayTraceEvent(msg, map[string]any{
		"type":                "gateway_done",
		"runtime_duration_ms": runtimeDuration.Milliseconds(),
		"reply_duration_ms":   int64(0),
		"total_duration_ms":   time.Since(start).Milliseconds(),
		"reply_style":         resp.Reply.Style,
		"follow_up_count":     len(resp.FollowUps),
		"failed":              resp.Failed,
	}))
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
		id, ackErr := sender.ReplyTextWithID(runCtx, msg, gatewayText(rt.Config, msg, "gateway.processing_ack", nil), msg.ID+":processing")
		if ackErr != nil {
			log.Printf("mateway gateway processing ack error message_id=%s session=%s: %v", msg.ID, msg.SessionKey, ackErr)
		}
		ackMessageID = id
	}
	runtimeStart := time.Now()
	progressRT := rt
	if strings.TrimSpace(ackMessageID) != "" {
		progressRT.ProgressSink = feishuProgressSink(runCtx, sender, ackMessageID)
	}
	resp, err := progressRT.Handle(runCtx, msg)
	runtimeDuration := time.Since(runtimeStart)
	if err != nil {
		log.Printf("mateway gateway runtime error message_id=%s session=%s: %v", msg.ID, msg.SessionKey, err)
		if !cardAction {
			react(runCtx, sender, msg.ID, "CROSS_MARK")
		}
		_ = sender.Reply(runCtx, msg, channel.OutboundMessage{Channel: msg.Channel, ThreadID: msg.ThreadID, Text: gatewayText(rt.Config, msg, "gateway.processing_failed", map[string]string{"error": err.Error()}), Style: "error"})
		return
	}
	replyStart := time.Now()
	if err := sendFinalReply(runCtx, sender, msg, ackMessageID, resp.Reply); err != nil {
		log.Printf("mateway gateway reply error message_id=%s session=%s: %v", msg.ID, msg.SessionKey, err)
		_ = runtime.AppendTraceEvent(resp.TracePath, gatewayTraceEvent(msg, map[string]any{
			"type":                "gateway_done",
			"runtime_duration_ms": runtimeDuration.Milliseconds(),
			"reply_duration_ms":   time.Since(replyStart).Milliseconds(),
			"total_duration_ms":   time.Since(start).Milliseconds(),
			"reply_error":         err.Error(),
		}))
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
	_ = runtime.AppendTraceEvent(resp.TracePath, gatewayTraceEvent(msg, map[string]any{
		"type":                "gateway_done",
		"runtime_duration_ms": runtimeDuration.Milliseconds(),
		"reply_duration_ms":   replyDuration.Milliseconds(),
		"total_duration_ms":   time.Since(start).Milliseconds(),
		"reply_style":         resp.Reply.Style,
		"failed":              resp.Failed,
	}))
}

func gatewayTraceEvent(msg channel.InboundMessage, payload map[string]any) map[string]any {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["session_key"] = msg.SessionKey
	payload["channel"] = msg.Channel
	payload["message_id"] = msg.ID
	payload["user_id"] = msg.UserID
	payload["thread_id"] = msg.ThreadID
	for _, key := range []string{"account_id", "peer_id", "message_type"} {
		if value := strings.TrimSpace(msg.Metadata[key]); value != "" {
			payload[key] = value
		}
	}
	return payload
}

func feishuProgressSink(ctx context.Context, sender *feishu.Sender, ackMessageID string) func(channel.OutboundMessage) {
	var lastUpdate time.Time
	return func(update channel.OutboundMessage) {
		if sender == nil || strings.TrimSpace(ackMessageID) == "" {
			return
		}
		now := time.Now()
		if !lastUpdate.IsZero() && now.Sub(lastUpdate) < 500*time.Millisecond {
			return
		}
		lastUpdate = now
		if text := feishuProgressText(update); text != "" {
			if err := sender.UpdateText(ctx, ackMessageID, text); err != nil {
				log.Printf("mateway gateway progress text update error message_id=%s: %v", ackMessageID, err)
			}
		}
	}
}

func feishuProgressText(update channel.OutboundMessage) string {
	var b strings.Builder
	text := strings.TrimSpace(update.Text)
	if text == "" {
		text = "Processing..."
	}
	b.WriteString(text)
	for _, step := range update.Progress {
		title := strings.TrimSpace(step.Tool)
		if title == "" {
			title = strings.TrimSpace(step.Title)
		}
		if title == "" {
			continue
		}
		status := strings.TrimSpace(step.Status)
		if status == "" {
			status = "recorded"
		}
		b.WriteString("\n- ")
		b.WriteString(title)
		b.WriteString(": ")
		b.WriteString(feishuProgressStatus(status))
		if step.TimedOut {
			b.WriteString(" / timed out")
		}
		if summary := strings.TrimSpace(step.Summary); summary != "" {
			b.WriteString(" / ")
			b.WriteString(compactProgressLineText(summary, 96))
		}
	}
	return b.String()
}

func feishuProgressStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "running":
		return "call"
	case "accepted", "completed":
		return "success"
	case "failed", "blocked", "suspect":
		return "failed"
	default:
		if strings.TrimSpace(status) != "" {
			return strings.TrimSpace(status)
		}
		return "recorded"
	}
}

func compactProgressLineText(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
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
	case "input_required", "clarify", "partial":
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

func gatewayText(cfg *config.Root, msg channel.InboundMessage, key string, values map[string]string) string {
	text := gatewayTexts[key]
	if text == "" {
		text = key
	}
	for key, value := range values {
		text = strings.ReplaceAll(text, "{"+key+"}", value)
	}
	return text
}

var gatewayTexts = map[string]string{
	"gateway.processing_ack":          "Processing...",
	"gateway.processing_failed":       "The request failed while processing: {error}",
	"gateway.media_download_failed":   "Failed to download message media: {error}",
}
