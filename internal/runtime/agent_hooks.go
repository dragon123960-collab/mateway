package runtime

import (
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/config"
	toolpkg "github.com/dongping/mateway/internal/tool"
)

var runtimeToolTimeout = func(cfg *config.Root, toolName string) time.Duration {
	_ = cfg
	switch strings.TrimSpace(toolName) {
	case "file.read":
		return 30 * time.Second
	case "terminal.run":
		return 120 * time.Second
	default:
		return 60 * time.Second
	}
}

var runtimeToolRetryBudget = func(cfg *config.Root, toolName string) int {
	_ = cfg
	switch strings.TrimSpace(toolName) {
	case "web.fetch":
		return 1
	case "web.search":
		return 1
	default:
		return 0
	}
}

func acceptToolResult(tool agentcore.Tool, call agentcore.ToolCall, result agentcore.ToolResult) (string, map[string]any) {
	evidence := map[string]any{}
	for key, value := range result.Evidence {
		evidence[key] = value
	}
	if tool != nil {
		contract := agentcore.ContractFor(tool)
		risk := toolpkg.EffectiveRisk(tool, call)
		evidence["risk"] = string(risk)
		evidence["mutation"] = risk == agentcore.RiskGuardedMutation || risk == agentcore.RiskDangerous
		if contract.Acceptance != "" {
			evidence["acceptance_criteria"] = contract.Acceptance
		}
		if contract.Evidence != "" {
			evidence["evidence_contract"] = contract.Evidence
		}
	}
	if result.IsError {
		evidence["acceptance"] = "failed"
		return "failed", evidence
	}
	if len(result.Evidence) == 0 && strings.TrimSpace(result.Content) == "" {
		evidence["acceptance"] = "suspect"
		return "suspect", evidence
	}
	evidence["acceptance"] = "accepted"
	return "accepted", evidence
}
