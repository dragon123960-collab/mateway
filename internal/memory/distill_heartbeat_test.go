package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
)

type distillStaticModel struct {
	text string
	err  error
}

func (m distillStaticModel) Next(context.Context, agentcore.Context) (agentcore.Message, error) {
	if m.err != nil {
		return agentcore.Message{}, m.err
	}
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: m.text}, nil
}

func TestRunDistillHeartbeatCreatesProposal(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "observe", "diary", "one.md"), `---
type: diary
---
# Task diary

- Goal: 记住 README 检查流程

Steps:
- file.read accepted: read README
`)
	model := distillStaticModel{text: `{"title":"README 检查流程","type":"experience","scope":"agent","body":"Use file.read when checking README workflows.","sources":["observe/diary/one.md"],"confidence":"medium"}`}
	result, err := RunDistillHeartbeat(context.Background(), DistillHeartbeatInput{Home: home, MemoryRoot: filepath.Join(home, "workspace", "memory"), Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Created != 1 || len(result.ProposalIDs) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	proposals, err := (ProposalStore{Home: home}).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(proposals) != 1 || proposals[0].Status != "proposed" || proposals[0].Title != "README 检查流程" {
		t.Fatalf("unexpected proposals: %#v", proposals)
	}
	audit, err := os.ReadFile(filepath.Join(home, "observe", "audit", "memory.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "memory_distill_proposal_created") || !strings.Contains(string(audit), "memory_distill_done") {
		t.Fatalf("unexpected audit:\n%s", audit)
	}
}

func TestRunDistillHeartbeatSkipsLowValueAndMarksProcessed(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "observe", "diary", "one.md"), "# Task diary\n\n- Goal: hello\n")
	result, err := RunDistillHeartbeat(context.Background(), DistillHeartbeatInput{Home: home, Model: distillStaticModel{text: `{}`}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Created != 0 || result.Skipped != 1 {
		t.Fatalf("unexpected first result: %#v", result)
	}
	result, err = RunDistillHeartbeat(context.Background(), DistillHeartbeatInput{Home: home, Model: distillStaticModel{text: `{}`}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 0 {
		t.Fatalf("expected processed source skipped, got %#v", result)
	}
}

func TestRunDistillHeartbeatSkipsDuplicateProposal(t *testing.T) {
	home := t.TempDir()
	store := ProposalStore{Home: home, MemoryRoot: filepath.Join(home, "workspace", "memory")}
	if _, err := store.Create(CreateProposalInput{Title: "README 检查流程", Type: "experience", Scope: "agent", Body: "Existing.", Sources: []string{"observe/diary/one.md"}, Confidence: "medium"}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, "observe", "diary", "one.md"), "# Task diary\n\n- Goal: 记住 README 检查流程\n")
	model := distillStaticModel{text: `{"title":"README 检查流程","type":"experience","scope":"agent","body":"Use file.read when checking README workflows.","sources":["observe/diary/one.md"],"confidence":"medium"}`}
	result, err := RunDistillHeartbeat(context.Background(), DistillHeartbeatInput{Home: home, MemoryRoot: filepath.Join(home, "workspace", "memory"), Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 0 || result.Duplicates != 1 {
		t.Fatalf("expected duplicate skip, got %#v", result)
	}
}

func TestRunDistillHeartbeatNoModelSkipsCandidates(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "observe", "diary", "one.md"), "# Task diary\n\n- Goal: 记住 README 检查流程\n")
	result, err := RunDistillHeartbeat(context.Background(), DistillHeartbeatInput{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 0 || result.Skipped != 1 {
		t.Fatalf("expected no-model skip, got %#v", result)
	}
	audit, err := os.ReadFile(filepath.Join(home, "observe", "audit", "memory.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "memory_distill_model_error") {
		t.Fatalf("expected model error audit:\n%s", audit)
	}
	model := distillStaticModel{text: `{"title":"README 检查流程","type":"experience","scope":"agent","body":"Use file.read when checking README workflows.","sources":["observe/diary/one.md"],"confidence":"medium"}`}
	result, err = RunDistillHeartbeat(context.Background(), DistillHeartbeatInput{Home: home, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 1 || result.Created != 1 {
		t.Fatalf("expected source retried after model configured, got %#v", result)
	}
}

func TestPendingProposalNudgeOncePerDay(t *testing.T) {
	home := t.TempDir()
	if _, err := (ProposalStore{Home: home}).Create(CreateProposalInput{Title: "One", Type: "experience", Scope: "agent", Body: "Body", Sources: []string{"trace:one"}, Confidence: "low"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	text, err := PendingProposalNudge(home, "cli:test", now, ProposalNudgeOptions{Channel: "cli", Channels: []string{"cli"}, Interval: 24 * time.Hour, MaxProposals: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Pending memory proposals: 1") || !strings.Contains(text, "mateway memory proposal list") || !strings.Contains(text, "mateway memory proposal show") {
		t.Fatalf("unexpected nudge: %q", text)
	}
	if strings.Contains(text, "Body") {
		t.Fatalf("nudge should stay compact and avoid proposal body: %q", text)
	}
	text, err = PendingProposalNudge(home, "cli:test", now.Add(time.Hour), ProposalNudgeOptions{Channel: "cli", Channels: []string{"cli"}, Interval: 24 * time.Hour, MaxProposals: 3})
	if err != nil {
		t.Fatal(err)
	}
	if text != "" {
		t.Fatalf("expected same-day nudge suppressed, got %q", text)
	}
	text, err = PendingProposalNudge(home, "cli:test", now.Add(24*time.Hour), ProposalNudgeOptions{Channel: "cli", Channels: []string{"cli"}, Interval: 24 * time.Hour, MaxProposals: 3})
	if err != nil {
		t.Fatal(err)
	}
	if text == "" {
		t.Fatal("expected next-day nudge")
	}
	text, err = PendingProposalNudge(home, "weixin:test", now.Add(48*time.Hour), ProposalNudgeOptions{Channel: "weixin", Channels: []string{"cli"}, Interval: 24 * time.Hour, MaxProposals: 3})
	if err != nil {
		t.Fatal(err)
	}
	if text != "" {
		t.Fatalf("expected disallowed channel suppressed, got %q", text)
	}
}
