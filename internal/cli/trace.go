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

func PrintTraceReport(out io.Writer, path string) error {
	report, err := buildTraceReport(path)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Trace Report")
	fmt.Fprintln(out, "trace:", path)
	if report.TraceID != "" {
		fmt.Fprintln(out, "trace_id:", report.TraceID)
	}
	if report.SessionKey != "" {
		fmt.Fprintln(out, "session_key:", report.SessionKey)
	}
	if report.TaskID != "" {
		fmt.Fprintln(out, "task_id:", report.TaskID)
	}
	if report.AgentID != "" {
		fmt.Fprintln(out, "agent_id:", report.AgentID)
	}
	if report.Request != "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Request")
		fmt.Fprintln(out, "- "+compactBlock(report.Request, 600))
	}
	if report.Contract.Summary != "" || len(report.Contract.RequiredTools) > 0 || len(report.Contract.RequiredEvidence) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Task Contract")
		if report.Contract.Summary != "" {
			fmt.Fprintln(out, "- summary:", report.Contract.Summary)
		}
		if report.Contract.Known {
			fmt.Fprintf(out, "- requires_tools: %t\n", report.Contract.RequiresTools)
		}
		if len(report.Contract.RequiredTools) > 0 {
			fmt.Fprintln(out, "- required_tools:", strings.Join(report.Contract.RequiredTools, ", "))
		}
		for _, evidence := range report.Contract.RequiredEvidence {
			fmt.Fprintf(out, "- evidence: %s via %s\n", compactInline(evidence.Description, 180), firstNonEmpty(evidence.Tool, "unspecified"))
		}
		if report.Contract.ExpectedOutcome != "" {
			fmt.Fprintln(out, "- expected:", report.Contract.ExpectedOutcome)
		}
	}
	if len(report.Models) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Models")
		for _, model := range report.Models {
			fmt.Fprintf(out, "- %s/%s", firstNonEmpty(model.Provider, "provider"), firstNonEmpty(model.Model, "model"))
			if model.InputTokens > 0 || model.OutputTokens > 0 {
				fmt.Fprintf(out, " tokens=%d/%d", model.InputTokens, model.OutputTokens)
			}
			if model.DurationMS > 0 {
				fmt.Fprintf(out, " duration=%dms", model.DurationMS)
			}
			fmt.Fprintln(out)
		}
	}
	if len(report.VisibleTools) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Visible Tools")
		fmt.Fprintln(out, "- "+strings.Join(report.VisibleTools, ", "))
		if report.HiddenTools > 0 {
			fmt.Fprintf(out, "- hidden_tools: %d\n", report.HiddenTools)
		}
		if len(report.TrimmedTools) > 0 {
			fmt.Fprintf(out, "- trimmed (budget): %s\n", strings.Join(report.TrimmedTools, ", "))
		}
		if len(report.NonDefaultExposed) > 0 {
			var entries []string
			for name, reason := range report.NonDefaultExposed {
				entries = append(entries, name+":"+reason)
			}
			sort.Strings(entries)
			fmt.Fprintf(out, "- non-default exposed: %s\n", strings.Join(entries, ", "))
		}
	}
	if len(report.ToolPolicies) > 0 || len(report.Tools) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Tool Process")
		for _, policy := range report.ToolPolicies {
			status := "allowed"
			if policy.Blocked {
				status = "blocked"
			}
			fmt.Fprintf(out, "- policy %s: %s", status, policy.Tool)
			if policy.Reason != "" {
				fmt.Fprintf(out, " - %s", compactInline(policy.Reason, 180))
			}
			fmt.Fprintln(out)
		}
		for _, tool := range report.Tools {
			status := "ok"
			if tool.Error {
				status = "error"
			}
			fmt.Fprintf(out, "- %s %s", status, tool.Name)
			if tool.Args != "" {
				fmt.Fprintf(out, " %s", tool.Args)
			}
			if tool.DurationMS > 0 {
				fmt.Fprintf(out, " (%dms)", tool.DurationMS)
			}
			if tool.Summary != "" {
				fmt.Fprintf(out, " - %s", compactInline(tool.Summary, 180))
			}
			fmt.Fprintln(out)
		}
	}
	if len(report.Judgments) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Result Judgment")
		for _, judgment := range report.Judgments {
			line := "- " + judgment.Type
			if judgment.Status != "" {
				line += " status=" + judgment.Status
			}
			if len(judgment.Missing) > 0 {
				line += " missing=" + strings.Join(judgment.Missing, "; ")
			}
			fmt.Fprintln(out, line)
		}
	}
	if report.FinalReply != "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Final Reply")
		fmt.Fprintln(out, compactBlock(report.FinalReply, 1200))
	}
	return nil
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

type traceReport struct {
	TraceID           string
	SessionKey        string
	TaskID            string
	AgentID           string
	Request           string
	Contract          traceReportContract
	Models            []traceReportModel
	VisibleTools      []string
	HiddenTools       int
	TrimmedTools      []string
	NonDefaultExposed map[string]string
	ToolPolicies      []traceReportToolPolicy
	Tools             []traceReportTool
	Judgments         []traceReportJudgment
	FinalReply        string
}

type traceReportContract struct {
	Summary          string
	Known            bool
	RequiresTools    bool
	RequiredTools    []string
	RequiredEvidence []traceReportEvidence
	ExpectedOutcome  string
}

