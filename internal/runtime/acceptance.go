package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
	var spec AcceptanceSpec
	var hasSpec bool
	if registry != nil {
		spec, hasSpec = acceptanceSpecForStep(registry, step, def)
	}
	if !result.OK {
		if strings.TrimSpace(step.Tool) == "memory.search" && memorySearchNoMatch(result) {
			return StepAcceptance{Status: AcceptanceUsable, Reason: "memory search returned no durable match but produced a valid no-result observation", Source: "code"}
		}
		if terminalDiagnosticFailureIsUsable(step, result, def, registry) {
			return StepAcceptance{Status: AcceptanceUsable, Reason: "terminal diagnostic returned usable stdout despite non-zero exit", Source: "code"}
		}
		return StepAcceptance{Status: AcceptanceHardFail, Reason: firstNonEmpty(result.Error, "tool execution failed"), Source: "code"}
	}
	if verification := verifyStepResult(step, result); verification.Blocking() {
		return StepAcceptance{Status: AcceptanceHardFail, Reason: strings.Join(verification.Errors, "; "), Warnings: verification.Warnings, Source: "code"}
	}
	if softwareSearchResultLooksWeak(step, result) {
		return StepAcceptance{Status: AcceptanceSuspect, Reason: "software search top result does not clearly match the requested software", Source: "code"}
	}
	if hasSpec {
		if accept := codeAcceptWithSpec(spec, result); accept != nil {
			return *accept
		}
	}
	if strings.TrimSpace(result.Output) == "" {
		return StepAcceptance{Status: AcceptanceHardFail, Reason: "tool returned empty output", Source: "code"}
	}
	text := strings.ToLower(strings.TrimSpace(result.Output))
	extraSignals := def.Metadata.SoftFailureSignals
	if hasSpec {
		extraSignals = append(extraSignals, spec.SoftFailureSignals...)
	}
	if reason := softFailureReason(text, result.Evidence, extraSignals); reason != "" {
		if hasSpec && explicitNoResultIsUsable(spec, result) {
			return StepAcceptance{Status: AcceptanceUsable, Reason: "tool returned a valid explicit no-result outcome", Source: "code"}
		}
		return StepAcceptance{Status: AcceptanceSuspect, Reason: reason, Source: "code"}
	}
	return StepAcceptance{Status: AcceptancePass, Source: "code"}
}

