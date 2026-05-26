package runtime

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/skill"
	"github.com/dongping/mateway/internal/tool"
)

type AcceptanceStatus string

const (
	AcceptancePass     AcceptanceStatus = "pass"
	AcceptanceUsable   AcceptanceStatus = "usable"
	AcceptanceHardFail AcceptanceStatus = "hard_fail"
	AcceptanceSuspect  AcceptanceStatus = "suspect"
	AcceptanceAccepted AcceptanceStatus = "accepted"
	AcceptancePartial  AcceptanceStatus = "partial"
	AcceptanceRejected AcceptanceStatus = "rejected"
)

type StepAcceptance struct {
	Status   AcceptanceStatus
	Reason   string
	Warnings []string
	Source   string
}

type FinalAcceptance struct {
	Status  AcceptanceStatus
	Reason  string
	Missing []string
}

type finalAcceptancePayload struct {
	Status  string   `json:"status"`
	Reason  string   `json:"reason"`
	Missing []string `json:"missing,omitempty"`
}

type stepAcceptancePayload struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func codeAcceptStep(step model.PlanStep, result model.ToolResult, def tool.Definition, registry *AcceptanceRegistry) StepAcceptance {
	if !result.OK {
		if strings.TrimSpace(step.Tool) == "memory.search" && memorySearchNoMatch(result) {
			return StepAcceptance{Status: AcceptanceUsable, Reason: "memory search returned no durable match but produced a valid no-result observation", Source: "code"}
		}
		return StepAcceptance{Status: AcceptanceHardFail, Reason: firstNonEmpty(result.Error, "tool execution failed"), Source: "code"}
	}
	if verification := verifyStepResult(step, result); verification.Blocking() {
		return StepAcceptance{Status: AcceptanceHardFail, Reason: strings.Join(verification.Errors, "; "), Warnings: verification.Warnings, Source: "code"}
	}
	if spec, ok := acceptanceSpecForStep(registry, step, def); ok {
		if accept := codeAcceptWithSpec(spec, result); accept != nil {
			return *accept
		}
	}
	if strings.TrimSpace(result.Output) == "" {
		return StepAcceptance{Status: AcceptanceHardFail, Reason: "tool returned empty output", Source: "code"}
	}
	text := strings.ToLower(strings.TrimSpace(result.Output))
	extraSignals := def.Metadata.SoftFailureSignals
	if spec, ok := acceptanceSpecForStep(registry, step, def); ok {
		extraSignals = append(extraSignals, spec.SoftFailureSignals...)
	}
	if softFailureReason(text, result.Evidence, extraSignals) != "" {
		return StepAcceptance{Status: AcceptanceSuspect, Reason: softFailureReason(text, result.Evidence, extraSignals), Source: "code"}
	}
	return StepAcceptance{Status: AcceptancePass, Source: "code"}
}

func memorySearchNoMatch(result model.ToolResult) bool {
	if strings.TrimSpace(result.Tool) != "memory.search" {
		return false
	}
	if count, ok := result.Evidence["result_count"].(int); ok && count == 0 {
		return true
	}
	if count, ok := result.Evidence["result_count"].(float64); ok && int(count) == 0 {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(result.Output))
	return strings.Contains(text, "no matching long memory found")
}