type traceReportEvidence struct {
	Tool        string
	Description string
}

type traceReportModel struct {
	Provider     string
	Model        string
	InputTokens  int
	OutputTokens int
	DurationMS   int64
}

type traceReportToolPolicy struct {
	Tool    string
	Blocked bool
	Reason  string
}

type traceReportTool struct {
	Name       string
	Args       string
	DurationMS int64
	Summary    string
	Error      bool
}

type traceReportJudgment struct {
	Type    string
	Status  string
	Missing []string
}

func buildTraceReport(path string) (traceReport, error) {
	file, err := os.Open(path)
	if err != nil {
		return traceReport{}, err
	}
	defer file.Close()
	var report traceReport
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if report.TraceID == "" {
			report.TraceID = traceString(event["trace_id"])
		}
		if report.SessionKey == "" {
			report.SessionKey = traceString(event["session_key"])
		}
		if report.TaskID == "" {
			report.TaskID = traceString(event["task_id"])
		}
		if report.AgentID == "" {
			report.AgentID = traceString(event["agent_id"])
		}
		switch traceString(event["type"]) {
		case "request":
			report.Request = traceString(event["text"])
		case "task_contract_created":
			report.Contract = traceContractFromEvent(event)
		case "task_contract_reused":
			if report.Contract.Summary == "" {
				report.Contract.Summary = "reused existing task contract"
			}
		case "model_route_selected":
			report.Models = append(report.Models, traceReportModel{
				Provider: traceString(event["provider"]),
				Model:    firstNonEmpty(traceString(event["model"]), traceString(event["model_name"])),
			})
		case "message_start":
			if len(report.Models) > 0 {
				model := &report.Models[len(report.Models)-1]
				model.DurationMS += int64Number(event["duration_ms"])
				if message, _ := event["message"].(map[string]any); message != nil {
					if usage, _ := message["usage"].(map[string]any); usage != nil {
						model.Provider = firstNonEmpty(model.Provider, traceString(usage["provider"]))
						model.Model = firstNonEmpty(model.Model, traceString(usage["model"]))
						model.InputTokens += int(int64Number(usage["input_tokens"]))
						model.OutputTokens += int(int64Number(usage["output_tokens"]))
					}
				}
			}
		case "context_budget_estimated":
			if tools := stringListFromAny(event["tools"]); len(tools) > 0 {
				report.VisibleTools = tools
			}
			if hidden := int(int64Number(event["hidden_tools"])); hidden > 0 {
				report.HiddenTools = hidden
			}
		case "context_budget_trimmed":
			report.TrimmedTools = stringListFromAny(event["trimmed_tools"])
		case "context_budget_non_default_exposed":
			if exposed, _ := event["non_default_exposed"].(map[string]any); exposed != nil {
				if report.NonDefaultExposed == nil {
					report.NonDefaultExposed = make(map[string]string)
				}
				for name, reason := range exposed {
					report.NonDefaultExposed[name] = traceString(reason)
				}
			}
		case "hook_event":
			if traceString(event["hook"]) == "tool_policy_hook" {
				report.ToolPolicies = append(report.ToolPolicies, traceReportToolPolicy{
					Tool:    traceString(event["tool"]),
					Blocked: boolValue(event["block"]),
					Reason:  traceString(event["reason"]),
				})
			}
		case "tool_execution_end":
			result, _ := event["tool_result"].(map[string]any)
			report.Tools = append(report.Tools, traceReportTool{
				Name:       traceToolName(event),
				Args:       traceArgsValue(event),
				DurationMS: int64Number(event["duration_ms"]),
				Summary:    traceString(result["Content"]),
				Error:      boolValue(result["IsError"]),
			})
		case "task_contract_unsatisfied", "task_contract_satisfied", "await_user_input", "empty_action_promise":
			report.Judgments = append(report.Judgments, traceReportJudgment{
				Type:    traceString(event["type"]),
				Status:  traceString(event["status"]),
				Missing: stringListFromAny(event["missing"]),
			})
		case "reply":
			report.FinalReply = traceString(event["text"])
		}
	}
	return report, scanner.Err()
}

func traceContractFromEvent(event map[string]any) traceReportContract {
	return traceReportContract{
		Summary:          traceString(event["summary"]),
		Known:            true,
		RequiresTools:    boolValue(event["requires_tools"]),
		RequiredTools:    stringListFromAny(event["required_tools"]),
		RequiredEvidence: traceEvidenceList(event["required_evidence"]),
		ExpectedOutcome:  traceString(event["expected_outcome"]),
	}
}

func traceEvidenceList(value any) []traceReportEvidence {
	items, _ := value.([]any)
	out := make([]traceReportEvidence, 0, len(items))
	for _, item := range items {
		obj, _ := item.(map[string]any)
		if obj == nil {
			continue
		}
		evidence := traceReportEvidence{
			Tool:        traceString(obj["tool"]),
			Description: traceString(obj["description"]),
		}
		if evidence.Tool != "" || evidence.Description != "" {
			out = append(out, evidence)
		}
	}
	return out
}

func stringListFromAny(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text := traceString(item); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func boolValue(value any) bool {
	v, _ := value.(bool)
	return v
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
