package feishu

import (
	"encoding/json"
	"strings"

	"github.com/dongping/mateway/internal/channel"
)

func renderReplyMessage(reply channel.OutboundMessage) (string, string, error) {
	text := sanitizeFeishuText(reply)
	if text == "" {
		text = "暂无内容。"
	}
	content, err := json.Marshal(map[string]string{"text": text})
	return "text", string(content), err
}

func sanitizeFeishuText(reply channel.OutboundMessage) string {
	text := strings.TrimSpace(reply.Text)
	if text == "" {
		return ""
	}
	if looksLikeJSONToolPlan(text) {
		return fallbackFeishuText(reply.Style)
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
		return fallbackFeishuText(reply.Style)
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

func fallbackFeishuText(style string) string {
	switch strings.TrimSpace(style) {
	case "approval_pending":
		return "需要你确认后我才能继续。请回复“同意”或“取消”。"
	case "input_required":
		return "还需要你补充一点信息，我才能继续。"
	case "error":
		return "这次处理失败了，但我已经停在安全位置。"
	default:
		return "已处理完成。"
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