func codeAcceptWithSpec(spec AcceptanceSpec, result model.ToolResult) *StepAcceptance {
	for _, check := range spec.CodeChecks {
		switch strings.TrimSpace(strings.ToLower(check)) {
		case "output must not be empty":
			if strings.TrimSpace(result.Output) == "" {
				accept := StepAcceptance{Status: AcceptanceHardFail, Reason: "tool returned empty output", Source: "code"}
				return &accept
			}
		case "evidence must include file path", "evidence must include target file path":
			if !evidenceHasAny(result.Evidence, "path", "target_path") {
				accept := StepAcceptance{Status: AcceptanceHardFail, Reason: "missing file path evidence", Source: "code"}
				return &accept
			}
		case "evidence should include preview or headings context":
			if !evidenceHasAny(result.Evidence, "headings", "start_line", "end_line") {
				accept := StepAcceptance{Status: AcceptanceUsable, Reason: "missing preview or headings evidence", Source: "code"}
				return &accept
			}
		case "evidence should include line range context":
			if !evidenceHasAny(result.Evidence, "start_line", "end_line") {
				accept := StepAcceptance{Status: AcceptanceUsable, Reason: "missing line range evidence", Source: "code"}
				return &accept
			}
		case "evidence should include bytes written":
			if !evidenceHasAny(result.Evidence, "bytes") {
				accept := StepAcceptance{Status: AcceptanceUsable, Reason: "missing bytes written evidence", Source: "code"}
				return &accept
			}
		case "evidence should include project counts":
			if !evidenceHasAny(result.Evidence, "directory_count", "file_count") {
				accept := StepAcceptance{Status: AcceptanceUsable, Reason: "missing project structure count evidence", Source: "code"}
				return &accept
			}
		case "evidence should include entry count":
			if !evidenceHasAny(result.Evidence, "entry_count") {
				accept := StepAcceptance{Status: AcceptanceUsable, Reason: "missing entry count evidence", Source: "code"}
				return &accept
			}
		case "patch output should mention the modified file":
			if path, _ := result.Evidence["path"].(string); strings.TrimSpace(path) != "" && !strings.Contains(result.Output, path) {
				accept := StepAcceptance{Status: AcceptanceSuspect, Reason: "patch output does not mention modified file path", Source: "code"}
				return &accept
			}
		case "evidence should include query, provider, and result count":
			if !evidenceHasAny(result.Evidence, "query", "provider", "result_count") {
				accept := StepAcceptance{Status: AcceptanceHardFail, Reason: "missing web search execution evidence", Source: "code"}
				return &accept
			}
		case "evidence should include query and result count":
			if !evidenceHasAny(result.Evidence, "query", "result_count") {
				accept := StepAcceptance{Status: AcceptanceHardFail, Reason: "missing skill search evidence", Source: "code"}
				return &accept
			}
		case "evidence should include skill name, target path, and install source":
			if !evidenceHasAll(result.Evidence, "name", "target_path", "source") {
				accept := StepAcceptance{Status: AcceptanceHardFail, Reason: "missing skill install evidence", Source: "code"}
				return &accept
			}
		case "evidence should include task count":
			if !evidenceHasAny(result.Evidence, "task_count") {
				accept := StepAcceptance{Status: AcceptanceUsable, Reason: "missing task count evidence", Source: "code"}
				return &accept
			}
		case "evidence should include task id, status, and path":
			if !evidenceHasAll(result.Evidence, "task_id", "status", "path") {
				accept := StepAcceptance{Status: AcceptanceHardFail, Reason: "missing schedule task evidence", Source: "code"}
				return &accept
			}
		case "evidence should include task id, status, schedule, and path":
			if !evidenceHasAll(result.Evidence, "task_id", "status", "schedule", "path") {
				accept := StepAcceptance{Status: AcceptanceHardFail, Reason: "missing schedule task evidence", Source: "code"}
				return &accept
			}
		case "evidence should include task id and path":
			if !evidenceHasAll(result.Evidence, "task_id", "path") {
				accept := StepAcceptance{Status: AcceptanceHardFail, Reason: "missing delete evidence", Source: "code"}
				return &accept
			}
		case "evidence should include exit code, stdout, stderr, and timed_out":
			if !evidenceHasAny(result.Evidence, "exit_code", "stdout", "stderr", "timed_out") {
				accept := StepAcceptance{Status: AcceptanceHardFail, Reason: "missing terminal execution evidence", Source: "code"}
				return &accept
			}
		case "evidence should include install command, verify command, and verified status":
			if !evidenceHasAny(result.Evidence, "command", "verify_command", "verified") {
				accept := StepAcceptance{Status: AcceptanceHardFail, Reason: "missing software install verification evidence", Source: "code"}
				return &accept
			}
		}
	}
	return nil
}

func shouldLLMAcceptStep(step model.PlanStep, def tool.Definition, accept StepAcceptance, skills []skill.Definition) bool {
	if accept.Status == AcceptanceHardFail {
		return false
	}
	if accept.Status == AcceptanceSuspect {
		return true
	}
	if def.Metadata.AcceptanceMode == tool.AcceptanceLLM {
		return true
	}
	if len(skills) > 0 {
		return true
	}
	return false
}

