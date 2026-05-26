package schedule

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dongping/mateway/internal/channel"
)

type fakeHandler struct {
	calls    int
	lastText string
}

type fakePolicyHandler struct {
	calls       int
	lastAllowed []string
	resp        Response
}

func TestRunnerRunDueUsesRuntimeAndWritesOutput(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	task, _, err := store.Create(CreateInput{ID: "ai-trends", Title: "AI Trends", Prompt: "Collect AI trends.", DailyAt: "09:00", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	handler := &fakeHandler{}
	results, err := Runner{Store: store, Handle: handler.Handle}.RunDue(context.Background(), now)
	if err != nil {
		t.Fatalf("run due: %v", err)
	}
	if len(results) != 1 || results[0].Task.ID != task.ID || results[0].Failed {
		t.Fatalf("unexpected results %#v", results)
	}
	if handler.calls != 1 {
		t.Fatalf("expected one handler call, got %d", handler.calls)
	}
	if !strings.Contains(handler.lastText, "这是一次已经触发的定时执行") || !strings.Contains(handler.lastText, task.Prompt) {
		t.Fatalf("expected handler call, calls=%d text=%q", handler.calls, handler.lastText)
	}
	data, err := os.ReadFile(results[0].OutputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(data), "scheduled result") {
		t.Fatalf("unexpected output:\n%s", string(data))
	}
	state, err := store.ReadState()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if len(state.Tasks) != 1 || state.Tasks[0].Status != "ok" || state.Tasks[0].Output == "" {
		t.Fatalf("unexpected state %#v", state)
	}
	accept := AcceptRunResult(task, results[0])
	if accept.Status != "pass" {
		t.Fatalf("expected accepted scheduled run, got %#v", accept)
	}
	if results[0].DeliveryAcceptStatus != "pass" {
		t.Fatalf("expected stored delivery acceptance, got %#v", results[0])
	}
}

func (f *fakeHandler) Handle(ctx context.Context, msg channel.InboundMessage) (Response, error) {
	f.calls++
	f.lastText = msg.Text
	return Response{
		Reply:   channel.OutboundMessage{Text: "scheduled result"},
		TraceID: "trace-1",
	}, nil
}

func (f *fakePolicyHandler) Handle(ctx context.Context, msg channel.InboundMessage) (Response, error) {
	f.calls++
	return f.resp, nil
}

func (f *fakePolicyHandler) WithSchedulePolicy(task Task) Handler {
	return func(ctx context.Context, msg channel.InboundMessage) (Response, error) {
		f.calls++
		f.lastAllowed = append([]string(nil), task.AllowedTools...)
		return f.resp, nil
	}
}

func TestRunnerAwaitConfirmIsBlockedAndDoesNotWriteOutput(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	task, _, err := store.Create(CreateInput{ID: "guarded", Title: "Guarded", Prompt: "Write file", DailyAt: "09:00", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler(func(ctx context.Context, msg channel.InboundMessage) (Response, error) {
		return Response{
			Reply:        channel.OutboundMessage{Text: "等待确认"},
			TraceID:      "trace-confirm",
			AwaitConfirm: true,
		}, nil
	})
	result := Runner{Store: store, Handle: handler}.RunTask(context.Background(), task, now)
	if !result.Failed || !result.AwaitConfirm || result.OutputPath != "" || result.DeliveryAcceptStatus != "hard_fail" {
		t.Fatalf("expected blocked confirmation without output, got %#v", result)
	}
	state, err := store.ReadState()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if len(state.Tasks) != 1 || state.Tasks[0].Status != "blocked" || state.Tasks[0].Output != "" {
		t.Fatalf("unexpected state %#v", state)
	}
}

func TestRunnerUsesSchedulePolicyHandler(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	task, _, err := store.Create(CreateInput{
		ID:           "allowed",
		Title:        "Allowed",
		Prompt:       "Read time",
		DailyAt:      "09:00",
		AllowedTools: []string{"time.now"},
		Now:          now,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := &fakePolicyHandler{resp: Response{Reply: channel.OutboundMessage{Text: "ok"}, TraceID: "trace-policy"}}
	result := Runner{Store: store, Handle: policy.Handle, PolicyHandler: policy}.RunTask(context.Background(), task, now)
	if result.Failed {
		t.Fatalf("expected policy run to pass, got %#v", result)
	}
	if policy.calls != 1 || len(policy.lastAllowed) != 1 || policy.lastAllowed[0] != "time.now" {
		t.Fatalf("expected schedule policy handler to receive allowed tools, calls=%d allowed=%#v", policy.calls, policy.lastAllowed)
	}
}

func TestRunnerLimitsOutputChars(t *testing.T) {
	home := t.TempDir()
	store := NewStore(home)
	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	task, _, err := store.Create(CreateInput{ID: "limited", Title: "Limited", Prompt: "Write long", DailyAt: "09:00", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	task.Limits.MaxOutputChars = 5
	handler := Handler(func(ctx context.Context, msg channel.InboundMessage) (Response, error) {
		return Response{Reply: channel.OutboundMessage{Text: "1234567890"}, TraceID: "trace-limit"}, nil
	})
	result := Runner{Store: store, Handle: handler}.RunTask(context.Background(), task, now)
	if result.Failed {
		t.Fatalf("expected limited run to pass, got %#v", result)
	}
	data, err := os.ReadFile(result.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "123456") || !strings.Contains(string(data), "output truncated to 5 chars") {
		t.Fatalf("expected truncated output, got %q", string(data))
	}
}

func TestAcceptRunResultArtifactMissingPathIsHardFail(t *testing.T) {
	task := Task{
		ID:    "ai-trends",
		Title: "AI Trends",
		Delivery: DeliverySpec{
			Mode: "artifact",
		},
	}
	accept := AcceptRunResult(task, RunResult{Task: task, TraceID: "trace-1"})
	if accept.Status != "hard_fail" || !strings.Contains(accept.Reason, "did not produce an output artifact path") {
		t.Fatalf("expected missing artifact path hard fail, got %#v", accept)
	}
}

func TestAcceptRunResultArtifactWithoutTraceIsUsable(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "scheduled.md")
	if err := os.WriteFile(path, []byte("# result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	task := Task{
		ID:    "ai-trends",
		Title: "AI Trends",
		Delivery: DeliverySpec{
			Mode: "artifact",
		},
	}
	accept := AcceptRunResult(task, RunResult{Task: task, OutputPath: path})
	if accept.Status != "usable" || !strings.Contains(accept.Reason, "trace id is missing") {
		t.Fatalf("expected artifact without trace to be usable, got %#v", accept)
	}
}
