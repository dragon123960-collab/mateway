package feishu

import (
	"encoding/json"
	"fmt"
	"os"
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
	if actions := approvalActions(reply); len(actions) > 0 {
		elements = append(elements, map[string]any{
			"tag":     "action",
			"actions": actions,
		})
	}
	if note := cardFooterNote(reply.Style); note != "" {
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

func approvalActions(reply channel.OutboundMessage) []map[string]any {
	if strings.TrimSpace(reply.Style) != "approval_pending" {
		return nil
	}
	if !feishuApprovalButtonsEnabled() {
		return nil
	}
	value := func(decision, text string) map[string]any {
		out := map[string]any{
			"mateway_action": "approval",
			"decision":       decision,
			"mateway_text":   text,
		}
		if threadID := strings.TrimSpace(reply.ThreadID); threadID != "" {
			out["mateway_thread_id"] = threadID
			out["mateway_session_key"] = firstNonEmpty(strings.TrimSpace(reply.Channel), "feishu") + ":" + threadID
		}
		return out
	}
	return []map[string]any{
		{
			"tag":  "button",
			"type": "primary",
			"text": map[string]any{
				"tag":     "plain_text",
				"content": "同意",
			},
			"value": value("confirm", "确认"),
		},
		{
			"tag":  "button",
			"type": "default",
			"text": map[string]any{
				"tag":     "plain_text",
				"content": "拒绝",
			},
			"value": value("cancel", "取消"),
		},
	}
}

func feishuApprovalButtonsEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("MATEWAY_FEISHU_APPROVAL_BUTTONS")))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func headerTemplateForStyle(style string) string {
	switch strings.TrimSpace(style) {
	case "approval_pending":
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
	case "approval_pending":
		return "Mateway 等待确认"
	case "input_required":
		return "Mateway 需要补充信息"
	case "error":
		return "Mateway 执行失败"
	default:
		return "Mateway"
	}
}

func cardFooterNote(style string) string {
	switch strings.TrimSpace(style) {
	case "approval_pending":
		return "也可以直接回复“确认”或“取消”。"
	case "input_required":
		return "请直接回复消息补充所需信息。"
	case "error":
		return "任务已停在安全位置，可以补充信息后重试。"
	default:
		return fmt.Sprintf("状态：%s", firstNonEmpty(strings.TrimSpace(style), "completed"))
	}
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
		return "继续之前需要你确认。回复“确认”继续，或回复“取消”放弃。"
	case "input_required":
		return "我还需要你补充一个信息才能继续。"
	case "error":
		return "任务失败了，我已经停在安全位置。"
	default:
		return "完成。"
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