func shouldLLMAcceptFinal(plan model.Plan, results []model.ToolResult, understanding taskUnderstanding, repairAttempted bool, registry *tool.Registry) bool {
	if len(results) == 0 {
		return false
	}
	if repairAttempted || anyFailed(results) {
		return true
	}
	if strings.TrimSpace(understanding.RiskLevel) == "guarded_mutation" || strings.TrimSpace(understanding.RiskLevel) == "dangerous_execute" || understanding.NeedsMutation {
		return true
	}
	for _, step := range plan.Steps {
		if step.RequiresConfirm {
			return true
		}
		if step.Risk == string(tool.RiskGuardedMutation) || step.Risk == string(tool.RiskDangerous) {
			return true
		}
		if registry != nil {
			if def, ok := registry.Get(step.Tool); ok {
				if def.Risk == tool.RiskGuardedMutation || def.Risk == tool.RiskDangerous || def.Metadata.AcceptanceMode == tool.AcceptanceLLM {
					return true
				}
			}
		}
	}
	return false
}

func softFailureReason(output string, evidence map[string]any, extra []string) string {
	for _, cue := range append(defaultSoftFailureSignals(), extra...) {
		cue = strings.ToLower(strings.TrimSpace(cue))
		if cue != "" && strings.Contains(output, cue) {
			return "soft failure signal: " + cue
		}
	}
	if timedOut, _ := evidence["timed_out"].(bool); timedOut {
		return "command timed out"
	}
	return ""
}

func defaultSoftFailureSignals() []string {
	return []string{
		"not found",
		"data not found",
		"no results",
		"permission denied",
		"unauthorized",
		"timed out",
	}
}

func llmAcceptStep(ctx context.Context, planner model.Planner, user string, step model.PlanStep, result model.ToolResult, def tool.Definition, registry *AcceptanceRegistry) StepAcceptance {
	reviewer, ok := planner.(interface {
		AcceptStepJSON(context.Context, string, model.PlanStep, model.ToolResult) (string, error)
	})
	if !ok {
		return StepAcceptance{Status: AcceptancePass, Source: "fallback"}
	}
	raw, err := reviewer.AcceptStepJSON(ctx, buildStepAcceptancePrompt(user, step, def, registry), step, result)
	if err != nil {
		return StepAcceptance{Status: AcceptanceSuspect, Reason: err.Error(), Source: "llm"}
	}
	var payload stepAcceptancePayload
	if json.Unmarshal([]byte(extractAcceptanceJSONObject(raw)), &payload) != nil {
		return StepAcceptance{Status: AcceptanceSuspect, Reason: "step acceptance parse failed", Source: "llm"}
	}
	switch strings.TrimSpace(payload.Status) {
	case string(AcceptancePass):
		return StepAcceptance{Status: AcceptancePass, Reason: payload.Reason, Source: "llm"}
	case string(AcceptanceHardFail):
		return StepAcceptance{Status: AcceptanceHardFail, Reason: firstNonEmpty(payload.Reason, "llm rejected step"), Source: "llm"}
	default:
		return StepAcceptance{Status: AcceptanceSuspect, Reason: firstNonEmpty(payload.Reason, "llm marked step as suspect"), Source: "llm"}
	}
}

