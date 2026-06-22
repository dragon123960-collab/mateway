package feishu

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/dongping/mateway/internal/channel"
)

func renderReplyMessage(reply channel.OutboundMessage) (string, string, error) {
	text := sanitizeFeishuText(reply)
	if text == "" {
		text = "No content."
	}
	return renderReplyCard(reply, text)
}

func renderReplyCard(reply channel.OutboundMessage, text string) (string, string, error) {
	card := map[string]any{
		"config": map[string]any{
			"wide_screen_mode": true,
			"enable_forward":   true,
		},
		"header": map[string]any{
			"template": headerTemplateForStyle(reply.Style),
			"title": map[string]any{
				"tag":     "plain_text",
				"content": feishuCardTitle(reply),
			},
		},
		"elements": buildCardElements(reply, text),
	}
	content, err := json.Marshal(card)
	return "interactive", string(content), err
}

func buildCardElements(reply channel.OutboundMessage, text string) []map[string]any {
	elements := []map[string]any{
		{
			"tag": "div",
			"text": map[string]any{
				"tag":     "lark_md",
				"content": text,
			},
		},
	}
	if progress := renderProgress(reply.Progress); progress != "" {
		elements = append(elements, map[string]any{
			"tag": "hr",
		})
		elements = append(elements, map[string]any{
			"tag": "div",
			"text": map[string]any{
				"tag":     "lark_md",
				"content": "**Progress**\n" + progress,
			},
		})
	}
	if note := cardFooterNote(reply); note != "" {
		elements = append(elements, map[string]any{
			"tag": "note",
			"elements": []map[string]any{
				{
					"tag":     "plain_text",
					"content": note,
				},
			},
		})
	}
	return elements
}

func renderProgress(steps []channel.ProgressStep) string {
	var b strings.Builder
	for _, step := range steps {
		line := renderProgressLine(step)
		if line == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(line)
	}
	return b.String()
}

func renderProgressLine(step channel.ProgressStep) string {
	title := firstNonEmpty(strings.TrimSpace(step.Tool), strings.TrimSpace(step.Title))
	status := strings.TrimSpace(step.Status)
	summary := strings.TrimSpace(step.Summary)
	if title == "" {
		return ""
	}
	if strings.EqualFold(status, "thinking") && (summary == "" || strings.EqualFold(summary, "waiting for model output") || strings.EqualFold(summary, "thinking")) {
		return ""
	}
	if status == "" {
		status = "recorded"
	}
	marker := progressMarker(status)
	var details []string
	details = append(details, progressStatusLabel(status))
	if step.DurationMS > 0 {
		details = append(details, formatDurationMS(step.DurationMS))
	}
	if step.TimedOut {
		details = append(details, "timed out")
	}
	if summary != "" {
		details = append(details, truncateProgressText(summary, 72))
	}
	return "- " + marker + " `" + escapeInlineCode(progressTitle(title)) + "`: " + strings.Join(details, " / ")
}

func progressMarker(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "accepted", "completed", "success", "done":
		return "✓"
	case "failed", "blocked", "error":
		return "✕"
	default:
		return "→"
	}
}

func progressStatusLabel(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "accepted", "completed", "success", "done":
		return "done"
	case "failed", "blocked", "error":
		return "blocked"
	case "running":
		return "running"
	default:
		return strings.TrimSpace(status)
	}
}

func progressTitle(title string) string {
	switch strings.TrimSpace(title) {
	case "file.read":
		return "Read"
	case "file.write":
		return "Write"
	case "file.delete":
		return "Delete"
	case "terminal.run":
		return "Run"
	case "web.search":
		return "Search"
	case "web.fetch":
		return "Fetch"
	case "schedule.manage":
		return "Schedule"
	case "task.search":
		return "Search tasks"
	case "task.resume":
		return "Resume task"
	default:
		return title
	}
}

func truncateProgressText(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}

func formatDurationMS(ms int64) string {
	if ms >= 1000 {
		return strconv.FormatFloat(float64(ms)/1000, 'f', 1, 64) + "s"
	}
	return strconv.FormatInt(ms, 10) + "ms"
}

