package feishu

import (
	"encoding/json"
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

func headerTemplateForStyle(style string) string {
	switch strings.TrimSpace(style) {
	case "partial":
		return "orange"
	case "input_required":
		return "blue"
	case "error":
		return "red"
	default:
		return "green"
	}
}

func feishuCardTitle(reply channel.OutboundMessage) string {
	if title := strings.TrimSpace(reply.Title); title != "" {
		return title
	}
	switch strings.TrimSpace(reply.Style) {
	case "input_required":
		return "Mateway Needs More Information"
	case "error":
		return "Mateway Failed"
	default:
		return "Mateway"
	}
}

func cardFooterNote(reply channel.OutboundMessage) string {
	switch strings.TrimSpace(reply.Style) {
	case "partial":
		return "Status: partial"
	case "input_required":
		return "Please reply directly with the missing information."
	case "error":
		return "The task stopped at a safe point. You can add more information and retry."
	default:
		return "Status: " + firstNonEmpty(strings.TrimSpace(reply.Style), "completed")
	}
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
	switch strings.TrimSpace(reply.Style) {
	case "input_required":
		return "I need one more piece of information before I can continue."
	case "error":
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
