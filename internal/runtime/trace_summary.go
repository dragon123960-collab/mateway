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
	ModelRequests     int
	InputTokens       int
	OutputTokens      int
	TotalTokens       int
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
			input := int(number(event["input_tokens"]))
			output := int(number(event["output_tokens"]))
			total := int(number(event["total_tokens"]))
			if usage, ok := event["usage"].(map[string]any); ok {
				input = int(number(usage["InputTokens"]))
				output = int(number(usage["OutputTokens"]))
				total = int(number(usage["TotalTokens"]))
				if input == 0 {
					input = int(number(usage["input_tokens"]))
				}
				if output == 0 {
					output = int(number(usage["output_tokens"]))
				}
				if total == 0 {
					total = int(number(usage["total_tokens"]))
				}
			}
			if input > 0 || output > 0 || total > 0 {
				out.ModelRequests++
				out.InputTokens += input
				out.OutputTokens += output
				if total == 0 {
					total = input + output
				}
				out.TotalTokens += total
			}
		case "model_usage":
			if out.ModelRequests == 0 {
				out.ModelRequests += int(number(event["requests"]))
				out.InputTokens += int(number(event["input_tokens"]))
				out.OutputTokens += int(number(event["output_tokens"]))
				out.TotalTokens += int(number(event["total_tokens"]))
			}
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