func escapeInlineCode(text string) string {
	return strings.ReplaceAll(text, "`", "'")
}

func headerTemplateForStyle(style channel.MessageStyle) string {
	switch style {
	case channel.StylePartial:
		return "orange"
	case channel.StyleInputRequired, channel.StyleProcessing:
		return "blue"
	case channel.StyleError:
		return "red"
	default:
		return "green"
	}
}

func feishuCardTitle(reply channel.OutboundMessage) string {
	if title := strings.TrimSpace(reply.Title); title != "" {
		return title
	}
	switch reply.Style {
	case channel.StyleInputRequired:
		if isConfirmationInputRequired(reply) {
			return "Mateway Needs Confirmation"
		}
		return "Mateway Needs More Information"
	case channel.StyleError:
		return "Mateway Failed"
	default:
		return "Mateway"
	}
}

func cardFooterNote(reply channel.OutboundMessage) string {
	switch reply.Style {
	case channel.StylePartial:
		return "Status: partial"
	case channel.StyleInputRequired:
		if isConfirmationInputRequired(reply) {
			return "Reply with 1 to confirm and continue, or 2 to cancel."
		}
		return "Please reply directly with the missing information."
	case channel.StyleError:
		return "The task stopped at a safe point. You can add more information and retry."
	case "failed":
		return "Status: failed"
	default:
		return "Status: " + firstNonEmpty(strings.TrimSpace(string(reply.Style)), string(channel.StyleCompleted))
	}
}

func isConfirmationInputRequired(reply channel.OutboundMessage) bool {
	text := strings.ToLower(strings.TrimSpace(reply.Text))
	return strings.Contains(text, "reply 1 to confirm") && strings.Contains(text, "2 to cancel")
}

func sanitizeFeishuText(reply channel.OutboundMessage) string {
	text := strings.TrimSpace(reply.Text)
	if text == "" {
		return ""
	}
	if looksLikeJSONToolPlan(text) {
		return fallbackFeishuText(reply)
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	skipToolBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		switch {
		case strings.Contains(lower, "<minimax:tool_call"):
			continue
		case strings.Contains(lower, "</minimax:tool_call>"):
			continue
		case looksLikeToolCallDetailLine(lower, trimmed):
			continue
		case strings.Contains(lower, "[tool_call]"):
			skipToolBlock = true
			continue
		case strings.Contains(lower, "[/tool_call]"):
			skipToolBlock = false
			continue
		case skipToolBlock && trimmed == "":
			skipToolBlock = false
			continue
		case skipToolBlock && (strings.Contains(lower, "file.read") || strings.Contains(lower, "shell.run") || strings.Contains(lower, "args") || strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "}")):
			continue
		}
		out = append(out, line)
	}
	text = strings.TrimSpace(strings.Join(out, "\n"))
	if text == "" {
		return fallbackFeishuText(reply)
	}
	return text
}

func looksLikeToolCallDetailLine(lower, trimmed string) bool {
	if trimmed == "" {
		return false
	}
	if strings.Contains(lower, " args:") ||
		strings.Contains(lower, " risk:") ||
		strings.Contains(lower, "requires_confirm:") {
		return strings.Contains(lower, "file.") ||
			strings.Contains(lower, "shell.") ||
			strings.Contains(lower, "web.") ||
			strings.Contains(lower, "project.") ||
			strings.Contains(lower, "time.")
	}
	return false
}

func fallbackFeishuText(reply channel.OutboundMessage) string {
	switch reply.Style {
	case channel.StyleInputRequired:
		return "I need one more piece of information before I can continue."
	case channel.StyleError:
		return "The task failed and stopped at a safe point."
	default:
		return "Done."
	}
}

func looksLikeJSONToolPlan(text string) bool {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
		return false
	}
	lower := strings.ToLower(trimmed)
	return strings.Contains(lower, `"tool"`) &&
		strings.Contains(lower, `"args"`) &&
		(strings.Contains(lower, `"risk"`) || strings.Contains(lower, `"requires_confirm"`) || strings.Contains(lower, `"expected_evidence"`))
}
