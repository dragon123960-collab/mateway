package runtime

import (
	"fmt"
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/session"
)

func (rt Runtime) emitProgress(msg channel.InboundMessage, state session.State, taskID string, eventOffset int, current channel.ProgressStep) {
	if rt.ProgressSink == nil {
		return
	}
	steps := progressStepsForTaskSince(state, taskID, eventOffset)
	if strings.TrimSpace(current.Title) != "" || strings.TrimSpace(current.Tool) != "" {
		steps = append(steps, current)
	}
	rt.ProgressSink(channel.OutboundMessage{
		Channel:  msg.Channel,
		ThreadID: msg.ThreadID,
		Text:     "Processing...",
		Style:    channel.StyleProcessing,
		Progress: steps,
	})
}

func summarizeToolCall(call agentcore.ToolCall) string {
	switch call.Name {
	case "terminal.run":
		return compactProgressSummary(fmt.Sprint(call.Args["command"]))
	case "file.read", "file.write", "file.delete":
		return compactProgressSummary(fmt.Sprint(call.Args["path"]))
	case "web.search":
		return compactProgressSummary(fmt.Sprint(call.Args["query"]))
	case "web.fetch":
		return compactProgressSummary(fmt.Sprint(call.Args["url"]))
	default:
		return ""
	}
}

func summarizeAssistantToolActivity(message agentcore.Message) string {
	if len(message.ToolCalls) > 0 {
		if len(message.ToolCalls) == 1 {
			return "prepared tool call " + message.ToolCalls[0].Name
		}
		return fmt.Sprintf("prepared %d tool calls", len(message.ToolCalls))
	}
	return ""
}

func progressStepsForTask(state session.State, taskID string) []channel.ProgressStep {
	return progressStepsForTaskSince(state, taskID, 0)
}

func progressStepsForTaskSince(state session.State, taskID string, eventOffset int) []channel.ProgressStep {
	task := taskFromState(state, taskID)
	events := task.Execution.Events
	if eventOffset < 0 {
		eventOffset = 0
	}
	if eventOffset > len(events) {
		eventOffset = len(events)
	}
	events = events[eventOffset:]
	out := planProgressSteps(task.Execution.Contract)
	for _, event := range events {
		step := progressStepFromExecutionEvent(event)
		if strings.TrimSpace(step.Title) == "" {
			continue
		}
		out = append(out, step)
	}
	const limit = 8
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func planProgressSteps(contract *session.TaskContract) []channel.ProgressStep {
	if contract == nil || len(contract.PlanItems) == 0 {
		return nil
	}
	out := make([]channel.ProgressStep, 0, len(contract.PlanItems))
	for _, item := range contract.PlanItems {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = strings.TrimSpace(item.ID)
		}
		if title == "" {
			continue
		}
		summary := compactProgressSummary(item.Criteria)
		if strings.TrimSpace(item.Tool) != "" {
			summary = compactProgressSummary(strings.TrimSpace(item.Tool) + " / " + item.Criteria)
		}
		out = append(out, channel.ProgressStep{
			Title:   title,
			Status:  strings.TrimSpace(item.Status),
			Summary: summary,
		})
	}
	return out
}

func progressStepFromExecutionEvent(event session.ExecutionEvent) channel.ProgressStep {
	title := strings.TrimSpace(event.Type)
	if event.Tool != "" {
		title = strings.TrimSpace(event.Tool)
	}
	step := channel.ProgressStep{
		Title:   title,
		Status:  strings.TrimSpace(event.Status),
		Tool:    strings.TrimSpace(event.Tool),
		Summary: compactProgressSummary(event.Summary),
	}
	if accepted, ok := event.Evidence["accepted"].(bool); ok && accepted {
		step.Status = firstNonEmpty(step.Status, "accepted")
	}
	if timedOut, ok := event.Evidence["timed_out"].(bool); ok {
		step.TimedOut = timedOut
	}
	if elapsed, ok := int64Evidence(event.Evidence["elapsed_ms"]); ok {
		step.DurationMS = elapsed
	}
	return step
}

func int64Evidence(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	default:
		return 0, false
	}
}

func compactProgressSummary(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" {
		return ""
	}
	return trimAndTruncateRunesWithSuffix(text, 80)
}

func summarize(text string) string {
	return trimAndTruncateRunesWithSuffix(text, 160)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
