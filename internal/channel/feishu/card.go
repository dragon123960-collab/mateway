package feishu

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/i18n"
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
	actions := approvalActions(reply)
	elements := []map[string]any{
		{
			"tag": "div",
			"text": map[string]any{
				"tag":     "lark_md",
				"content": text,
			},
		},
	}
	if len(actions) > 0 {
		elements = append(elements, map[string]any{
			"tag":     "action",
			"actions": actions,
		})
	}
	if note := cardFooterNote(reply, text, len(actions) > 0); note != "" {
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
	locale := feishuReplyLocale(reply)
	catalog := i18n.New(i18n.Config{})
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
				"content": catalog.T(locale, "feishu.button.confirm", nil),
			},
			"value": value("confirm", catalog.T(locale, "aliases.confirm.primary", nil)),
		},
		{
			"tag":  "button",
			"type": "default",
			"text": map[string]any{
				"tag":     "plain_text",
				"content": catalog.T(locale, "feishu.button.cancel", nil),
			},
			"value": value("cancel", catalog.T(locale, "aliases.cancel.primary", nil)),
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
	catalog := i18n.New(i18n.Config{})
	locale := feishuReplyLocale(reply)
	switch strings.TrimSpace(reply.Style) {
	case "approval_pending":
		return catalog.T(locale, "feishu.title.approval_pending", nil)
	case "input_required":
		return catalog.T(locale, "feishu.title.input_required", nil)
	case "error":
		return catalog.T(locale, "feishu.title.error", nil)
	default:
		return catalog.T(locale, "feishu.title.default", nil)
	}
}

func cardFooterNote(reply channel.OutboundMessage, text string, hasActions bool) string {
	catalog := i18n.New(i18n.Config{})
	locale := feishuReplyLocale(reply)
	switch strings.TrimSpace(reply.Style) {
	case "approval_pending":
		if hasActions || approvalTextMentionsConfirmAndCancel(text) {
			return ""
		}
		return catalog.T(locale, "feishu.footer.approval_pending", nil)
	case "partial":
		return catalog.T(locale, "feishu.footer.partial", nil)
	case "input_required":
		return catalog.T(locale, "feishu.footer.input_required", nil)
	case "error":
		return catalog.T(locale, "feishu.footer.error", nil)
	default:
		return catalog.T(locale, "feishu.footer.default", map[string]string{"status": firstNonEmpty(strings.TrimSpace(reply.Style), "completed")})
	}
}

func approvalTextMentionsConfirmAndCancel(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return false
	}
	catalog := i18n.New(i18n.Config{})
	hasConfirm := false
	for _, cue := range splitCardCueList(catalog.T(i18n.LocaleZH, "feishu.approval_text.confirm_cues", nil)) {
		if strings.Contains(normalized, cue) {
			hasConfirm = true
			break
		}
	}
	hasCancel := false
	for _, cue := range splitCardCueList(catalog.T(i18n.LocaleZH, "feishu.approval_text.cancel_cues", nil)) {
		if strings.Contains(normalized, cue) {
			hasCancel = true
			break
		}
	}
	return hasConfirm && hasCancel
}

func splitCardCueList(text string) []string {
	var out []string
	for _, item := range strings.Split(text, ",") {
		item = strings.TrimSpace(strings.ToLower(item))
		if item != "" {
			out = append(out, item)
		}
	}
	return out
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
	catalog := i18n.New(i18n.Config{})
	locale := feishuReplyLocale(reply)
	switch strings.TrimSpace(reply.Style) {
	case "approval_pending":
		return catalog.T(locale, "feishu.fallback.approval_pending", nil)
	case "input_required":
		return catalog.T(locale, "feishu.fallback.input_required", nil)
	case "error":
		return catalog.T(locale, "feishu.fallback.error", nil)
	default:
		return catalog.T(locale, "feishu.fallback.default", nil)
	}
}

func feishuReplyLocale(reply channel.OutboundMessage) string {
	return i18n.ResolveLocale(reply.Locale, reply.Text)
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
