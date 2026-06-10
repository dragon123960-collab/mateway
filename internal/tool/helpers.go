package tool

import (
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/util"
)

var (
	boolArg             = util.BoolArg
	configHome          = util.ConfigHome
	firstNonEmptyString = util.FirstNonEmptyString
)

func EffectiveRisk(tool agentcore.Tool, call agentcore.ToolCall) agentcore.Risk {
	if tool == nil {
		return agentcore.RiskGuardedMutation
	}
	if tool.Name() != "schedule.manage" {
		return tool.Risk()
	}
	switch strings.TrimSpace(toolArgString(call.Args, "action")) {
	case "list":
		return agentcore.RiskSafeRead
	case "delete":
		return agentcore.RiskDangerous
	default:
		return agentcore.RiskGuardedMutation
	}
}