func buildStepAcceptanceTask(user string, step model.PlanStep, def tool.Definition, registry *AcceptanceRegistry) string {
	parts := []string{
		"User task: " + strings.TrimSpace(user),
		"Step goal: " + strings.TrimSpace(step.Goal),
	}
	if spec, ok := acceptanceSpecForStep(registry, step, def); ok {
		if len(spec.CodeChecks) > 0 {
			parts = append(parts, "Tool code checks: "+strings.Join(spec.CodeChecks, " | "))
		}
		if len(spec.PassCriteria) > 0 {
			parts = append(parts, "Pass criteria: "+strings.Join(spec.PassCriteria, " | "))
		}
		if len(spec.SuspectCriteria) > 0 {
			parts = append(parts, "Suspect criteria: "+strings.Join(spec.SuspectCriteria, " | "))
		}
		if len(spec.FailCriteria) > 0 {
			parts = append(parts, "Fail criteria: "+strings.Join(spec.FailCriteria, " | "))
		}
		if strings.TrimSpace(spec.LLMReviewPrompt) != "" {
			parts = append(parts, "Tool acceptance prompt: "+spec.LLMReviewPrompt)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func buildStepAcceptancePrompt(user string, step model.PlanStep, def tool.Definition, registry *AcceptanceRegistry) string {
	contextPrompt := buildModelContextPrompt("", promptStageStepAcceptance, nil, nil, tool.Context{}, promptContextOptions{})
	taskPrompt := buildStepAcceptanceTask(user, step, def, registry)
	return strings.TrimSpace(contextPrompt + "\n\nAcceptance task:\n" + taskPrompt)
}

func derivedAcceptanceSpecRef(step model.PlanStep, def tool.Definition) string {
	switch def.Name {
	case "terminal.run":
		command := strings.ToLower(strings.TrimSpace(step.Args["command"]))
		goal := strings.ToLower(strings.TrimSpace(step.Goal))
		switch {
		case strings.Contains(command, "go test") || strings.Contains(command, "pytest") || strings.Contains(goal, "test"):
			return "terminal.run/test"
		case strings.Contains(command, "go build") || strings.Contains(command, "npm run build") || strings.Contains(goal, "build"):
			return "terminal.run/build"
		default:
			return "terminal.run/diagnostic"
		}
	case "file.patch":
		if strings.TrimSpace(step.Args["append"]) != "" {
			return "file.patch/append"
		}
		return "file.patch/replace"
	case "web.search":
		text := strings.ToLower(strings.TrimSpace(step.Goal + " " + strings.Join(step.ExpectedEvidence, " ")))
		if strings.Contains(text, "latest") || strings.Contains(text, "current") || strings.Contains(text, "today") || strings.Contains(text, "最新") {
			return "web.search/fresh_info"
		}
		return "web.search/background_info"
	default:
		return ""
	}
}

func llmAcceptFinal(ctx context.Context, planner model.Planner, user string, understanding taskUnderstanding, plan model.Plan, results []model.ToolResult) FinalAcceptance {
	reviewer, ok := planner.(interface {
		AcceptFinalJSON(context.Context, string, model.Plan, []model.ToolResult) (string, error)
	})
	if !ok {
		if anyFailed(results) {
			return FinalAcceptance{Status: AcceptanceRejected, Reason: "some steps failed"}
		}
		return FinalAcceptance{Status: AcceptanceAccepted, Reason: "all steps completed"}
	}
	user = buildFinalAcceptancePrompt(user, understanding)
	raw, err := reviewer.AcceptFinalJSON(ctx, user, plan, results)
	if err != nil {
		if anyFailed(results) {
			return FinalAcceptance{Status: AcceptanceRejected, Reason: err.Error()}
		}
		return FinalAcceptance{Status: AcceptancePartial, Reason: err.Error()}
	}
	var payload finalAcceptancePayload
	if json.Unmarshal([]byte(extractAcceptanceJSONObject(raw)), &payload) != nil {
		if anyFailed(results) {
			return FinalAcceptance{Status: AcceptanceRejected, Reason: "final acceptance parse failed"}
		}
		return FinalAcceptance{Status: AcceptancePartial, Reason: "final acceptance parse failed"}
	}
	switch strings.TrimSpace(payload.Status) {
	case string(AcceptanceAccepted):
		return FinalAcceptance{Status: AcceptanceAccepted, Reason: payload.Reason, Missing: payload.Missing}
	case string(AcceptanceRejected):
		return FinalAcceptance{Status: AcceptanceRejected, Reason: firstNonEmpty(payload.Reason, "final acceptance rejected"), Missing: payload.Missing}
	default:
		return FinalAcceptance{Status: AcceptancePartial, Reason: firstNonEmpty(payload.Reason, "partially accepted"), Missing: payload.Missing}
	}
}

func buildFinalAcceptanceTask(user string, understanding taskUnderstanding) string {
	parts := []string{"User task: " + strings.TrimSpace(user)}
	if len(understanding.CompletionDraft) > 0 {
		parts = append(parts, "Completion criteria: "+strings.Join(understanding.CompletionDraft, " | "))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func buildFinalAcceptancePrompt(user string, understanding taskUnderstanding) string {
	contextPrompt := buildModelContextPrompt("", promptStageFinalAcceptance, nil, nil, tool.Context{}, promptContextOptions{
		Understanding: understanding,
	})
	taskPrompt := buildFinalAcceptanceTask(user, understanding)
	return strings.TrimSpace(contextPrompt + "\n\nAcceptance task:\n" + taskPrompt)
}

func extractAcceptanceJSONObject(text string) string {
	text = strings.TrimSpace(text)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		return text[start : end+1]
	}
	return text
}

func evidenceHasAll(evidence map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := evidence[key]; !ok {
			return false
		}
	}
	return true
}
