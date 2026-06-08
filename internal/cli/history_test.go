package cli

import (
	"testing"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/session"
)

func TestParseHistoryTarget(t *testing.T) {
	target, err := parseHistoryTarget("feishu:ops:oc_123")
	if err != nil {
		t.Fatal(err)
	}
	if target.Channel != "feishu" || target.Account != "ops" || target.ID != "oc_123" {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func TestParseHistoryTargetRejectsMissingID(t *testing.T) {
	if _, err := parseHistoryTarget("weixin:"); err == nil {
		t.Fatal("expected missing id error")
	}
}

func TestParseSince(t *testing.T) {
	duration, err := ParseSince("48h")
	if err != nil {
		t.Fatal(err)
	}
	if duration != 48*time.Hour {
		t.Fatalf("duration = %s", duration)
	}
	duration, err = ParseSince("7")
	if err != nil {
		t.Fatal(err)
	}
	if duration != 7*24*time.Hour {
		t.Fatalf("duration = %s", duration)
	}
}

func TestImportHistoryMessagesDeduplicates(t *testing.T) {
	cfg := &config.Root{App: config.AppConfig{Home: t.TempDir()}}
	messages := []agentcore.Message{
		{Role: agentcore.RoleUser, Content: "[feishu user] hello"},
		{Role: agentcore.RoleUser, Content: "[feishu user] hello"},
	}
	result, err := importHistoryMessages(cfg, "cli:history", messages)
	if err != nil {
		t.Fatal(err)
	}
	if result.Imported != 1 || result.Skipped != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	state, err := session.NewStore(cfg.App.Home).Load("cli:history")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Messages) != 1 || state.Messages[0].Content != "[feishu user] hello" {
		t.Fatalf("unexpected state: %#v", state.Messages)
	}
}
