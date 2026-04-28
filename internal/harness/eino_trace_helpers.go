package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	einosummarization "github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/schema"
)

type callbackAgentContextKey struct{}

type toolSearchSelection struct {
	SelectedTools []string `json:"selectedTools"`
}

type runTraceStats struct {
	ModelAttempts      int
	ModelCalls         int
	ToolCalls          int
	ModelErrors        int
	ToolErrors         int
	Model429s          int
	ToolChoices        int
	ToolSearches       int
	Summarizations     int
	ReductionPasses    int
	OffloadedResults   int
	Transfers          int
	ContextCompactions int
}

var persistedOutputPathPattern = regexp.MustCompile(`(?im)(?:saved to|保存到|保存至):\s*(.+)$`)

func currentCallbackAgentName(ctx context.Context, fallbacks ...string) string {
	if ctx != nil {
		if value, ok := ctx.Value(callbackAgentContextKey{}).(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return firstNonEmpty(fallbacks...)
}

func parseToolSearchSelection(content string) ([]string, bool) {
	var result toolSearchSelection
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &result); err != nil {
		return nil, false
	}
	if len(result.SelectedTools) == 0 {
		return nil, false
	}
	return result.SelectedTools, true
}

func parsePersistedOutputPath(content string) string {
	match := persistedOutputPathPattern.FindStringSubmatch(content)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}

func summarizeAgentEvents(iter *adk.AsyncIterator[*adk.AgentEvent]) string {
	if iter == nil {
		return ""
	}
	var (
		total         int
		assistant     int
		toolResults   int
		transfers     int
		interrupts    int
		customActions int
		eventErrors   int
	)
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event == nil {
			continue
		}
		total++
		if event.Err != nil {
			eventErrors++
		}
		if event.Action != nil {
			if event.Action.TransferToAgent != nil {
				transfers++
			}
			if event.Action.Interrupted != nil {
				interrupts++
			}
			if event.Action.CustomizedAction != nil {
				customActions++
			}
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		msg, err := event.Output.MessageOutput.GetMessage()
		if err != nil || msg == nil {
			continue
		}
		switch msg.Role {
		case schema.Assistant:
			assistant++
		case schema.Tool:
			toolResults++
		}
	}
	return fmt.Sprintf("events=%d assistant=%d tool_results=%d transfers=%d interrupts=%d custom=%d errors=%d",
		total, assistant, toolResults, transfers, interrupts, customActions, eventErrors)
}

func appendCustomizedActionStep(h *Harness, run Run, event *adk.AgentEvent) bool {
	if h == nil || event == nil || event.Action == nil || event.Action.CustomizedAction == nil {
		return false
	}
	action, ok := event.Action.CustomizedAction.(*einosummarization.CustomizedAction)
	if !ok || action == nil {
		return false
	}
	now := time.Now()
	switch action.Type {
	case einosummarization.ActionTypeBeforeSummarize:
		count := 0
		if action.Before != nil {
			count = len(action.Before.Messages)
		}
		h.appendRunStep(run.ID, RunStep{
			Kind:       "middleware_summarization_prepare",
			Status:     "completed",
			AgentName:  firstNonEmpty(event.AgentName, run.AgentName),
			Output:     fmt.Sprintf("messages=%d", count),
			StartedAt:  now,
			FinishedAt: now,
		})
	case einosummarization.ActionTypeGenerateSummary:
		phase := ""
		attempt := 0
		status := "completed"
		responseLen := 0
		errText := ""
		if action.GenerateSummary != nil {
			phase = string(action.GenerateSummary.Phase)
			attempt = action.GenerateSummary.Attempt
			if action.GenerateSummary.ModelResponse != nil {
				responseLen = len(strings.TrimSpace(einoMessageText(action.GenerateSummary.ModelResponse)))
			}
			if err := action.GenerateSummary.GetError(); err != nil {
				status = "failed"
				errText = trim(err.Error(), 180)
			}
		}
		output := fmt.Sprintf("phase=%s attempt=%d response_chars=%d", firstNonEmpty(phase, "primary"), attempt, responseLen)
		if errText != "" {
			output += " error=" + errText
		}
		h.appendRunStep(run.ID, RunStep{
			Kind:       "middleware_summarization_attempt",
			Status:     status,
			AgentName:  firstNonEmpty(event.AgentName, run.AgentName),
			Output:     trim(output, 320),
			StartedAt:  now,
			FinishedAt: now,
		})
	case einosummarization.ActionTypeAfterSummarize:
		count := 0
		if action.After != nil {
			count = len(action.After.Messages)
		}
		h.appendRunStep(run.ID, RunStep{
			Kind:       "middleware_summarization_apply",
			Status:     "completed",
			AgentName:  firstNonEmpty(event.AgentName, run.AgentName),
			Output:     fmt.Sprintf("messages=%d", count),
			StartedAt:  now,
			FinishedAt: now,
		})
	}
	return true
}

func buildRunTraceStats(steps []RunStep) runTraceStats {
	var stats runTraceStats
	for _, step := range steps {
		switch step.Kind {
		case "model_attempt":
			stats.ModelAttempts++
			if step.Status == "rate_limited" {
				stats.Model429s++
			}
		case "callback_model_end":
			stats.ModelCalls++
		case "callback_tool_end":
			stats.ToolCalls++
		case "callback_model_error":
			stats.ModelErrors++
		case "callback_tool_error":
			stats.ToolErrors++
		case "tool_choice":
			stats.ToolChoices++
		case "tool_search":
			stats.ToolSearches++
		case "middleware_summarization":
			stats.Summarizations++
		case "tool_reduction":
			stats.ReductionPasses++
		case "tool_offload":
			stats.OffloadedResults++
		case "transfer":
			stats.Transfers++
		case "context_compaction":
			stats.ContextCompactions++
		}
	}
	return stats
}
