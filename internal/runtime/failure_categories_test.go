package runtime

import (
	"strings"
	"testing"

	"github.com/dongping/mateway/internal/agentcore"
)

func TestWebFetchClassifiesRateLimit(t *testing.T) {
	result := agentcore.ToolResult{
		ToolCallID: "call_1",
		Content:    "Edge: Too Many Requests",
		IsError:    true,
	}
	info := ClassifyToolFailure("web.fetch", result)
	if info.Category != FailureRetryable || !strings.Contains(info.Reason, "rate") {
		t.Fatalf("expected rate_limit, got category=%s reason=%q", info.Category, info.Reason)
	}
}

func TestWebFetchClassifiesBotProtection(t *testing.T) {
	cases := []string{
		"Cloudflare challenge page",
		"Please enable cookies and JavaScript",
		"Please enable JS to continue",
		"Disable any ad blocker",
		"CAPTCHA verification required",
	}
	for _, content := range cases {
		result := agentcore.ToolResult{
			ToolCallID: "call_1",
			Content:    content,
			IsError:    true,
		}
		info := ClassifyToolFailure("web.fetch", result)
		if info.Category != FailureBlocked || !strings.Contains(info.Reason, "bot") {
			t.Fatalf("case=%q: expected bot_protection, got category=%s reason=%q", content, info.Category, info.Reason)
		}
	}
}

func TestWebFetchClassifiesTimeout(t *testing.T) {
	cases := []string{
		"context deadline exceeded (Client.Timeout exceeded while awaiting headers)",
		"i/o timeout",
		"request timed out",
	}
	for _, content := range cases {
		result := agentcore.ToolResult{
			ToolCallID: "call_1",
			Content:    content,
			IsError:    true,
		}
		info := ClassifyToolFailure("web.fetch", result)
		if info.Category != FailureRetryable || !strings.Contains(info.Reason, "time") {
			t.Fatalf("case=%q: expected timeout, got category=%s reason=%q", content, info.Category, info.Reason)
		}
	}
}

func TestWebFetchClassifiesHttpError(t *testing.T) {
	result := agentcore.ToolResult{
		ToolCallID: "call_1",
		Content:    "HTTP status 403 Forbidden",
		IsError:    true,
	}
	info := ClassifyToolFailure("web.fetch", result)
	if info.Category != FailureFallback || !strings.Contains(info.Reason, "HTTP") {
		t.Fatalf("expected HTTP error, got category=%s reason=%q", info.Category, info.Reason)
	}
}

func TestWebFetchClassifiesNetworkError(t *testing.T) {
	cases := []string{
		"connection refused",
		"connection reset by peer",
		"no such host",
		"DNS error",
	}
	for _, content := range cases {
		result := agentcore.ToolResult{
			ToolCallID: "call_1",
			Content:    content,
			IsError:    true,
		}
		info := ClassifyToolFailure("web.fetch", result)
		if info.Category != FailureRetryable || !strings.Contains(info.Reason, "network") {
			t.Fatalf("case=%q: expected network error, got category=%s reason=%q", content, info.Category, info.Reason)
		}
	}
}

func TestClassifyTurnFailuresGroupsByTool(t *testing.T) {
	results := []agentcore.ToolResult{
		{ToolCallID: "call_1", Content: "Too Many Requests", IsError: true},
		{ToolCallID: "call_2", Content: "old_string not found in file", IsError: true},
	}
	calls := []agentcore.ToolCall{
		{ID: "call_1", Name: "web.fetch"},
		{ID: "call_2", Name: "file.edit"},
	}
	infos := classifyTurnFailures(results, calls)
	if len(infos) != 2 {
		t.Fatalf("expected 2 classified failures, got %d: %v", len(infos), infos)
	}
	if fi, ok := infos["web.fetch"]; !ok || fi.Category != FailureRetryable {
		t.Fatalf("expected web.fetch as retryable, got %v", infos)
	}
	if fi, ok := infos["file.edit"]; !ok || fi.Category != FailureRetryable {
		t.Fatalf("expected file.edit as retryable, got %v", infos)
	}
}
