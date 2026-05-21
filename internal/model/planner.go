package model

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/dongping/mateway/internal/tool"
)

type Plan struct {
	Summary string     `json:"summary"`
	Steps   []PlanStep `json:"steps"`
}

type PlanStep struct {
	ID               string   `json:"id"`
	Goal             string   `json:"goal"`
	Tool             string   `json:"tool"`
	Args             Args     `json:"args"`
	Risk             string   `json:"risk"`
	RequiresConfirm  bool     `json:"requires_confirm"`
	ExpectedEvidence []string `json:"expected_evidence"`
}

type PlanCheckResult struct {
	Plan     Plan
	Fixed    bool
	Warnings []string
	Raw      string
}

type Args map[string]string

func (a *Args) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || strings.TrimSpace(string(data)) == "" {
		*a = Args{}
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := Args{}
	for key, value := range raw {
		switch v := value.(type) {
		case string:
			out[key] = v
		case float64:
			if v == float64(int64(v)) {
				out[key] = fmt.Sprintf("%d", int64(v))
			} else {
				out[key] = fmt.Sprintf("%g", v)
			}
		case bool:
			if v {
				out[key] = "true"
			} else {
				out[key] = "false"
			}
		default:
			b, _ := json.Marshal(v)
			out[key] = string(b)
		}
	}
	*a = out
	return nil
}

type ToolResult struct {
	StepID   string         `json:"step_id"`
	Tool     string         `json:"tool"`
	OK       bool           `json:"ok"`
	Output   string         `json:"output"`
	Evidence map[string]any `json:"evidence,omitempty"`
	Error    string         `json:"error,omitempty"`
}

type FollowupDecision struct {
	Kind          string  `json:"kind"`
	TargetTaskID  string  `json:"target_task_id,omitempty"`
	SourceTaskID  string  `json:"source_task_id,omitempty"`
	ResolvedQuery string  `json:"resolved_query,omitempty"`
	Reason        string  `json:"reason,omitempty"`
	Confidence    float64 `json:"confidence,omitempty"`
}

type Planner interface {
	PlanJSON(ctx context.Context, user string, tools []tool.Definition, skillPrompt string) (Plan, error)
	RepairPlanJSON(ctx context.Context, user string, plan Plan, results []ToolResult, tools []tool.Definition, skillPrompt string) (Plan, error)
	Synthesize(ctx context.Context, user string, plan Plan, results []ToolResult, skillPrompt string) (string, error)
	ResolveFollowupJSON(ctx context.Context, prompt string) (FollowupDecision, error)
}

func (c Client) PlanJSON(ctx context.Context, user string, tools []tool.Definition, skillPrompt string) (Plan, error) {
	result, err := c.PlanCheckedJSON(ctx, user, tools, skillPrompt)
	if err != nil {
		return Plan{}, err
	}
	return result.Plan, nil
}

func (c Client) PlanCheckedJSON(ctx context.Context, user string, tools []tool.Definition, skillPrompt string) (PlanCheckResult, error) {
	prompt := "User task:\n" + user + "\n\nAvailable tools:\n" + toolListForPrompt(tools)
	if strings.TrimSpace(skillPrompt) != "" {
		prompt += "\n\n" + skillPrompt
	}
	prompt += "\n\nReturn the JSON plan now."
	text, err := c.Generate(ctx, plannerSystemPrompt(skillPrompt), []Message{{Role: "user", Content: prompt}})
	if err != nil {
		return PlanCheckResult{}, err
	}
	return CheckAndRepairPlanJSON(text)
}

func (c Client) RepairPlanJSON(ctx context.Context, user string, plan Plan, results []ToolResult, tools []tool.Definition, skillPrompt string) (Plan, error) {
	result, err := c.RepairPlanCheckedJSON(ctx, user, plan, results, tools, skillPrompt)
	if err != nil {
		return Plan{}, err
	}
	return result.Plan, nil
}

func (c Client) RepairPlanCheckedJSON(ctx context.Context, user string, plan Plan, results []ToolResult, tools []tool.Definition, skillPrompt string) (PlanCheckResult, error) {
	planData, _ := json.MarshalIndent(plan, "", "  ")
	resultData, _ := json.MarshalIndent(results, "", "  ")
	prompt := "User task:\n" + user + "\n\nPrevious plan:\n" + string(planData) + "\n\nTool results/errors:\n" + string(resultData) + "\n\nAvailable tools:\n" + toolListForPrompt(tools)
	if strings.TrimSpace(skillPrompt) != "" {
		prompt += "\n\n" + skillPrompt
	}
	prompt += "\n\nReturn a corrected JSON plan. Keep already successful work in mind and only plan the remaining necessary steps."
	text, err := c.Generate(ctx, plannerSystemPrompt(skillPrompt), []Message{{Role: "user", Content: prompt}})
	if err != nil {
		return PlanCheckResult{}, err
	}
	return CheckAndRepairPlanJSON(text)
}

