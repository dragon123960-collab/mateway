package runtime

import (
	"regexp"
	"strings"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/tool"
)

const defaultReplyLimit = 4000

var multiBlankLines = regexp.MustCompile(`\n{3,}`)
var toolCallBlock = regexp.MustCompile(`(?is)\[TOOL_CALL\].*?(?:\[/TOOL_CALL\]|$)`)
var minimaxToolCallBlock = regexp.MustCompile(`(?is)<minimax:tool_call\b[^>]*>.*?(?:</minimax:tool_call>|$)`)
var jsonToolPlanBlock = regexp.MustCompile(`(?is)^\s*\[\s*\{.*"(?:tool|args|risk|requires_confirm|expected_evidence)".*\}\s*\]\s*$`)

type ResponseSanitizer interface {
	Sanitize(reply channel.OutboundMessage) channel.OutboundMessage
}

type DefaultSanitizer struct {
	ReplyLimit int
}

func (s DefaultSanitizer) Sanitize(reply channel.OutboundMessage) channel.OutboundMessage {
	reply.Text = sanitizeReplyText(reply.Style, reply.Text, s.ReplyLimit)
	return reply
}

func sanitizeReplyText(style, text string, limit int) string {
	text = strings.TrimSpace(text)
	text = stripToolCallEcho(text)
	text = stripJSONToolPlanEcho(text)
	text = stripPromptEcho(text)
	text = multiBlankLines.ReplaceAllString(text, "\n\n")
	text = strings.TrimSpace(text)
	if text == "" {
		return defaultReplyText(style)
	}
	if limit <= 0 {
		limit = defaultReplyLimit
	}
	return tool.Truncate(text, limit)
}

func stripJSONToolPlanEcho(text string) string {
	if !jsonToolPlanBlock.MatchString(text) {
		return text
	}
	return ""
}

func stripToolCallEcho(text string) string {
	text = toolCallBlock.ReplaceAllString(text, "")
	text = minimaxToolCallBlock.ReplaceAllString(text, "")
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	skipJSONish := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		switch {
		case strings.Contains(lower, "[tool_call]"):
			skipJSONish = true
			continue
		case skipJSONish && (strings.Contains(lower, `"tool"`) || strings.Contains(lower, `"args"`) || strings.Contains(lower, `"name"`) || strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "}")):
			continue
		case skipJSONish && trimmed == "":
			skipJSONish = false
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func stripPromptEcho(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	skipBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "Selected skills:":
			skipBlock = true
			continue
		case trimmed == "User task:":
			skipBlock = true
			continue
		case trimmed == "Plan:":
			skipBlock = true
			continue
		case trimmed == "Tool results:":
			skipBlock = true
			continue
		case skipBlock && trimmed == "":
			skipBlock = false
			continue
		case skipBlock:
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func defaultReplyText(style string) string {
	switch strings.TrimSpace(style) {
	case "approval_pending":
		return "需要确认后才能继续。"
	case "input_required":
		return "还需要你补充一点信息，我才能继续。"
	case "error":
		return "这次处理失败了，但我已经停在安全位置。"
	default:
		return "已处理完成。"
	}
}
