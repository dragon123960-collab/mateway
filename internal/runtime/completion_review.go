package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/session"
)

type defaultCompletionReviewHookProvider struct{}

func (defaultCompletionReviewHookProvider) Name() string { return "default_completion_review" }

func (defaultCompletionReviewHookProvider) CompletionReviewHook(ctx context.Context, input CompletionReviewInput) (CompletionReviewResult, error) {
	if input.Model == nil {
		return heuristicCompletionReview(input), nil
	}
	msg, err := input.Model.Next(ctx, agentcore.Context{
		SystemPrompt: "You review whether an agent task is actually complete. Return JSON only. Do not call tools.",
		Messages: []agentcore.Message{{
			Role: agentcore.RoleUser,
			Content: "Review this task completion.\n" +
				"Schema: {\"completed\":boolean,\"reason\":string,\"missing_items\":[string],\"suggested_followup\":string}\n" +
				"Mark completed=false when the final reply is only a plan, says it will do more work next, lacks requested deliverables/actions, or only reports environment discovery.\n" +
				"Do not trust claims about files, email, publishing, or remote actions unless the tool steps show accepted evidence.\n" +
				"If incomplete, suggested_followup should be a short instruction for the agent to continue.\n\n" +
				"User request:\n" + strings.TrimSpace(input.UserText) + "\n\n" +
				"Tool steps:\n" + renderReviewSteps(input.Task.Steps) + "\n\n" +
				"Recent transcript:\n" + renderReviewTranscript(input.TranscriptMessages, 8) + "\n\n" +
				"Final reply:\n" + strings.TrimSpace(input.FinalText),
		}},
		Tools: nil,
	})
	if err != nil {
		return heuristicCompletionReview(input), nil
	}
	result, err := parseCompletionReviewJSON(msg.Content)
	if err != nil {
		return heuristicCompletionReview(input), nil
	}
	return normalizeCompletionReview(result, input), nil
}

func parseCompletionReviewJSON(text string) (CompletionReviewResult, error) {
	var payload struct {
		Completed         bool     `json:"completed"`
		Reason            string   `json:"reason"`
		MissingItems      []string `json:"missing_items"`
		SuggestedFollowUp string   `json:"suggested_followup"`
	}
	if err := json.Unmarshal([]byte(stripJSONFence(text)), &payload); err != nil {
		return CompletionReviewResult{}, err
	}
	return CompletionReviewResult{
		Completed:         payload.Completed,
		Reason:            strings.TrimSpace(payload.Reason),
		MissingItems:      compactReviewItems(payload.MissingItems, 8),
		SuggestedFollowUp: strings.TrimSpace(payload.SuggestedFollowUp),
	}, nil
}

func stripJSONFence(text string) string {
	raw := strings.TrimSpace(text)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	return strings.TrimSpace(raw)
}

func normalizeCompletionReview(result CompletionReviewResult, input CompletionReviewInput) CompletionReviewResult {
	result.Reason = strings.TrimSpace(result.Reason)
	result.SuggestedFollowUp = strings.TrimSpace(result.SuggestedFollowUp)
	result.MissingItems = compactReviewItems(result.MissingItems, 8)
	if !result.Completed && result.SuggestedFollowUp == "" {
		result.SuggestedFollowUp = defaultCompletionFollowUp(result, input)
	}
	if result.Completed && looksLikeIncompleteFinalText(input.FinalText) {
		result.Completed = false
		if result.Reason == "" {
			result.Reason = "final reply looks like an intermediate plan"
		}
		result.SuggestedFollowUp = defaultCompletionFollowUp(result, input)
	}
	if result.Reason == "" {
		if result.Completed {
			result.Reason = "review judged the requested task complete"
		} else {
			result.Reason = "review judged the requested task incomplete"
		}
	}
	return result
}

func heuristicCompletionReview(input CompletionReviewInput) CompletionReviewResult {
	if looksLikeIncompleteFinalText(input.FinalText) {
		result := CompletionReviewResult{
			Completed:    false,
			Reason:       "final reply looks like an intermediate plan",
			MissingItems: []string{"requested work is not fully evidenced"},
		}
		result.SuggestedFollowUp = defaultCompletionFollowUp(result, input)
		return result
	}
	return CompletionReviewResult{Completed: true, Reason: "heuristic review found no incomplete signals"}
}

func defaultCompletionFollowUp(result CompletionReviewResult, input CompletionReviewInput) string {
	var b strings.Builder
	b.WriteString("Continue now. Do not give a final answer until the completion review passes.")
	if len(result.MissingItems) > 0 {
		b.WriteString(" Missing items: ")
		b.WriteString(strings.Join(result.MissingItems, "; "))
		b.WriteString(".")
	}
	return b.String()
}

func renderReviewSteps(steps []session.TaskStep) string {
	if len(steps) == 0 {
		return "(none)"
	}
	var lines []string
	for _, step := range steps {
		tool := strings.TrimSpace(step.Tool)
		if tool == "" {
			tool = "unknown"
		}
		status := strings.TrimSpace(step.Status)
		if status == "" {
			status = "unknown"
		}
		summary := strings.TrimSpace(step.Summary)
		if len(summary) > 220 {
			summary = summary[:220] + "..."
		}
		if summary == "" {
			lines = append(lines, fmt.Sprintf("- %s status=%s", tool, status))
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s status=%s summary=%s", tool, status, summary))
	}
	return strings.Join(lines, "\n")
}

func renderReviewTranscript(messages []agentcore.Message, limit int) string {
	if limit <= 0 {
		limit = 8
	}
	start := len(messages) - limit
	if start < 0 {
		start = 0
	}
	var lines []string
	for _, msg := range messages[start:] {
		if msg.Role == agentcore.RoleSystem {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" && len(msg.ToolCalls) > 0 {
			content = fmt.Sprintf("%d tool call(s)", len(msg.ToolCalls))
		}
		if len(content) > 320 {
			content = content[:320] + "..."
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", msg.Role, content))
	}
	if len(lines) == 0 {
		return "(none)"
	}
	return strings.Join(lines, "\n")
}

func compactReviewItems(items []string, limit int) []string {
	var out []string
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, item)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func looksLikeIncompleteFinalText(text string) bool {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)
	if lower == "" {
		return true
	}
	if strings.HasSuffix(trimmed, ":") || strings.HasSuffix(trimmed, "：") {
		return true
	}
	cues := []string{
		"next i will", "i will now", "will proceed", "will continue", "continue now",
		"start writing", "start creating", "start sending", "check the script",
		"接下来", "下一步", "然后我会", "我将", "准备开始", "并行", "继续处理",
		"环境摸清", "环境梳清", "先摸清", "先生成", "重写脚本", "检查脚本", "预计", "计划",
	}
	for _, cue := range cues {
		if strings.Contains(lower, cue) {
			return true
		}
	}
	return false
}
