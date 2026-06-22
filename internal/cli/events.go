package cli

import (
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/runtime"
)

type NDJSONEventWriter struct {
	Out io.Writer

	mu       sync.Mutex
	lastLine string
}

func (w *NDJSONEventWriter) Progress(msg channel.OutboundMessage) {
	if w == nil || w.Out == nil || len(msg.Progress) == 0 {
		return
	}
	step := msg.Progress[len(msg.Progress)-1]
	event := eventFromProgressStep(step)
	if strings.TrimSpace(event.Type) == "" {
		return
	}
	w.write(event)
}

func (w *NDJSONEventWriter) Final(resp runtime.Response, sessionKey string) error {
	if w == nil || w.Out == nil {
		return nil
	}
	event := ProcessEvent{
		Type:       "final.completed",
		Text:       resp.Reply.Text,
		Style:      string(resp.Reply.Style),
		TraceID:    resp.TraceID,
		TracePath:  resp.TracePath,
		SessionKey: sessionKey,
		Failed:     resp.Failed,
		Time:       time.Now().Format(time.RFC3339Nano),
	}
	if resp.Failed {
		event.Type = "final.failed"
	}
	w.write(event)
	return nil
}

func (w *NDJSONEventWriter) write(event ProcessEvent) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	line := string(data)
	w.mu.Lock()
	defer w.mu.Unlock()
	if line == w.lastLine {
		return
	}
	w.lastLine = line
	_, _ = w.Out.Write(append(data, '\n'))
}

type ProcessEvent struct {
	Type       string `json:"type"`
	Title      string `json:"title,omitempty"`
	Status     string `json:"status,omitempty"`
	Tool       string `json:"tool,omitempty"`
	Args       string `json:"args,omitempty"`
	Summary    string `json:"summary,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	Text       string `json:"text,omitempty"`
	Style      string `json:"style,omitempty"`
	TraceID    string `json:"trace_id,omitempty"`
	TracePath  string `json:"trace_path,omitempty"`
	SessionKey string `json:"session_key,omitempty"`
	Failed     bool   `json:"failed,omitempty"`
	Time       string `json:"time"`
}

func eventFromProgressStep(step channel.ProgressStep) ProcessEvent {
	title := firstNonEmpty(strings.TrimSpace(step.Tool), strings.TrimSpace(step.Title))
	status := strings.TrimSpace(step.Status)
	eventType := processEventType(title, status, strings.TrimSpace(step.Tool) != "")
	event := ProcessEvent{
		Type:       eventType,
		Title:      title,
		Status:     status,
		Tool:       strings.TrimSpace(step.Tool),
		DurationMS: step.DurationMS,
		TimedOut:   step.TimedOut,
		Time:       time.Now().Format(time.RFC3339Nano),
	}
	summary := compactInline(step.Summary, 240)
	switch eventType {
	case "tool.started":
		event.Args = summary
	default:
		event.Summary = summary
	}
	return event
}

func processEventType(title, status string, isTool bool) string {
	switch {
	case title == "model":
		return "model.thinking"
	case !isTool:
		switch status {
		case "accepted", "completed", "success", "done":
			return "runtime.completed"
		case "blocked", "failed", "error":
			return "runtime.blocked"
		default:
			return "runtime.progress"
		}
	case status == "running":
		return "tool.started"
	case status == "accepted" || status == "completed":
		return "tool.completed"
	case status == "blocked" || status == "failed":
		return "tool.blocked"
	default:
		return "progress.updated"
	}
}
