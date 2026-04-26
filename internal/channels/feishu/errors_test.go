package feishu

import (
	"strings"
	"testing"
)

func TestFormatRuntimeErrorClassifiesToolFailure(t *testing.T) {
	got := formatRuntimeError(assertErr("web search http 504: timeout"))
	if want := "工具当前不可用"; !strings.HasPrefix(got, want) {
		t.Fatalf("unexpected tool failure message: %s", got)
	}
}

func TestFormatRuntimeErrorClassifiesLLMFailure(t *testing.T) {
	got := formatRuntimeError(assertErr("llm http 503: unavailable"))
	if want := "LLM 当前不可用"; !strings.HasPrefix(got, want) {
		t.Fatalf("unexpected llm failure message: %s", got)
	}
}

func TestFormatRuntimeErrorClassifiesProviderQuota(t *testing.T) {
	got := formatRuntimeError(assertErr("llm fallback exhausted after [qwen3.6-plus, qwen3.5-plus]: model qwen3.6-plus failed, trying fallback: llm http 429: {\"error\":{\"message\":\"usage allocated quota exceeded\"}}"))
	if want := "模型供应侧额度已用尽"; !strings.HasPrefix(got, want) {
		t.Fatalf("unexpected quota failure message: %s", got)
	}
}

type runtimeErr string

func (e runtimeErr) Error() string { return string(e) }

func assertErr(msg string) error { return runtimeErr(msg) }
