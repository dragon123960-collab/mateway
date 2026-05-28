package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
)

type traceRecorder struct {
	id   string
	path string
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
	return r.write(map[string]any{
		"type":        event.Type,
		"iteration":   event.Iteration,
		"duration_ms": event.Duration.Milliseconds(),
		"message":     event.Message,
		"tool_call":   event.ToolCall,
		"tool_result": event.ToolResult,
	})
}

func (r *traceRecorder) write(payload map[string]any) error {
	if r == nil {
		return nil
	}
	payload["trace_id"] = r.id
	payload["time"] = time.Now().Format(time.RFC3339Nano)
	return appendTracePayload(r.path, payload)
}

func AppendTraceEvent(path string, payload map[string]any) error {
	if path == "" {
		return nil
	}
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
