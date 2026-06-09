package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/dongping/mateway/internal/runtime"
	"github.com/dongping/mateway/internal/session"
)

func latestTracePath(state session.State) string {
	for i := len(state.Tasks) - 1; i >= 0; i-- {
		if path := strings.TrimSpace(state.Tasks[i].TracePath); path != "" {
			return path
		}
	}
	return ""
}

func printTraceSummary(out io.Writer, path string) error {
	summary, err := runtime.SummarizeTrace(path)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "trace:", summary.Path)
	if summary.TraceID != "" {
		fmt.Fprintln(out, "trace_id:", summary.TraceID)
	}
	if summary.SessionKey != "" {
		fmt.Fprintln(out, "session_key:", summary.SessionKey)
	}
	if summary.Channel != "" {
		fmt.Fprintln(out, "channel:", summary.Channel)
	}
	if summary.AgentID != "" {
		fmt.Fprintln(out, "agent_id:", summary.AgentID)
	}
	if summary.TaskID != "" {
		fmt.Fprintln(out, "task_id:", summary.TaskID)
	}
	fmt.Fprintln(out, "events:", summary.Events)
	fmt.Fprintf(out, "complete: %t\n", summary.RuntimeDone)
	fmt.Fprintln(out, "model_ms:", summary.ModelDurationMS)
	fmt.Fprintln(out, "tool_ms:", summary.ToolDurationMS)
	fmt.Fprintln(out, "runtime_ms:", summary.RuntimeDurationMS)
	if len(summary.ToolCalls) > 0 {
		fmt.Fprintln(out, "tools:", strings.Join(summary.ToolCalls, ", "))
	}
	return nil
}

func PrintTraceEvents(out io.Writer, path string) error {
	return PrintTraceEventsWithOptions(out, path, TraceEventsOptions{})
}

type TraceEventsOptions struct {
	JSON bool
}

func PrintTraceEventsWithOptions(out io.Writer, path string, opts TraceEventsOptions) error {
	return printTraceEventsWithOptions(out, path, opts)
}

func printTraceEvents(out io.Writer, path string) error {
	return printTraceEventsWithOptions(out, path, TraceEventsOptions{})
}

func printTraceEventsWithOptions(out io.Writer, path string, opts TraceEventsOptions) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if opts.JSON {
			processEvent, ok := processEventFromTrace(event)
			if !ok {
				continue
			}
			data, err := json.Marshal(processEvent)
			if err != nil {
				return err
			}
			fmt.Fprintln(out, string(data))
			continue
		}
		line := renderTraceEvent(event)
		if strings.TrimSpace(line) != "" {
			fmt.Fprintln(out, line)
		}
	}
	return scanner.Err()
}

func renderTraceEvent(event map[string]any) string {
	processEvent, ok := processEventFromTrace(event)
	if !ok {
		return ""
	}
	switch processEvent.Type {
	case "final.completed", "final.failed":
		return "Assistant\n" + compactInline(processEvent.Text, 160)
	default:
		return renderProcessEvent(processEvent, false)
	}
}

func processEventFromTrace(event map[string]any) (ProcessEvent, bool) {
	eventType := strings.TrimSpace(fmt.Sprint(event["type"]))
	duration := int64Number(event["duration_ms"])
	eventTime := traceString(event["time"])
	switch eventType {
	case "model_start":
		return ProcessEvent{Type: "model.thinking", Title: "model", Status: "thinking", Time: eventTime}, true
	case "message_start":
		return processMessageStartEvent(event, duration, eventTime)
	case "tool_execution_start":
		return ProcessEvent{Type: "tool.started", Status: "running", Tool: traceToolName(event), Args: traceArgsValue(event), Time: eventTime}, true
	case "tool_execution_progress":
		return ProcessEvent{Type: "tool.progress", Status: "running", Tool: traceToolName(event), DurationMS: duration, Time: eventTime}, true
	case "tool_execution_end":
		return processToolEndEvent(event, duration, eventTime)
	case "tool_blocked":
		return ProcessEvent{Type: "tool.blocked", Status: "failed", Tool: firstNonEmpty(traceString(event["tool"]), traceToolName(event)), Summary: compactInline(traceString(event["reason"]), 240), Time: eventTime}, true
	case "reply":
		eventType := "final.completed"
		if strings.TrimSpace(traceString(event["style"])) == "error" {
			eventType = "final.failed"
		}
		return ProcessEvent{Type: eventType, Text: compactInline(traceString(event["text"]), 1000), Style: traceString(event["style"]), Time: eventTime}, true
	case "runtime_done":
		return ProcessEvent{Type: "runtime.completed", DurationMS: duration, Time: eventTime}, true
	case "gateway_done":
		return ProcessEvent{Type: "gateway.completed", DurationMS: int64Number(event["total_duration_ms"]), Time: eventTime}, true
	default:
		return ProcessEvent{}, false
	}
}

func processMessageStartEvent(event map[string]any, duration int64, eventTime string) (ProcessEvent, bool) {
	message, _ := event["message"].(map[string]any)
	toolCalls, _ := message["ToolCalls"].([]any)
	if len(toolCalls) == 1 {
		call, _ := toolCalls[0].(map[string]any)
		return ProcessEvent{Type: "model.prepared_tools", Title: "model", Status: "thinking", Summary: "prepared tool call " + traceString(call["Name"]), DurationMS: duration, Time: eventTime}, true
	}
	if len(toolCalls) > 1 {
		return ProcessEvent{Type: "model.prepared_tools", Title: "model", Status: "thinking", Summary: fmt.Sprintf("prepared %d tool calls", len(toolCalls)), DurationMS: duration, Time: eventTime}, true
	}
	return ProcessEvent{}, false
}

func processToolEndEvent(event map[string]any, duration int64, eventTime string) (ProcessEvent, bool) {
	result, _ := event["tool_result"].(map[string]any)
	eventType := "tool.completed"
	status := "success"
	if isError, _ := result["IsError"].(bool); isError {
		eventType = "tool.blocked"
		status = "failed"
	}
	content := compactInline(traceString(result["Content"]), 160)
	return ProcessEvent{Type: eventType, Status: status, Tool: traceToolName(event), Summary: content, DurationMS: duration, Time: eventTime}, true
}

func traceToolName(event map[string]any) string {
	call, _ := event["tool_call"].(map[string]any)
	return firstNonEmpty(traceString(call["Name"]), "tool")
}

func traceArgsValue(event map[string]any) string {
	call, _ := event["tool_call"].(map[string]any)
	args, ok := call["Args"].(map[string]any)
	if !ok || len(args) == 0 {
		return ""
	}
	for _, key := range []string{"path", "command", "query", "url"} {
		if value := compactInline(traceString(args[key]), 120); value != "" {
			return value
		}
	}
	var keys []string
	for key := range args {
		if strings.HasPrefix(key, "_mateway_") {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var parts []string
	for _, key := range keys {
		parts = append(parts, key+"="+compactInline(traceString(args[key]), 48))
	}
	if len(parts) > 0 {
		return compactInline(strings.Join(parts, " "), 120)
	}
	return ""
}

func durationSuffix(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return fmt.Sprintf(" (%dms)", ms)
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

func int64Number(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	default:
		return 0
	}
}
