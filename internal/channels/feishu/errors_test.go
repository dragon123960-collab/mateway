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

func TestFormatRuntimeErrorHumanizesNodeRunError(t *testing.T) {
	got := formatRuntimeError(assertErr("[NodeRunError] failed to invoke tool[name:sandbox_exec id:call_x]: sandbox_exec failed: exec: \"opencli\": executable file not found in $PATH\n------------------------\nnode path: [node_1, ToolNode]"))
	if !strings.HasPrefix(got, "工具当前不可用") {
		t.Fatalf("unexpected runtime error prefix: %s", got)
	}
	if strings.Contains(got, "NodeRunError") || strings.Contains(got, "node path") {
		t.Fatalf("expected runtime error to be humanized: %s", got)
	}
	if !strings.Contains(got, "opencli") || !strings.Contains(got, "PATH") {
		t.Fatalf("expected actionable tool guidance: %s", got)
	}
}

type runtimeErr string

func (e runtimeErr) Error() string { return string(e) }

func assertErr(msg string) error { return runtimeErr(msg) }
