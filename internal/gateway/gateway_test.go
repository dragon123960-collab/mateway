package gateway

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/session"
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

func TestShouldIgnoreFeishuImageMessages(t *testing.T) {
	msg := channel.InboundMessage{
		Channel:  "feishu",
		Metadata: map[string]string{"message_type": "image", "chat_type": "p2p"},
		Parts:    []channel.MessagePart{{Type: channel.PartImage, Metadata: map[string]string{"image_key": "img_1"}}},
	}
	if shouldIgnoreInbound(config.FeishuConfig{}, msg) {
		t.Fatal("expected image message accepted")
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
		"input_required": "EYES",
		"partial":        "EYES",
		"clarify":        "EYES",
		"error":          "CROSS_MARK",
		"cancelled":      "CROSS_MARK",
		"completed":      "DONE",
		"processing":     "DONE",
		"":               "DONE",
	}
	for style, want := range cases {
		if got := reactionForReply(channel.OutboundMessage{Style: style}); got != want {
			t.Fatalf("style %q reaction = %q want %q", style, got, want)
		}
	}
}

func TestShouldSendProcessingAckSkipsNewSessionCommand(t *testing.T) {
	rt := runtime.New(&config.Root{App: config.AppConfig{Home: t.TempDir()}})
	if shouldSendProcessingAck(rt, channel.InboundMessage{Text: "/new"}) {
		t.Fatal("expected /new to skip processing ack")
	}
	if shouldSendProcessingAck(rt, channel.InboundMessage{Text: "/read README.md"}) {
		t.Fatal("expected slash command to skip processing ack")
	}
	if !shouldSendProcessingAck(rt, channel.InboundMessage{SessionKey: "cli:test", Text: "hello"}) {
		t.Fatal("expected normal message to send processing ack")
	}
	if shouldSendProcessingAck(rt, channel.InboundMessage{Text: "hello", Metadata: map[string]string{"message_type": "interactive", "card_action": "confirm"}}) {
		t.Fatal("expected card action to skip processing ack")
	}
}

func TestFeishuProgressTextIncludesSteps(t *testing.T) {
	text := feishuProgressText(channel.OutboundMessage{
		Text: "Processing...",
		Progress: []channel.ProgressStep{
			{Tool: "web.search", Status: "running", Summary: "北京天气"},
		},
	})
	if !strings.Contains(text, "web.search: call") || !strings.Contains(text, "北京天气") {
		t.Fatalf("unexpected progress text %q", text)
	}
}

func TestFeishuProgressTextShowsToolResultOutcome(t *testing.T) {
	text := feishuProgressText(channel.OutboundMessage{
		Text: "Processing...",
		Progress: []channel.ProgressStep{
			{Tool: "terminal.run", Status: "accepted", Summary: "tests passed"},
			{Tool: "file.write", Status: "failed", Summary: "permission denied"},
		},
	})
	for _, want := range []string{"terminal.run: success / tests passed", "file.write: failed / permission denied"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %q", want, text)
		}
	}
}

func TestFeishuProgressTextCompactsLongSummary(t *testing.T) {
	text := feishuProgressText(channel.OutboundMessage{
		Text: "Processing...",
		Progress: []channel.ProgressStep{
			{Title: "model", Status: "thinking", Summary: strings.Repeat("long ", 80)},
		},
	})
	if strings.Contains(text, strings.Repeat("long ", 20)) || !strings.Contains(text, "...") {
		t.Fatalf("expected compact progress text, got %q", text)
	}
}

func TestShouldSendProcessingAckSkipsPendingSession(t *testing.T) {
	rt := runtime.New(&config.Root{App: config.AppConfig{Home: t.TempDir()}})
	state, err := rt.Store.Load("cli:test")
	if err != nil {
		t.Fatal(err)
	}
	task := state.StartTask("review memory proposal")
	state.Pending = &session.PendingAction{Kind: "memory_proposal_review", TaskID: task.ID, ProposalID: "prop_test"}
	if err := rt.Store.Save(state); err != nil {
		t.Fatal(err)
	}
	if shouldSendProcessingAck(rt, channel.InboundMessage{SessionKey: "cli:test", Text: "1"}) {
		t.Fatal("expected pending session to skip processing ack")
	}
}

func TestEnabledHeartbeatStartersUseProfileConfig(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{
		App: config.AppConfig{Home: home},
		Agents: config.AgentsConfig{
			Default: "main",
			Profiles: []config.AgentProfileConfig{{
				ID: "main",
				Heartbeat: config.HeartbeatConfig{
					Enabled:  true,
					Interval: "7m",
					Jobs:     []string{"memory_distill", "learning_distill"},
				},
			}},
		},
	}
	input := heartbeatInputForProfile(cfg, cfg.Agents.Profiles[0])
	if input.Home != home {
		t.Fatalf("home = %q want %q", input.Home, home)
	}
	if input.Workspace != filepath.Join(home, "workspace") {
		t.Fatalf("workspace = %q", input.Workspace)
	}
	if input.MemoryRoot != filepath.Join(home, "workspace", "memory") {
		t.Fatalf("memory root = %q", input.MemoryRoot)
	}
	if input.IndexPath != filepath.Join(home, "indexes", "memory_index.json") {
		t.Fatalf("index path = %q", input.IndexPath)
	}
	if input.Interval != 7*time.Minute {
		t.Fatalf("interval = %v want 7m", input.Interval)
	}
	if strings.Join(input.Jobs, ",") != "memory_distill,learning_distill" {
		t.Fatalf("jobs = %#v", input.Jobs)
	}
	if len(enabledHeartbeatStarters(context.Background(), cfg)) != 1 {
		t.Fatal("expected one heartbeat starter")
	}
}

func TestEnabledHeartbeatStartersDeduplicateEquivalentProfiles(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Root{
		App: config.AppConfig{Home: home},
		Agents: config.AgentsConfig{
			Default: "main",
			Profiles: []config.AgentProfileConfig{
				{
					ID: "main",
					Heartbeat: config.HeartbeatConfig{
						Enabled:  true,
						Interval: "30m",
						Jobs:     []string{"memory_distill", "learning_distill", "skill_learning", "memory_lint"},
					},
				},
				{
					ID: "local",
					Heartbeat: config.HeartbeatConfig{
						Enabled:  true,
						Interval: "30m",
						Jobs:     []string{"memory_lint", "memory_distill", "learning_distill", "skill_learning"},
					},
				},
			},
		},
	}
	if len(enabledHeartbeatStarters(context.Background(), cfg)) != 1 {
		t.Fatal("expected equivalent heartbeat configs to share one starter")
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

func TestBuiltinChannelSpecsExposeFeishuAccounts(t *testing.T) {
	cfg := Config{Config: &config.Root{}}
	cfg.Config.Channels.Feishu = config.FeishuConfig{
		Enabled:        true,
		AppIDEnv:       "BASE_APP_ID",
		AppSecretEnv:   "BASE_SECRET",
		WebSocket:      config.FeishuWebSocketConfig{Enabled: true},
		DefaultAccount: "main",
		Accounts: []config.FeishuAccountConfig{
			{ID: "ops", AppIDEnv: "OPS_APP_ID"},
			{ID: "local", AppIDEnv: "LOCAL_APP_ID"},
		},
	}
	specs := builtinChannelSpecs(cfg)
	var names []string
	for _, spec := range specs {
		if spec.Enabled {
			names = append(names, spec.Name)
		}
	}
	got := strings.Join(names, ",")
	if got != "feishu:ops,feishu:local" {
		t.Fatalf("enabled specs = %q", got)
	}
}
