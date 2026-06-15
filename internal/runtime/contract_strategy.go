package runtime

import (
	"strings"

	"github.com/dongping/mateway/internal/session"
)

type contractStrategy string

const (
	contractStrategyDirect         contractStrategy = "direct"
	contractStrategyAutoContract   contractStrategy = "auto_contract"
	contractStrategyReviewRequired contractStrategy = "review_required"
)

var highRiskTools = map[string]bool{
	"file.write":      true,
	"file.edit":       true,
	"file.delete":     true,
	"terminal.run":    true,
	"schedule.manage": true,
}

func classifyContractStrategy(goal, userText string, contract session.TaskContract) contractStrategy {
	text := strings.ToLower(strings.TrimSpace(firstNonEmpty(userText, goal)))

	// Check explicit review request FIRST, before any other classification.
	// This ensures "plan first" or "show me the plan" always triggers review,
	// even for tasks that would otherwise be direct.
	if explicitReviewRequest(text) {
		return contractStrategyReviewRequired
	}

	if !contract.RequiresTools && len(contract.RequiredTools) == 0 && len(contract.RequiredEvidence) == 0 && len(contract.RequiredSkills) == 0 {
		if looksLikeActionTask(text) {
			return contractStrategyReviewRequired
		}
		return contractStrategyDirect
	}

	if len(contract.RequiredSkills) > 0 {
		return contractStrategyReviewRequired
	}

	if hasHighRiskTool(contract.RequiredTools) {
		return contractStrategyReviewRequired
	}

	if hasHighRiskPlanItem(contract.PlanItems) {
		return contractStrategyReviewRequired
	}

	if needsExternalPublish(text) {
		return contractStrategyReviewRequired
	}

	if isMultiStepDeliveryTask(contract, text) {
		return contractStrategyReviewRequired
	}

	return contractStrategyAutoContract
}

// looksLikeActionTask detects tasks that need tools even when the fallback
// contract has RequiresTools=false. This mirrors the old
// shouldSkipTaskContractModel action markers that were lost.
func looksLikeActionTask(text string) bool {
	if text == "" {
		return false
	}
	for _, marker := range []string{
		"read ", "write ", "edit ", "create ", "delete ", "run ", "test ",
		"fix ", "implement ", "build ", "update ", "commit ", "use ",
		"publish ", "deploy ", "generate ", "send ", "research ", "search ", "fetch ",
		"file", "repo", "repository", "project", "code",
		"web", "http", "https",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func hasHighRiskTool(tools []string) bool {
	for _, t := range tools {
		if highRiskTools[strings.TrimSpace(t)] {
			return true
		}
	}
	return false
}

func hasHighRiskPlanItem(items []session.TaskPlanItem) bool {
	for _, item := range items {
		if highRiskTools[strings.TrimSpace(item.Tool)] {
			return true
		}
	}
	return false
}

func needsExternalPublish(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{
		"feishu", "lark", "cloud doc", "send to", "publish", "deploy",
		"publish to", "post to",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func explicitReviewRequest(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{
		"plan first", "先计划", "make a plan", "create a plan",
		"让我确认", "let me confirm", "confirm first",
		"先确认", "show me the plan", "show plan",
		"review first",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func isMultiStepDeliveryTask(contract session.TaskContract, text string) bool {
	if len(contract.RequiredTools) <= 1 && len(contract.PlanItems) <= 1 {
		return false
	}

	hasWrite := false
	hasExternal := false
	for _, t := range contract.RequiredTools {
		tool := strings.TrimSpace(t)
		if tool == "file.write" || tool == "file.edit" {
			hasWrite = true
		}
		if tool == "terminal.run" {
			if strings.Contains(text, "publish") || strings.Contains(text, "deploy") || strings.Contains(text, "send") || strings.Contains(text, "发布") {
				hasExternal = true
			}
		}
	}

	return hasWrite && hasExternal
}

// classifyContractStrategyFromGoalAndText provides a standalone entry point
// for testing the strategy classifier directly.
func classifyContractStrategyFromGoalAndText(goal, userText string) contractStrategy {
	contract := fallbackTaskContract(goal, userText)
	return classifyContractStrategy(goal, userText, contract)
}
