package runtime

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type TraceSummary struct {
	Path              string
	Events            int
	ModelDurationMS   int64
	ToolDurationMS    int64
	RuntimeDurationMS int64
	ReplyDurationMS   int64
	TotalDurationMS   int64
	ToolCalls         []string
}

func SummarizeTrace(path string) (TraceSummary, error) {
	file, err := os.Open(path)
	if err != nil {
		return TraceSummary{}, err
	}
	defer file.Close()
	out := TraceSummary{Path: path}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		out.Events++
		eventType := fmt.Sprint(event["type"])
		duration := int64(number(event["duration_ms"]))
		switch eventType {
		case "message_start":
			out.ModelDurationMS += duration
		case "tool_execution_end":
			out.ToolDurationMS += duration
			if call, ok := event["tool_call"].(map[string]any); ok {
				name := strings.TrimSpace(fmt.Sprint(call["Name"]))
				if name != "" {
					out.ToolCalls = append(out.ToolCalls, name)
				}
			}
		case "runtime_done":
			out.RuntimeDurationMS = duration
		case "gateway_done":
			out.RuntimeDurationMS = int64(number(event["runtime_duration_ms"]))
			out.ReplyDurationMS = int64(number(event["reply_duration_ms"]))
			out.TotalDurationMS = int64(number(event["total_duration_ms"]))
		}
	}
	if err := scanner.Err(); err != nil {
		return TraceSummary{}, err
	}
	return out, nil
}

func number(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case int:
		return float64(v)
	default:
		return 0
	}
}