func terminalDiagnosticFailureIsUsable(step model.PlanStep, result model.ToolResult, def tool.Definition, registry *AcceptanceRegistry) bool {
	if strings.TrimSpace(step.Tool) != "terminal.run" && strings.TrimSpace(def.Name) != "terminal.run" {
		return false
	}
	if derivedAcceptanceSpecRef(step, def) != "terminal.run/diagnostic" {
		return false
	}
	if timedOut, _ := result.Evidence["timed_out"].(bool); timedOut {
		return false
	}
	exitCode := 0
	switch v := result.Evidence["exit_code"].(type) {
	case int:
		exitCode = v
	case float64:
		exitCode = int(v)
	default:
		return false
	}
	if exitCode == 0 {
		return false
	}
	stdout, _ := result.Evidence["stdout"].(string)
	if strings.TrimSpace(stdout) == "" {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(result.Output))
	extraSignals := def.Metadata.SoftFailureSignals
	if spec, ok := acceptanceSpecForStep(registry, step, def); ok {
		extraSignals = append(extraSignals, spec.SoftFailureSignals...)
	}
	return softFailureReason(text, result.Evidence, extraSignals) == ""
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

func softwareSearchResultLooksWeak(step model.PlanStep, result model.ToolResult) bool {
	if strings.TrimSpace(step.Tool) != "software.search" || strings.TrimSpace(result.Tool) != "software.search" {
		return false
	}
	count, ok := evidenceInt(result.Evidence, "result_count")
	if ok && count == 0 {
		return false
	}
	query := strings.TrimSpace(firstNonEmpty(stringValue(result.Evidence["query"]), step.Args["query"], step.Args["q"]))
	name := strings.TrimSpace(stringValue(result.Evidence["name"]))
	if query == "" || name == "" {
		return false
	}
	queryTokens := stableSearchNameTokens(query)
	nameTokens := stableSearchNameTokens(name)
	if len(queryTokens) == 0 || len(nameTokens) == 0 {
		return false
	}
	matches := 0
	for _, token := range queryTokens {
		if token == "" {
			continue
		}
		for _, actual := range nameTokens {
			if actual == token {
				matches++
				break
			}
		}
	}
	if len(queryTokens) >= 2 && matches < 2 {
		return true
	}
	if len(queryTokens) == 1 && matches == 0 {
		return true
	}
	return false
}

func stableSearchNameTokens(text string) []string {
	text = strings.ToLower(strings.TrimSpace(text))
	replacer := strings.NewReplacer("/", " ", "-", " ", "_", " ", ".", " ", ":", " ")
	text = replacer.Replace(text)
	raw := strings.Fields(text)
	out := make([]string, 0, len(raw))
	for _, token := range raw {
		token = strings.TrimSpace(token)
		if len([]rune(token)) < 3 {
			continue
		}
		if token == "install" || token == "package" || token == "tool" || token == "official" || token == "github" || token == "repo" || token == "repository" || token == "npm" || token == "cli" {
			if token != "cli" {
				continue
			}
		}
		if strings.HasSuffix(token, "s") && len(token) > 4 {
			token = strings.TrimSuffix(token, "s")
		}
		out = append(out, token)
	}
	return out
}

func codeAcceptWithSpec(spec AcceptanceSpec, result model.ToolResult) *StepAcceptance {
	for _, rule := range spec.EvidenceRules {
		if acceptanceEvidenceRuleSatisfied(rule, result.Evidence) {
			continue
		}
		accept := StepAcceptance{
			Status: firstNonEmptyAcceptanceStatus(rule.Status, AcceptanceHardFail),
			Reason: firstNonEmpty(rule.Reason, "missing required evidence"),
			Source: "code",
		}
		return &accept
	}
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

func acceptanceEvidenceRuleSatisfied(rule AcceptanceEvidenceRule, evidence map[string]any) bool {
	match := strings.ToLower(strings.TrimSpace(rule.Match))
	switch match {
	case "all":
		return evidenceHasAll(evidence, rule.Keys...)
	case "any":
		return evidenceHasAny(evidence, rule.Keys...)
	default:
		return evidenceHasAll(evidence, rule.Keys...)
	}
}

func explicitNoResultIsUsable(spec AcceptanceSpec, result model.ToolResult) bool {
	if !spec.AllowExplicitNoResult {
		return false
	}
	if strings.TrimSpace(result.Output) == "" {
		return false
	}
	count, ok := evidenceInt(result.Evidence, "result_count")
	return ok && count == 0
}

func evidenceInt(evidence map[string]any, key string) (int, bool) {
	if evidence == nil {
		return 0, false
	}
	switch v := evidence[key].(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

func firstNonEmptyAcceptanceStatus(primary, fallback AcceptanceStatus) AcceptanceStatus {
	if strings.TrimSpace(string(primary)) != "" {
		return primary
	}
	return fallback
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
	user = buildFinalAcceptancePrompt(user, understanding, finalAcceptanceContext{
		Results:       results,
		ScheduledRun:  understanding.IsScheduledRun,
	})
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

type finalAcceptanceContext struct {
	Results      []model.ToolResult
	ScheduledRun bool
}

func buildFinalAcceptanceTask(user string, understanding taskUnderstanding, ctx ...finalAcceptanceContext) string {
	parts := []string{"User task: " + strings.TrimSpace(user)}
	var extra finalAcceptanceContext
	if len(ctx) > 0 {
		extra = ctx[0]
	}
	if len(understanding.CompletionDraft) > 0 {
		parts = append(parts, "Completion criteria: "+strings.Join(understanding.CompletionDraft, " | "))
	}
	if extra.ScheduledRun {
		parts = append(parts, "Scheduled run context: This is an already-triggered schedule execution. Judge whether the requested task work completed this run; do not reinterpret success as schedule creation or schedule update success.")
	}
	if summary := renderFinalAcceptanceExecutionSummary(extra.Results); summary != "" {
		parts = append(parts, "Execution summary: "+summary)
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func buildFinalAcceptancePrompt(user string, understanding taskUnderstanding, ctx ...finalAcceptanceContext) string {
	contextPrompt := buildModelContextPrompt("", promptStageFinalAcceptance, nil, nil, tool.Context{}, promptContextOptions{
		Understanding: understanding,
	})
	taskPrompt := buildFinalAcceptanceTask(user, understanding, ctx...)
	return strings.TrimSpace(contextPrompt + "\n\nAcceptance task:\n" + taskPrompt)
}

func renderFinalAcceptanceExecutionSummary(results []model.ToolResult) string {
	if len(results) == 0 {
		return ""
	}
	passed := 0
	failed := 0
	artifacts := 0
	toolNames := map[string]struct{}{}
	for _, result := range results {
		if strings.TrimSpace(result.Tool) != "" {
			toolNames[strings.TrimSpace(result.Tool)] = struct{}{}
		}
		if result.OK {
			passed++
		} else if strings.TrimSpace(result.Error) == "step_acceptance_suspect" || strings.TrimSpace(result.Error) == "step_verification_failed" || strings.TrimSpace(result.Error) == "dependency_failed" || strings.TrimSpace(result.Error) == "await_confirm" {
			failed++
		} else {
			failed++
		}
		if len(collectArtifacts([]model.ToolResult{result})) > 0 {
			artifacts++
		}
	}
	if passed == 0 && failed == 0 {
		return ""
	}
	names := make([]string, 0, len(toolNames))
	for name := range toolNames {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := []string{
		"passed=" + fmt.Sprint(passed),
		"failed=" + fmt.Sprint(failed),
		"artifact_evidence_steps=" + fmt.Sprint(artifacts),
	}
	if len(names) > 0 {
		parts = append(parts, "tools="+strings.Join(names, ","))
	}
	return strings.Join(parts, " | ")
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
