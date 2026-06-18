package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/runtime"
)

func TestNDJSONEventWriterProgressAndFinal(t *testing.T) {
	var out bytes.Buffer
	writer := &NDJSONEventWriter{Out: &out}
	writer.Progress(channel.OutboundMessage{Progress: []channel.ProgressStep{{
		Title:   "model",
		Status:  "thinking",
		Summary: "waiting for model output",
	}}})
	if err := writer.Final(runtime.Response{Reply: channel.OutboundMessage{Text: "done", Style: "ok"}, TraceID: "trace-1", TracePath: "/tmp/trace.jsonl"}, "cli:test"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two events, got %q", out.String())
	}
	var first ProcessEvent
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.Type != "model.thinking" || first.Summary != "waiting for model output" {
		t.Fatalf("unexpected first event: %#v", first)
	}
	var final ProcessEvent
	if err := json.Unmarshal([]byte(lines[1]), &final); err != nil {
		t.Fatal(err)
	}
	if final.Type != "final.completed" || final.Text != "done" || final.SessionKey != "cli:test" {
		t.Fatalf("unexpected final event: %#v", final)
	}
}

func TestNDJSONEventWriterSeparatesToolArgsAndResultSummary(t *testing.T) {
	var out bytes.Buffer
	writer := &NDJSONEventWriter{Out: &out}
	writer.Progress(channel.OutboundMessage{Progress: []channel.ProgressStep{{
		Tool:    "terminal.run",
		Status:  "running",
		Summary: "go test ./...",
	}}})
	writer.Progress(channel.OutboundMessage{Progress: []channel.ProgressStep{{
		Tool:       "terminal.run",
		Status:     "accepted",
		Summary:    "ok",
		DurationMS: 42,
	}}})
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two events, got %q", out.String())
	}
	var started ProcessEvent
	if err := json.Unmarshal([]byte(lines[0]), &started); err != nil {
		t.Fatal(err)
	}
	if started.Type != "tool.started" || started.Args != "go test ./..." || started.Summary != "" {
		t.Fatalf("unexpected started event: %#v", started)
	}
	var completed ProcessEvent
	if err := json.Unmarshal([]byte(lines[1]), &completed); err != nil {
		t.Fatal(err)
	}
	if completed.Type != "tool.completed" || completed.Summary != "ok" || completed.Args != "" || completed.DurationMS != 42 {
		t.Fatalf("unexpected completed event: %#v", completed)
	}
}

func TestNDJSONEventWriterKeepsRuntimeProgressSeparateFromTools(t *testing.T) {
	var out bytes.Buffer
	writer := &NDJSONEventWriter{Out: &out}
	writer.Progress(channel.OutboundMessage{Progress: []channel.ProgressStep{{
		Title:   "Plan",
		Status:  "running",
		Summary: "preparing task graph",
	}}})
	writer.Progress(channel.OutboundMessage{Progress: []channel.ProgressStep{{
		Title:   "Plan",
		Status:  "completed",
		Summary: "task graph ready",
	}}})
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two events, got %q", out.String())
	}
	var running ProcessEvent
	if err := json.Unmarshal([]byte(lines[0]), &running); err != nil {
		t.Fatal(err)
	}
	if running.Type != "runtime.progress" || running.Title != "Plan" || running.Tool != "" {
		t.Fatalf("unexpected runtime progress event: %#v", running)
	}
	var completed ProcessEvent
	if err := json.Unmarshal([]byte(lines[1]), &completed); err != nil {
		t.Fatal(err)
	}
	if completed.Type != "runtime.completed" || completed.Title != "Plan" || completed.Tool != "" {
		t.Fatalf("unexpected runtime completed event: %#v", completed)
	}
}

func TestRunAskRejectsConflictingOutputModes(t *testing.T) {
	err := RunAsk(t.Context(), AskOptions{Quiet: true, JSON: true, Message: "hello"})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutually exclusive error, got %v", err)
	}
}
