package runtime

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
)

const redactedSecret = "[REDACTED_SECRET]"
const (
	storedRecentMessagesLimit = 20
	storedToolContentLimit    = 2048
	modelPromptCharBudget     = 120000
)

var (
	secretAssignmentPattern = regexp.MustCompile(`(?i)\b([a-z0-9_.-]*(?:secret|token|api[_-]?key|password|passwd|pwd|pass|authorization|auth[_-]?code|smtp[_-]?pass|imap[_-]?pass|pop3[_-]?pass)[a-z0-9_.-]*)(\s*[:=]\s*)(["']?)([^\s"',}#]+)(["']?)`)
	bearerTokenPattern      = regexp.MustCompile(`(?i)\b(bearer\s+)[a-z0-9._~+/=-]{12,}`)
)

func redactSecrets(value any) any {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		return redactSecretString(v)
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if isSecretKey(key) {
				out[key] = redactedSecret
				continue
			}
			out[key] = redactSecrets(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redactSecrets(item)
		}
		return out
	case agentcore.Message:
		v.Content = redactSecretString(v.Content)
		for i := range v.Parts {
			v.Parts[i] = redactMessagePart(v.Parts[i])
		}
		for i := range v.ToolCalls {
			v.ToolCalls[i] = redactToolCall(v.ToolCalls[i])
		}
		return v
	case agentcore.ToolCall:
		return redactToolCall(v)
	case agentcore.ToolResult:
		return redactToolResult(v)
	default:
		return v
	}
}

func redactMessagePart(part agentcore.MessagePart) agentcore.MessagePart {
	part.Text = redactSecretString(part.Text)
	if len(part.Metadata) > 0 {
		metadata := make(map[string]string, len(part.Metadata))
		for key, value := range part.Metadata {
			if isSecretKey(key) {
				metadata[key] = redactedSecret
				continue
			}
			metadata[key] = redactSecretString(value)
		}
		part.Metadata = metadata
	}
	return part
}

func redactToolCall(call agentcore.ToolCall) agentcore.ToolCall {
	if len(call.Args) == 0 {
		return call
	}
	args := make(map[string]any, len(call.Args))
	for key, value := range call.Args {
		if isSecretKey(key) {
			args[key] = redactedSecret
			continue
		}
		args[key] = redactSecrets(value)
	}
	call.Args = args
	return call
}

func redactToolResult(result agentcore.ToolResult) agentcore.ToolResult {
	result.Content = redactSecretString(result.Content)
	if len(result.Evidence) > 0 {
		evidence := make(map[string]any, len(result.Evidence))
		for key, value := range result.Evidence {
			if isSecretKey(key) {
				evidence[key] = redactedSecret
				continue
			}
			evidence[key] = redactSecrets(value)
		}
		result.Evidence = evidence
	}
	return result
}

func redactSecretString(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	text = bearerTokenPattern.ReplaceAllString(text, `${1}`+redactedSecret)
	text = secretAssignmentPattern.ReplaceAllString(text, `${1}${2}${3}`+redactedSecret+`${5}`)
	text = strings.ReplaceAll(text, redactedSecret+" "+redactedSecret, redactedSecret)
	return text
}

func isSecretKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	if isTelemetryKey(normalized) {
		return false
	}
	for _, marker := range []string{"secret", "token", "api_key", "apikey", "password", "passwd", "pwd", "smtp_pass", "imap_pass", "pop3_pass", "authorization", "auth_code"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func isTelemetryKey(key string) bool {
	switch key {
	case "input_tokens", "output_tokens", "total_tokens", "prompt_tokens", "completion_tokens", "max_tokens", "max_output_tokens":
		return true
	default:
		return false
	}
}

func redactPayload(payload map[string]any) map[string]any {
	out := make(map[string]any, len(payload))
	for key, value := range payload {
		if isSecretKey(key) {
			out[key] = redactedSecret
			continue
		}
		out[key] = redactSecrets(value)
	}
	return out
}

func redactedSummary(text string) string {
	return summarize(redactSecretString(fmt.Sprint(text)))
}

func redactMessagesForStorage(messages []agentcore.Message) []agentcore.Message {
	if len(messages) == 0 {
		return messages
	}
	out := make([]agentcore.Message, len(messages))
	for i, msg := range messages {
		out[i] = redactSecrets(msg).(agentcore.Message)
	}
	return out
}

type messageCompactStats struct {
	BeforeMessages int
	AfterMessages  int
	BeforeChars    int
	AfterChars     int
	TruncatedTools int
	DroppedSystem  int
	DroppedOld     int
}

func compactMessagesForStorage(messages []agentcore.Message) ([]agentcore.Message, messageCompactStats) {
	stats := messageCompactStats{BeforeMessages: len(messages), BeforeChars: messageChars(messages)}
	if len(messages) == 0 {
		return messages, stats
	}
	filtered := make([]agentcore.Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == agentcore.RoleSystem {
			stats.DroppedSystem++
			continue
		}
		if msg.Role == agentcore.RoleTool {
			content, truncated := truncateMiddle(msg.Content, storedToolContentLimit)
			msg.Content = content
			if truncated {
				stats.TruncatedTools++
			}
		}
		filtered = append(filtered, msg)
	}
	if len(filtered) > storedRecentMessagesLimit {
		stats.DroppedOld = len(filtered) - storedRecentMessagesLimit
		filtered = append([]agentcore.Message(nil), filtered[len(filtered)-storedRecentMessagesLimit:]...)
	}
	stats.AfterMessages = len(filtered)
	stats.AfterChars = messageChars(filtered)
	return filtered, stats
}

func prepareMessagesForModel(messages []agentcore.Message) ([]agentcore.Message, messageCompactStats, error) {
	prepared, stats := compactMessagesForStorage(redactMessagesForStorage(messages))
	if messageChars(prepared) <= modelPromptCharBudget {
		return prepared, stats, nil
	}
	for limit := storedToolContentLimit / 2; limit >= 256; limit /= 2 {
		prepared = shrinkToolMessages(prepared, limit)
		if messageChars(prepared) <= modelPromptCharBudget {
			stats.AfterChars = messageChars(prepared)
			stats.AfterMessages = len(prepared)
			return prepared, stats, nil
		}
	}
	for len(prepared) > 4 && messageChars(prepared) > modelPromptCharBudget {
		prepared = prepared[1:]
		stats.DroppedOld++
	}
	stats.AfterChars = messageChars(prepared)
	stats.AfterMessages = len(prepared)
	if stats.AfterChars > modelPromptCharBudget {
		return prepared, stats, fmt.Errorf("session context is still too large after compaction: %d chars", stats.AfterChars)
	}
	return prepared, stats, nil
}

func shrinkToolMessages(messages []agentcore.Message, limit int) []agentcore.Message {
	out := append([]agentcore.Message(nil), messages...)
	for i := range out {
		if out[i].Role != agentcore.RoleTool {
			continue
		}
		out[i].Content, _ = truncateMiddle(out[i].Content, limit)
	}
	return out
}

func messageChars(messages []agentcore.Message) int {
	total := 0
	for _, msg := range messages {
		total += len(msg.Content)
		for _, part := range msg.Parts {
			total += len(part.Text) + len(part.URI) + len(part.MimeType) + len(part.Name)
		}
		for _, call := range msg.ToolCalls {
			total += len(call.Name) + len(call.ID)
			for key, value := range call.Args {
				total += len(key) + len(fmt.Sprint(value))
			}
		}
	}
	return total
}

func truncateMiddle(text string, limit int) (string, bool) {
	if limit <= 0 || len(text) <= limit {
		return text, false
	}
	if limit < 80 {
		return text[:limit], true
	}
	head := limit / 2
	tail := limit - head
	return text[:head] + fmt.Sprintf("\n...[truncated %d chars]...\n", len(text)-limit) + text[len(text)-tail:], true
}