func (c Client) Synthesize(ctx context.Context, user string, plan Plan, results []ToolResult, skillPrompt string) (string, error) {
	planData, _ := json.MarshalIndent(plan, "", "  ")
	resultData, _ := json.MarshalIndent(results, "", "  ")
	systemParts := []string{
		"You are Mateway, a concise tool-using assistant.",
		"Answer the user from the supplied tool results.",
		"Do not dump raw tool JSON. Summarize evidence clearly.",
		"If execution stopped for confirmation, explain what is waiting and why.",
	}
	if strings.TrimSpace(skillPrompt) != "" {
		systemParts = append(systemParts, skillPrompt)
	}
	system := strings.Join(systemParts, "\n")
	prompt := "User task:\n" + user + "\n\nPlan:\n" + string(planData) + "\n\nTool results:\n" + string(resultData)
	return c.Generate(ctx, system, []Message{{Role: "user", Content: prompt}})
}

func plannerSystemPrompt(skillPrompt string) string {
	lines := []string{
		"You are the Mateway planner.",
		"Return ONLY strict JSON. No markdown. No code fences.",
		"The JSON schema is:",
		`{"summary":"short summary","steps":[{"id":"step-1","goal":"what to do","tool":"tool.name","args":{"key":"value"},"risk":"safe_read|guarded_mutation|dangerous_execute","requires_confirm":false,"expected_evidence":["evidence"]}]}`,
		"Use at most 6 steps.",
		"Use only available tool names.",
		"For current time/date use time.now.",
		"For configuration summary use config.summary.",
		"For web/current information use web.search.",
		"For finding public software, CLI tools, GitHub repositories, install commands, or whether a tool can be used, use software.search before generic web.search.",
		"For finding installable agent skills use skill.search. Prefer skills.sh, skillhub.cn, and clawhub.ai through that tool.",
		"For installing agent skills use skill.install and set requires_confirm=true. Do not use shell.run or local file scans to find or install skills.",
		"For reading files use file.read.",
		"For project overview, repository map, package distribution, or file tree use project.index instead of shell.run.",
		"For summarizing one file use file.summary before falling back to file.read.",
		"For writing or editing files use file.write or file.patch and set requires_confirm=true unless the user explicitly confirmed.",
		"For local commands use shell.run. Set requires_confirm=true only for dangerous or mutating commands; safe read-only commands such as pwd, ls, git status, go test, and find-like inspection should use requires_confirm=false.",
		"When information is missing, use user.ask.",
	}
	if strings.TrimSpace(skillPrompt) != "" {
		lines = append(lines, skillPrompt)
	}
	return strings.Join(lines, "\n")
}

func toolListForPrompt(tools []tool.Definition) string {
	var b strings.Builder
	for _, def := range tools {
		fmt.Fprintf(&b, "- %s: %s risk=%s args=%v\n", def.Name, def.Description, def.Risk, def.ArgsSchema)
	}
	return b.String()
}

