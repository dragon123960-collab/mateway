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
