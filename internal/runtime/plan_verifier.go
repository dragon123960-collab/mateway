package runtime

import (
	"fmt"
	"strings"

	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/tool"
)

type PlanVerification struct {
	Warnings           []string
	RepairableWarnings []string
	Errors             []string
}

func (v PlanVerification) Blocking() bool {
	return len(v.Errors) > 0
}

func (v PlanVerification) ShouldRepair() bool {
	return v.Blocking() || len(v.RepairableWarnings) > 0
}

func (v PlanVerification) RepairGuidance() string {
	parts := make([]string, 0, len(v.Errors)+len(v.RepairableWarnings)+len(v.Warnings))
	for _, err := range v.Errors {
		parts = append(parts, "error: "+strings.TrimSpace(err))
	}
	for _, warning := range v.RepairableWarnings {
		parts = append(parts, "repairable_warning: "+strings.TrimSpace(warning))
	}
	for _, warning := range v.Warnings {
		parts = append(parts, "warning: "+strings.TrimSpace(warning))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func VerifyPlanContract(plan model.Plan, registry *tool.Registry, user string) PlanVerification {
	return verifyPlanContract(plan, registry, user, taskUnderstanding{})
}

func verifyPlanContract(plan model.Plan, registry *tool.Registry, user string, understanding taskUnderstanding) PlanVerification {
	var out PlanVerification
	seen := map[string]bool{}
	usedTools := make([]string, 0, len(plan.Steps))
	for i, step := range plan.Steps {
		label := step.ID
		if strings.TrimSpace(label) == "" {
			label = fmt.Sprintf("step-%d", i+1)
		}
		if registry == nil {
			out.Errors = append(out.Errors, label+": tool registry is unavailable")
			continue
		}
		def, ok := registry.Get(step.Tool)
		if !ok {
			out.Errors = append(out.Errors, label+": unknown tool "+step.Tool)
		} else {
			usedTools = append(usedTools, def.Name)
			for _, missing := range missingRequiredArgs(def, step.Args) {
				out.Errors = append(out.Errors, label+": missing required arg "+missing)
			}
		}
		for _, dep := range step.DependsOn {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}
			if !seen[dep] {
				out.Errors = append(out.Errors, label+": dependency "+dep+" does not reference an earlier step")
			}
		}
		if step.Tool != "user.ask" && len(step.ExpectedEvidence) == 0 && requiresStepEvidence(user, step) {
			out.Warnings = append(out.Warnings, label+": expected_evidence is empty")
		}
		if step.Tool != "user.ask" && len(step.SuccessCriteria) == 0 && requiresStepEvidence(user, step) {
			out.Warnings = append(out.Warnings, label+": success_criteria is empty")
		}
		if len(understanding.EvidenceHints) > 0 && !evidenceHintsMatchStep(understanding.EvidenceHints, step) {
			out.RepairableWarnings = append(out.RepairableWarnings, label+": expected_evidence does not clearly align with understanding evidence hints")
		}
		seen[step.ID] = true
	}
	for _, warning := range toolNeedsCoverageWarnings(understanding.Capabilities, usedTools) {
		out.RepairableWarnings = append(out.RepairableWarnings, warning)
	}
	if len(understanding.CompletionDraft) > 0 && !successCriteriaMatchUnderstanding(understanding.CompletionDraft, plan.Steps) {
		out.RepairableWarnings = append(out.RepairableWarnings, "plan success_criteria do not clearly align with understanding completion criteria")
	}
	return out
}

func toolNeedsCoverageWarnings(toolNeeds []string, tools []string) []string {
	var warnings []string
	for _, need := range toolNeeds {
		if toolNeedSatisfiedByTools(need, tools) {
			continue
		}
		warnings = append(warnings, "plan tools do not clearly cover tool_need "+need)
	}
	return warnings
}

func toolNeedSatisfiedByTools(need string, tools []string) bool {
	for _, name := range tools {
		if name == need {
			return true
		}
	}
	return false
}

func evidenceHintsMatchStep(hints []string, step model.PlanStep) bool {
	if len(step.ExpectedEvidence) == 0 {
		return false
	}
	text := strings.ToLower(strings.Join(step.ExpectedEvidence, " "))
	for _, hint := range hints {
		hint = strings.ToLower(strings.TrimSpace(hint))
		switch {
		case strings.Contains(hint, "file path") && (strings.Contains(text, "file") || strings.Contains(text, "path")):
			return true
		case strings.Contains(hint, "line") && strings.Contains(text, "line"):
			return true
		case strings.Contains(hint, "query") && strings.Contains(text, "query"):
			return true
		case strings.Contains(hint, "result count") && strings.Contains(text, "result"):
			return true
		case strings.Contains(hint, "install command") && strings.Contains(text, "install"):
			return true
		case strings.Contains(hint, "verify command") && strings.Contains(text, "verify"):
			return true
		case strings.Contains(hint, "task id") && strings.Contains(text, "task"):
			return true
		case strings.Contains(hint, "exit code") && strings.Contains(text, "exit"):
			return true
		}
	}
	return false
}

func successCriteriaMatchUnderstanding(criteria []string, steps []model.PlanStep) bool {
	if len(criteria) == 0 {
		return true
	}
	var textParts []string
	for _, step := range steps {
		textParts = append(textParts, step.SuccessCriteria...)
		textParts = append(textParts, step.Goal)
	}
	text := strings.ToLower(strings.Join(textParts, " "))
	for _, criterion := range criteria {
		criterion = strings.ToLower(strings.TrimSpace(criterion))
		switch {
		case strings.Contains(criterion, "grounded evidence") && strings.Contains(text, "evidence"):
			return true
		case strings.Contains(criterion, "verify") && strings.Contains(text, "verify"):
			return true
		case strings.Contains(criterion, "summarize") && strings.Contains(text, "summar"):
			return true
		case strings.Contains(criterion, "install") && strings.Contains(text, "install"):
			return true
		case strings.Contains(criterion, "file") && strings.Contains(text, "file"):
			return true
		}
	}
	return false
}

func missingRequiredArgs(def tool.Definition, args map[string]string) []string {
	required := requiredArgsForTool(def.Name)
	var missing []string
	for _, name := range required {
		if strings.TrimSpace(args[name]) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

func requiredArgsForTool(name string) []string {
	switch name {
	case "file.read", "file.summary":
		return []string{"path"}
	case "file.write":
		return []string{"path", "content"}
	case "file.patch":
		return []string{"path"}
	case "shell.run", "terminal.run":
		return []string{"command"}
	case "web.search", "skill.search", "memory.search", "software.search":
		return []string{"query"}
	case "web.fetch":
		return []string{"url"}
	case "skill.install":
		return []string{"name"}
	case "software.install":
		return []string{"command"}
	case "schedule.create":
		return []string{"title", "prompt"}
	case "schedule.show", "schedule.pause", "schedule.resume", "schedule.delete":
		return []string{"id"}
	default:
		return nil
	}
}

func requiresStepEvidence(user string, step model.PlanStep) bool {
	if strings.TrimSpace(step.Tool) == "" {
		return false
	}
	switch step.Tool {
	case "time.now", "config.summary", "user.ask":
		return false
	}
	if strings.Contains(step.Tool, ".") {
		return true
	}
	return requiresGroundingEvidence(user)
}

type StepVerification struct {
	Warnings []string
	Errors   []string
}

func (v StepVerification) Blocking() bool {
	return len(v.Errors) > 0
}

func VerifyStepResult(step model.PlanStep, result model.ToolResult) StepVerification {
	return verifyStepResult(step, result)
}

func verifyStepResult(step model.PlanStep, result model.ToolResult) StepVerification {
	var out StepVerification
	if !result.OK {
		return out
	}
	if strings.TrimSpace(result.Output) == "" {
		out.Errors = append(out.Errors, "tool returned empty output")
	}
	if len(step.ExpectedEvidence) > 0 && !resultHasEvidence(result) {
		out.Errors = append(out.Errors, "expected evidence was not returned")
	}
	if !evidenceMatchesStep(step, result) {
		out.Errors = append(out.Errors, "returned evidence does not match expected evidence")
	}
	return out
}

func resultHasEvidence(result model.ToolResult) bool {
	if len(result.Evidence) == 0 {
		return false
	}
	if kind, _ := result.Evidence["kind"].(string); strings.TrimSpace(kind) != "" {
		return true
	}
	return len(result.Evidence) > 0
}

func evidenceMatchesStep(step model.PlanStep, result model.ToolResult) bool {
	if len(step.ExpectedEvidence) == 0 || len(result.Evidence) == 0 {
		return true
	}
	text := strings.ToLower(strings.Join(step.ExpectedEvidence, " "))
	kind, _ := result.Evidence["kind"].(string)
	switch {
	case strings.Contains(text, "file") || strings.Contains(text, "path") || strings.Contains(text, "line"):
		return evidenceHasAny(result.Evidence, "path", "target_path", "start_line", "end_line")
	case strings.Contains(text, "url") || strings.Contains(text, "search"):
		return evidenceHasAny(result.Evidence, "url", "source_url", "query", "result_count")
	case strings.Contains(text, "install") || strings.Contains(text, "verify"):
		return evidenceHasAny(result.Evidence, "verified", "verify_command", "install_url", "target_path")
	case strings.Contains(text, "task") || strings.Contains(text, "schedule"):
		return evidenceHasAny(result.Evidence, "task_id", "id", "path")
	default:
		return strings.TrimSpace(kind) != ""
	}
}

func evidenceHasAny(evidence map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := evidence[key]; ok {
			return true
		}
	}
	return false
}