var fencedJSON = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)\\s*```")

func parsePlan(text string) (Plan, error) {
	result, err := CheckAndRepairPlanJSON(text)
	if err != nil {
		return Plan{}, err
	}
	return result.Plan, nil
}

func CheckAndRepairPlanJSON(raw string) (PlanCheckResult, error) {
	original := raw
	text := extractJSONObject(raw)
	result := PlanCheckResult{Raw: original}
	var plan Plan
	if err := json.Unmarshal([]byte(text), &plan); err != nil {
		repaired, warnings, repairErr := repairPlanJSONText(text)
		if repairErr != nil {
			return result, fmt.Errorf("parse plan json: %w; repair failed: %v; text=%s", err, repairErr, text)
		}
		if repairErr := json.Unmarshal([]byte(repaired), &plan); repairErr != nil {
			return result, fmt.Errorf("parse plan json: %w; repair produced invalid json: %v; text=%s", err, repairErr, text)
		}
		result.Fixed = true
		result.Warnings = append(result.Warnings, warnings...)
	}
	plan, warnings := normalizePlan(plan)
	result.Plan = plan
	result.Warnings = append(result.Warnings, warnings...)
	if len(warnings) > 0 {
		result.Fixed = true
	}
	if err := validatePlanSchema(result.Plan); err != nil {
		return result, err
	}
	return result, nil
}

func extractJSONObject(text string) string {
	text = strings.TrimSpace(text)
	if match := fencedJSON.FindStringSubmatch(text); len(match) == 2 {
		text = strings.TrimSpace(match[1])
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		text = text[start : end+1]
	}
	return text
}

func normalizePlan(plan Plan) (Plan, []string) {
	var warnings []string
	if strings.TrimSpace(plan.Summary) == "" {
		plan.Summary = "execute user task"
		warnings = append(warnings, "summary_defaulted")
	}
	for i := range plan.Steps {
		if strings.TrimSpace(plan.Steps[i].ID) == "" {
			plan.Steps[i].ID = fmt.Sprintf("step-%d", i+1)
			warnings = append(warnings, fmt.Sprintf("step_%d_id_defaulted", i+1))
		}
		if plan.Steps[i].Args == nil {
			plan.Steps[i].Args = map[string]string{}
			warnings = append(warnings, fmt.Sprintf("step_%d_args_defaulted", i+1))
		}
		plan.Steps[i].ID = strings.TrimSpace(plan.Steps[i].ID)
		plan.Steps[i].Goal = strings.TrimSpace(plan.Steps[i].Goal)
		plan.Steps[i].Tool = strings.TrimSpace(plan.Steps[i].Tool)
		plan.Steps[i].Risk = strings.TrimSpace(plan.Steps[i].Risk)
	}
	return plan, warnings
}

func validatePlanSchema(plan Plan) error {
	if len(plan.Steps) == 0 {
		return fmt.Errorf("plan schema invalid: steps must not be empty")
	}
	for i, step := range plan.Steps {
		if strings.TrimSpace(step.Tool) == "" {
			return fmt.Errorf("plan schema invalid: step %d tool is required", i+1)
		}
		if step.Args == nil {
			return fmt.Errorf("plan schema invalid: step %d args must be an object", i+1)
		}
	}
	return nil
}

func repairPlanJSONText(text string) (string, []string, error) {
	type candidate struct {
		text     string
		warning  string
		warnings []string
	}
	candidates := []candidate{
		{text: repairUnescapedStringQuotes(text), warning: "repaired_unescaped_string_quotes"},
		{text: repairMissingStepObjectClosures(text), warning: "repaired_missing_step_object_closures"},
		{text: repairMissingStepObjectClosures(repairUnescapedStringQuotes(text)), warnings: []string{"repaired_unescaped_string_quotes", "repaired_missing_step_object_closures"}},
	}
	var lastErr error
	for _, candidate := range candidates {
		if candidate.text == text {
			continue
		}
		var probe any
		if err := json.Unmarshal([]byte(candidate.text), &probe); err != nil {
			lastErr = err
			continue
		}
		warnings := candidate.warnings
		if candidate.warning != "" {
			warnings = append(warnings, candidate.warning)
		}
		return candidate.text, warnings, nil
	}
	if lastErr != nil {
		return "", nil, lastErr
	}
	return "", nil, fmt.Errorf("no repair candidate changed input")
}

func repairUnescapedStringQuotes(text string) string {
	var b strings.Builder
	b.Grow(len(text) + 16)
	inString := false
	escaped := false
	changed := false
	for i, r := range text {
		if !inString {
			b.WriteRune(r)
			if r == '"' {
				inString = true
			}
			continue
		}
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		switch r {
		case '\\':
			b.WriteRune(r)
			escaped = true
		case '"':
			if quoteLooksLikeStringTerminator(text, i) {
				b.WriteRune(r)
				inString = false
			} else {
				b.WriteString(`\"`)
				changed = true
			}
		default:
			b.WriteRune(r)
		}
	}
	if !changed {
		return text
	}
	return b.String()
}

