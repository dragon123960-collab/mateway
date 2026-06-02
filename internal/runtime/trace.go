package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/observe"
)

type traceRecorder struct {
	id         string
	path       string
	sessionKey string
	taskID     string
}

func newTraceRecorder(cfg *config.Root) (*traceRecorder, error) {
	home := config.DefaultHome()
	if cfg != nil && cfg.App.Home != "" {
		home = cfg.App.Home
	}
	dir := filepath.Join(home, "trace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	id := time.Now().Format("20060102-150405.000000")
	return &traceRecorder{id: id, path: filepath.Join(dir, id+".jsonl")}, nil
}

func (r *traceRecorder) emit(ctx context.Context, event agentcore.Event) error {
	if r == nil {
		return nil
	}
	payload := map[string]any{
		"type":        event.Type,
		"iteration":   event.Iteration,
		"duration_ms": event.Duration.Milliseconds(),
		"message":     event.Message,
		"tool_call":   event.ToolCall,
		"tool_result": event.ToolResult,
	}
	if event.Type == agentcore.EventMessageStart && event.Message.Usage != nil {
		payload["usage"] = event.Message.Usage
		payload["model"] = event.Message.Usage.Model
		payload["provider"] = event.Message.Usage.Provider
	}
	return r.write(payload)
}

func (r *traceRecorder) write(payload map[string]any) error {
	if r == nil {
		return nil
	}
	payload = redactPayload(payload)
	payload["trace_id"] = r.id
	payload["time"] = time.Now().Format(time.RFC3339Nano)
	if r.sessionKey != "" && stringValue(payload["session_key"]) == "" {
		payload["session_key"] = r.sessionKey
	}
	if r.taskID != "" && stringValue(payload["task_id"]) == "" {
		payload["task_id"] = r.taskID
	}
	observe.Publish(traceEventFromPayload(payload))
	return appendTracePayload(r.path, payload)
}

func (r *traceRecorder) setSessionKey(sessionKey string) {
	if r != nil && strings.TrimSpace(sessionKey) != "" {
		r.sessionKey = strings.TrimSpace(sessionKey)
	}
}

func (r *traceRecorder) setTaskID(taskID string) {
	if r != nil && strings.TrimSpace(taskID) != "" {
		r.taskID = strings.TrimSpace(taskID)
	}
}

func AppendTraceEvent(path string, payload map[string]any) error {
	if path == "" {
		return nil
	}
	payload = redactPayload(payload)
	payload["time"] = time.Now().Format(time.RFC3339Nano)
	return appendTracePayload(path, payload)
}

func appendTracePayload(path string, payload map[string]any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(data, '\n'))
	return err
}

func traceEventFromPayload(payload map[string]any) observe.Event {
	eventType := strings.TrimSpace(stringValue(payload["type"]))
	return observe.Event{
		Type:       realtimeEventType(eventType),
		Time:       stringValue(payload["time"]),
		TraceID:    stringValue(payload["trace_id"]),
		SessionKey: stringValue(payload["session_key"]),
		TaskID:     firstNonEmptyTraceValue(payload["task_id"], payload["TaskID"]),
		Payload:    payload,
	}
}

func realtimeEventType(traceType string) string {
	switch traceType {
	case "request":
		return "runtime_started"
	case "agent_start":
		return "task_created"
	case "turn_start":
		return "model_started"
	case "message_start":
		return "model_finished"
	case "tool_call_start":
		return "tool_started"
	case "tool_call_end", "tool_execution_start", "tool_execution_end":
		if traceType == "tool_execution_start" {
			return "tool_started"
		}
		return "tool_finished"
	case "model_usage":
		return "usage_delta"
	case "reply", "follow_up_reply":
		return "reply"
	case "runtime_done":
		return "runtime_done"
	case "model_error", "context_budget_exceeded", "hook_warning":
		return "error"
	case "schedule_review_pending", "pending_user_input", "pending_confirmed":
		return "task_blocked"
	default:
		return traceType
	}
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func firstNonEmptyTraceValue(values ...any) string {
	for _, value := range values {
		if text := strings.TrimSpace(stringValue(value)); text != "" {
			return text
		}
	}
	return ""
}
