package runtime

import (
	"strings"

	"github.com/dongping/mateway/internal/session"
)

type contractCheckResult struct {
	Satisfied bool
	Reason    string
	FollowUp  string
}

func buildCompletionContract(userText string) session.CompletionContract {
	text := strings.ToLower(strings.TrimSpace(userText))
	contract := session.CompletionContract{}
	if looksLikeSlashMutation(text) {
		contract.RequiredTools = requiredToolsForSlashCommand(text)
		contract.SuccessCondition = "requested command has accepted tool evidence"
		return contract
	}
	if hasExecutionIntentCue(text) {
		contract.RequiredTools = []string{"mutation"}
		contract.SuccessCondition = "requested artifact or action has accepted mutation or verification evidence"
		return contract
	}
	if looksComplexEnoughForLLMReview(text) {
		contract.RequiresLLMReview = true
		contract.SuccessCondition = "final answer should be semantically checked against the user request"
	}
	return contract
}

func looksLikeSlashMutation(text string) bool {
	return strings.HasPrefix(text, "/write ") || strings.HasPrefix(text, "/run ") || strings.HasPrefix(text, "/schedule ")
}

func requiredToolsForSlashCommand(text string) []string {
	switch {
	case strings.HasPrefix(text, "/write "):
		return []string{"file.write"}
	case strings.HasPrefix(text, "/run "):
		return []string{"terminal.run"}
	case strings.HasPrefix(text, "/schedule "):
		return []string{"schedule.create"}
	default:
		return nil
	}
}

func looksComplexEnoughForLLMReview(text string) bool {
	fields := strings.Fields(text)
	return len(fields) >= 18
}

func checkCompletionContract(task session.TaskNode) contractCheckResult {
	contract := task.CompletionContract
	if len(contract.RequiredTools) == 0 {
		return contractCheckResult{Satisfied: true}
	}
	for _, required := range contract.RequiredTools {
		if required == "mutation" {
			if !taskHasNoAcceptedMutationEvidence(task) {
				continue
			}
			return contractCheckResult{
				Reason:   "missing accepted mutation evidence",
				FollowUp: "Continue now. Produce accepted mutation or verification evidence for the task before giving a final answer.",
			}
		}
		if taskHasAcceptedTool(task, required) {
			continue
		}
		return contractCheckResult{
			Reason:   "missing accepted tool evidence: " + required,
			FollowUp: "Continue now. Use " + required + " or explain the concrete blocker if it cannot be used.",
		}
	}
	return contractCheckResult{Satisfied: true}
}

func hasExecutionIntentCue(text string) bool {
	cues := map[string]bool{
		"create": true, "write": true, "modify": true, "edit": true, "fix": true,
		"test": true, "tests": true, "verify": true, "install": true, "send": true,
		"publish": true, "deploy": true, "script": true, "scripts": true, "skill": true,
		"skills": true,
	}
	for _, field := range strings.Fields(strings.ToLower(text)) {
		if strings.Contains(field, "/") || strings.Contains(field, "\\") {
			continue
		}
		field = strings.Trim(field, ".,:;!?()[]{}\"'`*_")
		if cues[field] {
			return true
		}
	}
	return false
}

func taskHasNoAcceptedMutationEvidence(task session.TaskNode) bool {
	for _, step := range task.Steps {
		if step.Status == "accepted" && isMutationEvidenceTool(step.Tool) {
			return false
		}
	}
	return true
}

func isMutationEvidenceTool(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "file.write", "script.run", "terminal.run", "secret.set", "schedule.create":
		return true
	default:
		return false
	}
}

func taskHasAcceptedTool(task session.TaskNode, toolName string) bool {
	for _, step := range task.Steps {
		if step.Status == "accepted" && strings.EqualFold(strings.TrimSpace(step.Tool), strings.TrimSpace(toolName)) {
			return true
		}
	}
	return false
}
