package feishu

import (
	"errors"
	"fmt"
	"strings"

	agentharness "github.com/dongping/mateway/internal/harness"
	"github.com/dongping/mateway/internal/llm"
)

func formatRuntimeError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, agentharness.ErrSessionBusy) {
		return "上一条消息还在处理中，等我先收尾这一轮。"
	}
	msg := strings.TrimSpace(err.Error())
	lowered := strings.ToLower(msg)
	switch {
	case looksLikeToolFailure(lowered):
		return fmt.Sprintf("工具当前不可用：%s\n\n你仍然可以使用：\n- /skills\n- /run <skill-name>\n- /trace", msg)
	case llm.LooksLikeQuotaExceeded(err):
		return fmt.Sprintf("模型供应侧额度已用尽：%s\n\n建议稍后重试，或切到备用模型链。\n\n你仍然可以使用：\n- /skills\n- /run <skill-name>\n- /trace", msg)
	case llm.LooksLikeProviderRateLimited(err):
		return fmt.Sprintf("模型供应侧限流中：%s\n\n我会优先尝试备用模型；如果仍失败，可以稍后再试。\n\n你仍然可以使用：\n- /skills\n- /run <skill-name>\n- /trace", msg)
	case llm.LooksLikeLocalCooldown(err):
		return fmt.Sprintf("本地模型调用正在冷却：%s\n\n你仍然可以使用：\n- /skills\n- /run <skill-name>\n- /trace", msg)
	case looksLikeLLMFailure(lowered):
		return fmt.Sprintf("LLM 当前不可用：%s\n\n你仍然可以使用：\n- /skills\n- /run <skill-name>", msg)
	default:
		return fmt.Sprintf("运行时当前不可用：%s\n\n你仍然可以使用：\n- /skills\n- /run <skill-name>\n- /trace", msg)
	}
}

func looksLikeToolFailure(msg string) bool {
	markers := []string{
		"web search",
		"browser fetch",
		"tool ",
		"sandbox_exec",
		"external cli",
		"dangerous command",
		"path ",
		"not allowed for agent",
		"not available",
	}
	for _, marker := range markers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func looksLikeLLMFailure(msg string) bool {
	markers := []string{
		"llm ",
		"chat/completions",
		"no enabled model",
		"missing api_base",
		"model ",
		"cooling down",
	}
	for _, marker := range markers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
