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
	TraceID           string
	SessionKey        string
	Channel           string
	AccountID         string
	AgentID           string
	TaskID            string
	MessageID         string
	UserID            string
	ThreadID          string
	Events            int
	ModelDurationMS   int64
	ToolDurationMS    int64
	RuntimeDurationMS int64
	ReplyDurationMS   int64
	TotalDurationMS   int64
	RuntimeDone       bool
	GatewayDone       bool
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
		captureTraceIdentity(&out, event)
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
			out.RuntimeDone = true
		case "gateway_done":
			out.RuntimeDurationMS = int64(number(event["runtime_duration_ms"]))
			out.ReplyDurationMS = int64(number(event["reply_duration_ms"]))
			out.TotalDurationMS = int64(number(event["total_duration_ms"]))
			out.GatewayDone = true
		}
	}
	if err := scanner.Err(); err != nil {
		return TraceSummary{}, err
	}
	return out, nil
}

func captureTraceIdentity(out *TraceSummary, event map[string]any) {
	if out.TraceID == "" {
		out.TraceID = traceString(event["trace_id"])
	}
	if out.SessionKey == "" {
		out.SessionKey = traceString(event["session_key"])
	}
	if out.Channel == "" {
		out.Channel = traceString(event["channel"])
	}
	if out.AccountID == "" {
		out.AccountID = traceString(event["account_id"])
	}
	if out.AgentID == "" {
		out.AgentID = traceString(event["agent_id"])
	}
	if out.TaskID == "" {
		out.TaskID = traceString(event["task_id"])
	}
	if out.MessageID == "" {
		out.MessageID = traceString(event["message_id"])
	}
	if out.UserID == "" {
		out.UserID = traceString(event["user_id"])
	}
	if out.ThreadID == "" {
		out.ThreadID = traceString(event["thread_id"])
	}
}

func traceString(value any) string {
	if value == nil {
		return ""
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "<nil>" {
		return ""
	}
	return text
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
