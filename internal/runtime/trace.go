package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
)

type traceRecorder struct {
	id      string
	path    string
	base    map[string]any
	onWrite func()
	mu      sync.Mutex
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

func (r *traceRecorder) setIdentity(values map[string]any) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.base == nil {
		r.base = map[string]any{}
	}
	for key, value := range values {
		if key == "" || value == nil {
			continue
		}
		if text, ok := value.(string); ok && text == "" {
			continue
		}
		r.base[key] = value
	}
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
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.onWrite != nil {
		r.onWrite()
	}
	if len(r.base) > 0 {
		merged := make(map[string]any, len(r.base)+len(payload))
		for key, value := range r.base {
			merged[key] = value
		}
		for key, value := range payload {
			merged[key] = value
		}
		payload = merged
	}
	payload = redactPayload(payload)
	payload["trace_id"] = r.id
	payload["time"] = time.Now().Format(time.RFC3339Nano)
	return appendTracePayload(r.path, payload)
}

func AppendTraceEvent(path string, payload map[string]any) error {
	if path == "" {
		return nil
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload = redactPayload(payload)
	payload["time"] = time.Now().Format(time.RFC3339Nano)
	if _, ok := payload["trace_id"]; !ok {
		if id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)); id != "" {
			payload["trace_id"] = id
		}
	}
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
