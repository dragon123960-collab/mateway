package runtime

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

type TraceSummary struct {
	Path                 string
	TraceID              string
	SessionKey           string
	Channel              string
	AccountID            string
	AgentID              string
	TaskID               string
	MessageID            string
	UserID               string
	ThreadID             string
	Events               int
	ModelDurationMS      int64
	ToolDurationMS       int64
	RuntimeDurationMS    int64
	ReplyDurationMS      int64
	TotalDurationMS      int64
	RuntimeDone          bool
	GatewayDone          bool
	ModelRequests        int
	InputTokens          int
	OutputTokens         int
	TotalTokens          int
	EstimatedInputTokens int
	SavedEstimatedTokens int
	CompactedMessages    int
	CompactedToolResults int
	CacheHits            int
	CacheReadTokens      int
	CacheWriteTokens     int
	CacheInputTokens     int
	CacheOutputTokens    int
	ModelCallStarts      int
	ModelCallEnds        int
	ModelCallFailures    int
	ModelCallSkips       int
	ModelStages          map[string]TraceModelStageSummary
	ToolCalls            []string
}

type TraceModelStageSummary struct {
	Starts   int
	Ends     int
	Failures int
	Skips    int
}

func (s TraceSummary) ModelStageNames() []string {
	names := make([]string, 0, len(s.ModelStages))
	for name := range s.ModelStages {
		if strings.TrimSpace(name) == "" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func SummarizeTrace(path string) (TraceSummary, error) {
	file, err := os.Open(path)
	if err != nil {
		return TraceSummary{}, err
	}
	defer file.Close()
	out := TraceSummary{Path: path, ModelStages: map[string]TraceModelStageSummary{}}
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
		case "model_call_start":
			out.ModelCallStarts++
			addModelStageEvent(&out, event, "start")
		case "model_call_end":
			out.ModelCallEnds++
			addModelStageEvent(&out, event, "end")
		case "model_call_failed":
			out.ModelCallFailures++
			addModelStageEvent(&out, event, "failed")
		case "model_call_skipped":
			out.ModelCallSkips++
			addModelStageEvent(&out, event, "skipped")
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
			out.SavedEstimatedTokens += int(number(event["saved_estimated_tokens"]))
			out.CompactedMessages += int(number(event["compacted_messages"]))
			out.CompactedToolResults += int(number(event["compacted_tool_results"]))
			out.CacheHits += int(number(event["cache_hits"]))
			out.CacheReadTokens += int(number(event["cache_read_tokens"]))
			out.CacheWriteTokens += int(number(event["cache_write_tokens"]))
			out.CacheInputTokens += int(number(event["cache_input_tokens"]))
			out.CacheOutputTokens += int(number(event["cache_output_tokens"]))
		case "context_budget_estimated":
			out.EstimatedInputTokens += int(number(event["estimated_input_tokens"]))
		case "context_budget_compacted":
			out.SavedEstimatedTokens += int(number(event["saved_estimated_tokens"]))
			out.CompactedMessages += int(number(event["compacted_messages"]))
			out.CompactedToolResults += int(number(event["compacted_tool_results"]))
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

func addModelStageEvent(out *TraceSummary, event map[string]any, kind string) {
	if out == nil {
		return
	}
	if out.ModelStages == nil {
		out.ModelStages = map[string]TraceModelStageSummary{}
	}
	stage := traceString(event["model_stage"])
	if stage == "" {
		stage = "unknown"
	}
	summary := out.ModelStages[stage]
	switch kind {
	case "start":
		summary.Starts++
	case "end":
		summary.Ends++
	case "failed":
		summary.Failures++
	case "skipped":
		summary.Skips++
	}
	out.ModelStages[stage] = summary
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
	case json.Number:
		n, _ := v.Float64()
		return n
	case int64:
		return float64(v)
	case int:
		return float64(v)
	default:
		return 0
	}
}