func repairMissingStepObjectClosures(text string) string {
	stepsKey := `"steps"`
	stepsAt := strings.Index(text, stepsKey)
	if stepsAt < 0 {
		return text
	}
	arrayStartRel := strings.Index(text[stepsAt:], "[")
	if arrayStartRel < 0 {
		return text
	}
	arrayStart := stepsAt + arrayStartRel
	arrayEnd := matchingJSONArrayEnd(text, arrayStart)
	if arrayEnd < 0 {
		return text
	}
	stepsRaw := text[arrayStart+1 : arrayEnd]
	parts := splitLikelyStepObjects(stepsRaw)
	if len(parts) < 2 {
		parts = splitCompactStepObjects(stepsRaw)
	}
	if len(parts) < 2 {
		return text
	}
	changed := false
	for i, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "{") {
			return text
		}
		if missing := missingClosingObjectBraces(trimmed); missing > 0 {
			trimmed += strings.Repeat("}", missing)
			changed = true
		}
		parts[i] = trimmed
	}
	if !changed {
		return text
	}
	repairedSteps := strings.Join(parts, ",")
	return text[:arrayStart+1] + repairedSteps + text[arrayEnd:]
}

func matchingJSONArrayEnd(text string, arrayStart int) int {
	inString := false
	escaped := false
	depth := 0
	for i := arrayStart; i < len(text); i++ {
		ch := text[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func splitLikelyStepObjects(text string) []string {
	var parts []string
	inString := false
	escaped := false
	start := 0
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case ',':
			if nextLooksLikeStepObject(text[i+1:]) {
				parts = append(parts, text[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, text[start:])
	return parts
}

func splitCompactStepObjects(text string) []string {
	markers := []string{`,{"id":"step-`, `,{"id": "step-`, ",\n{\"id\":\"step-", ",\n  {\"id\":\"step-"}
	for _, marker := range markers {
		if !strings.Contains(text, marker) {
			continue
		}
		var parts []string
		start := 0
		searchFrom := 0
		for {
			idx := strings.Index(text[searchFrom:], marker)
			if idx < 0 {
				break
			}
			splitAt := searchFrom + idx
			parts = append(parts, text[start:splitAt])
			start = splitAt + 1
			searchFrom = start
		}
		parts = append(parts, text[start:])
		return parts
	}
	return []string{text}
}

func missingClosingObjectBraces(text string) int {
	inString := false
	escaped := false
	depth := 0
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		}
	}
	return depth
}

func nextLooksLikeStepObject(rest string) bool {
	trimmed := strings.TrimLeft(rest, " \n\r\t")
	return strings.HasPrefix(trimmed, `{"id"`) || strings.HasPrefix(trimmed, "{\n") || strings.HasPrefix(trimmed, "{\r\n")
}

func quoteLooksLikeStringTerminator(text string, quoteByteIndex int) bool {
	for i := quoteByteIndex + 1; i < len(text); i++ {
		switch text[i] {
		case ' ', '\n', '\r', '\t':
			continue
		case ':', ',', '}', ']':
			return true
		default:
			return false
		}
	}
	return true
}

func (c Client) ResolveFollowupJSON(ctx context.Context, prompt string) (FollowupDecision, error) {
	text, err := c.Generate(ctx, followupSystemPrompt(), []Message{{Role: "user", Content: prompt}})
	if err != nil {
		return FollowupDecision{}, err
	}
	return parseFollowupDecision(text)
}

func followupSystemPrompt() string {
	return strings.Join([]string{
		"You are the Mateway followup resolver.",
		"Decide whether the current user message should bind to the active task, another open task, a historical task continuation, or a new task.",
		"Return ONLY strict JSON. No markdown. No code fences.",
		`{"kind":"active_followup|open_task_followup|historical_continuation|new_task|ambiguous","target_task_id":"","source_task_id":"","resolved_query":"","reason":"","confidence":0.0}`,
		"Use historical_continuation only when the message clearly refers to an older completed discussion.",
		"Use ambiguous instead of guessing when the target is not clear.",
		"resolved_query must be a standalone executable user request in Chinese.",
	}, "\n")
}

func parseFollowupDecision(text string) (FollowupDecision, error) {
	text = strings.TrimSpace(text)
	if match := fencedJSON.FindStringSubmatch(text); len(match) == 2 {
		text = strings.TrimSpace(match[1])
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		text = text[start : end+1]
	}
	var decision FollowupDecision
	if err := json.Unmarshal([]byte(text), &decision); err != nil {
		return FollowupDecision{}, fmt.Errorf("parse followup json: %w; text=%s", err, text)
	}
	decision.Kind = strings.TrimSpace(decision.Kind)
	decision.TargetTaskID = strings.TrimSpace(decision.TargetTaskID)
	decision.SourceTaskID = strings.TrimSpace(decision.SourceTaskID)
	decision.ResolvedQuery = strings.TrimSpace(decision.ResolvedQuery)
	decision.Reason = strings.TrimSpace(decision.Reason)
	return decision, nil
}
