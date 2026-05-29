package runtime

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
)

const redactedSecret = "[REDACTED_SECRET]"

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
	for _, marker := range []string{"secret", "token", "api_key", "apikey", "password", "passwd", "pwd", "smtp_pass", "imap_pass", "pop3_pass", "authorization", "auth_code"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
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
