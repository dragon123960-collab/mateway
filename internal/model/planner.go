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
	Summary       string            `json:"summary"`
	Understanding UnderstandingJSON `json:"understanding"`
	Steps         []PlanStep        `json:"steps"`
}

type UnderstandingJSON struct {
	Goal                 string   `json:"goal"`
	Subtasks             []string `json:"subtasks,omitempty"`
	Constraints          []string `json:"constraints,omitempty"`
	MissingInfo          []string `json:"missing_info,omitempty"`
	CompletionCriteria   []string `json:"completion_criteria,omitempty"`
	EvidenceExpectations []string `json:"evidence_expectations,omitempty"`
	RiskLevel            string   `json:"risk_level,omitempty"`
	ToolNeeds            []string `json:"tool_needs,omitempty"`
}

func (u *UnderstandingJSON) UnmarshalJSON(data []byte) error {
	type alias UnderstandingJSON
	var raw struct {
		alias
		Subtasks             flexibleStringList `json:"subtasks,omitempty"`
		Constraints          flexibleStringList `json:"constraints,omitempty"`
		MissingInfo          flexibleStringList `json:"missing_info,omitempty"`
		CompletionCriteria   flexibleStringList `json:"completion_criteria,omitempty"`
		EvidenceExpectations flexibleStringList `json:"evidence_expectations,omitempty"`
		ToolNeeds            flexibleStringList `json:"tool_needs,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*u = UnderstandingJSON(raw.alias)
	u.Subtasks = []string(raw.Subtasks)
	u.Constraints = []string(raw.Constraints)
	u.MissingInfo = []string(raw.MissingInfo)
	u.CompletionCriteria = []string(raw.CompletionCriteria)
	u.EvidenceExpectations = []string(raw.EvidenceExpectations)
	u.ToolNeeds = []string(raw.ToolNeeds)
	return nil
}

type PlanStep struct {
	ID               string   `json:"id"`
	Goal             string   `json:"goal"`
	Tool             string   `json:"tool"`
	Args             Args     `json:"args"`
	DependsOn        []string `json:"depends_on,omitempty"`
	Risk             string   `json:"risk"`
	RequiresConfirm  bool     `json:"requires_confirm"`
	ExpectedEvidence []string `json:"expected_evidence"`
	SuccessCriteria  []string `json:"success_criteria,omitempty"`
	OnFailure        string   `json:"on_failure,omitempty"`
}

func (s *PlanStep) UnmarshalJSON(data []byte) error {
	type alias PlanStep
	var raw struct {
		alias
		DependsOn        flexibleStringList `json:"depends_on,omitempty"`
		ExpectedEvidence flexibleStringList `json:"expected_evidence"`
		SuccessCriteria  flexibleStringList `json:"success_criteria,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*s = PlanStep(raw.alias)
	s.DependsOn = []string(raw.DependsOn)
	s.ExpectedEvidence = []string(raw.ExpectedEvidence)
	s.SuccessCriteria = []string(raw.SuccessCriteria)
	return nil
}

type PlanCheckResult struct {
	Plan     Plan
	Fixed    bool
	Warnings []string
	Raw      string
}

type Args map[string]string

type flexibleStringList []string

func (l *flexibleStringList) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "" || text == "null" {
		*l = nil
		return nil
	}
	if strings.HasPrefix(text, "[") {
		var out []string
		if err := json.Unmarshal(data, &out); err != nil {
			return err
		}
		*l = out
		return nil
	}
	var single string
	if err := json.Unmarshal(data, &single); err != nil {
		return err
	}
	single = strings.TrimSpace(single)
	if single == "" {
		*l = nil
		return nil
	}
	*l = []string{single}
	return nil
}

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
	resultData, _ := json.MarshalIndent(synthesisResultView(results), "", "  ")
	systemParts := []string{
		"You are Mateway, a concise tool-using assistant.",
		"Answer the user from the supplied tool results.",
		"Do not dump raw tool JSON. Summarize evidence clearly.",
		"Do not include tool args, step IDs, or internal plan JSON unless the user explicitly asks for debugging details.",
		"If a search returns no results, explain what was searched and suggest 2-3 better next search phrases or a broader plan.",
		"If execution stopped for confirmation, explain what is waiting and why.",
	}
	if strings.TrimSpace(skillPrompt) != "" {
		systemParts = append(systemParts, skillPrompt)
	}
	system := strings.Join(systemParts, "\n")
	prompt := "User task:\n" + user + "\n\nTool result summaries:\n" + string(resultData)
	return c.Generate(ctx, system, []Message{{Role: "user", Content: prompt}})
}

func (c Client) AcceptStepJSON(ctx context.Context, user string, step PlanStep, result ToolResult) (string, error) {
	stepData, _ := json.MarshalIndent(step, "", "  ")
	resultData, _ := json.MarshalIndent(result, "", "  ")
	system := strings.Join([]string{
		"You are the Mateway step acceptance reviewer.",
		"Return ONLY strict JSON. No markdown.",
		`{"status":"pass|suspect|hard_fail","reason":"short reason"}`,
		"Judge only whether the step result basically satisfies the step goal.",
		"If the output shows a soft failure such as not found, no results, permission denied, timeout, or missing artifact, use suspect or hard_fail.",
	}, "\n")
	prompt := "User task:\n" + user + "\n\nStep:\n" + string(stepData) + "\n\nStep result:\n" + string(resultData)
	return c.Generate(ctx, system, []Message{{Role: "user", Content: prompt}})
}

func (c Client) AcceptFinalJSON(ctx context.Context, user string, plan Plan, results []ToolResult) (string, error) {
	planData, _ := json.MarshalIndent(plan, "", "  ")
	resultData, _ := json.MarshalIndent(synthesisResultView(results), "", "  ")
	system := strings.Join([]string{
		"You are the Mateway final acceptance reviewer.",
		"Return ONLY strict JSON. No markdown.",
		`{"status":"accepted|partial|rejected","reason":"short reason","missing":["optional gaps"]}`,
		"Be lightweight and practical.",
		"Accepted means the user goal is basically complete.",
		"Partial means the main goal is mostly complete but there are minor gaps.",
		"Rejected means the core goal is not complete or the evidence is contradictory.",
	}, "\n")
	prompt := "User task:\n" + user + "\n\nPlan:\n" + string(planData) + "\n\nResults:\n" + string(resultData)
	return c.Generate(ctx, system, []Message{{Role: "user", Content: prompt}})
}

func synthesisResultView(results []ToolResult) []map[string]any {
	out := make([]map[string]any, 0, len(results))
	for _, result := range results {
		item := map[string]any{
			"tool":    result.Tool,
			"ok":      result.OK,
			"summary": summarizeToolOutput(result.Output, 900),
		}
		if result.Error != "" && result.Error != "await_confirm" {
			item["error"] = result.Error
		}
		if len(result.Evidence) > 0 {
			item["evidence"] = result.Evidence
		}
		out = append(out, item)
	}
	return out
}

func summarizeToolOutput(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len([]rune(text)) <= limit {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:limit])) + "..."
}

func plannerSystemPrompt(skillPrompt string) string {
	lines := []string{
		"You are the Mateway planner.",
		"Most important rules:",
		"1. Return ONLY strict JSON. No markdown. No code fences.",
		"2. Use only available tool names.",
		"3. First understand the task, then directly decompose it and choose tools for a verifiable execution contract.",
		"4. Every non-trivial step must include goal, expected_evidence, and success_criteria.",
		"5. Always fill the understanding block before planning steps.",
		"First understand the user's task, then decompose it into subtasks, choose tools, and produce a verifiable execution contract.",
		"The JSON schema is:",
		`{"summary":"short summary","understanding":{"goal":"core goal","subtasks":["subtask"],"constraints":["constraint"],"missing_info":["missing fact"],"completion_criteria":["what counts as done"],"evidence_expectations":["evidence expected"],"risk_level":"safe_read|guarded_mutation|dangerous_execute","tool_needs":["tool.name"]},"steps":[{"id":"step-1","goal":"what this step must accomplish","tool":"tool.name","args":{"key":"value"},"depends_on":["step-id"],"risk":"safe_read|guarded_mutation|dangerous_execute","requires_confirm":false,"expected_evidence":["file path, URL, line range, search result, task id, or verification record"],"success_criteria":["how to know this step is complete"],"on_failure":"repair|ask_user|stop"}]}`,
		"depends_on, expected_evidence, and success_criteria must always be JSON arrays of strings, even when there is only one item.",
		"Use at most 6 steps.",
		"Use depends_on when a step needs prior output. References must point to earlier step IDs.",
		"Use on_failure=repair by default. Use ask_user only when the user must provide missing facts. Use stop when continuing would be unsafe.",
		"Do not call tools just to fill a plan. If required information is missing, use user.ask with the missing fields in the question.",
		"Use understanding.tool_needs to capture the concrete tools the plan expects to use, not abstract capability labels.",
		"Use understanding.completion_criteria and understanding.evidence_expectations to guide step success_criteria and expected_evidence.",
		"Prefer the shortest verifiable plan that can complete the task.",
		"Simple tasks should usually be 1-3 steps. Most complex tasks should stay within 3-5 steps unless there is a strong reason for more.",
		"Do not repeat near-identical read or summary steps for the same source unless the second step is truly necessary.",
		"Do not create intermediate files or artifacts unless the user asked for them or the plan cannot proceed without them.",
		"Do not write a file just to stage or restate information for a later step unless the user explicitly asked for that file.",
		"For terminal diagnostics, prefer fewer higher-signal commands over many similar commands.",
		"For current time/date use time.now.",
		"For configuration summary use config.summary.",
		"For web/current information use web.search.",
		"When the user provides a URL or a previous search result contains a URL that needs reading, use web.fetch instead of searching again.",
		"For finding public software, CLI tools, GitHub repositories, install commands, or whether a tool can be used, use software.search before generic web.search.",
		"For installing CLI software use software.install only after you have an explicit install command from upstream docs or the user. Include command, executable or verify_command, method, source_url when known, and verification evidence. Do not guess install commands.",
		"For requests about finding or installing agent skills, first understand the capability the user wants. Use skill.search with concise capability keywords, not the whole user sentence. Prefer skills.sh, skillhub.cn, and clawhub.ai through that tool. skill.search only searches and never installs. If the user asks to install, the plan must include skill.install after skill.search, with skill.install depending on the search result. If the first query is too specific, repair with broader synonyms.",
		"For installing agent skills use skill.install. The name arg may be an exact skill name, URL, or the same concise capability query used in skill.search; the tool resolves the best catalog match. If the user asks to install a skill and test it, plan skill.install first, then a matching test task. Do not use terminal.run or local file scans to find or install skills.",
		"For reading files use file.read.",
		"For project overview, repository map, package distribution, or file tree use project.index instead of terminal.run.",
		"For summarizing one file use file.summary before falling back to file.read.",
		"For writing or editing files use file.write or file.patch. The runtime enforces confirmation policy; do not add confirmation steps unless the user must answer a question.",
		"For local terminal diagnostics, CLI status checks, logs, tests, builds, and running small local scripts, use terminal.run. When checking whether local software is stuck or running, first verify the CLI exists with command -v, then run the read-only status command if obvious, then inspect safe process or log output if needed.",
		"For generic local shell commands, use terminal.run. Destructive delete commands require runtime confirmation. Safe read-only commands such as pwd, ls, git status, go test, and find-like inspection should use requires_confirm=false.",
		"For recurring or scheduled user tasks such as daily, weekly, monthly, interval, cron-like, or automatic repeated execution, use schedule.create, schedule.update, schedule.list, schedule.show, schedule.pause, schedule.resume, or schedule.delete. If the user gives a schedule name or likely id for deletion, prefer schedule.delete with that id; do not stop at schedule.list unless the target is genuinely ambiguous. Do not use terminal.run to configure crontab, launchd, or background daemons. Ask for missing schedule fields with user.ask.",
		"When information is missing, use user.ask.",
		"Final reminders:",
		"- Return ONLY strict JSON.",
		"- The understanding block is required and must not be empty.",
		"- understanding.tool_needs must list concrete tools, and should usually contain 1-4 items.",
		"- Do not invent unavailable tools or guessed install commands.",
		"- Keep the plan short; avoid redundant file reads, summaries, and terminal calls.",
		"- Non-trivial steps must include goal, expected_evidence, and success_criteria.",
	}
	if strings.TrimSpace(skillPrompt) != "" {
		lines = append(lines, skillPrompt)
	}
	return strings.Join(lines, "\n")
}

func toolListForPrompt(tools []tool.Definition) string {
	var b strings.Builder
	for _, def := range tools {
		fmt.Fprintf(&b, "- %s: %s risk=%s args=%v when_to_use=%v when_not_to_use=%v output_contract=%v acceptance_mode=%s parallel_mode=%s resource_scope=%s\n",
			def.Name,
			def.Description,
			def.Risk,
			def.ArgsSchema,
			def.Metadata.WhenToUse,
			def.Metadata.WhenNotToUse,
			def.Metadata.OutputContract,
			def.Metadata.AcceptanceMode,
			def.Metadata.ParallelMode,
			def.Metadata.ResourceScope,
		)
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
	if strings.TrimSpace(plan.Understanding.Goal) == "" {
		plan.Understanding.Goal = plan.Summary
		warnings = append(warnings, "understanding_goal_defaulted")
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
		plan.Steps[i].OnFailure = strings.TrimSpace(plan.Steps[i].OnFailure)
		if plan.Steps[i].OnFailure == "" {
			plan.Steps[i].OnFailure = "repair"
			warnings = append(warnings, fmt.Sprintf("step_%d_on_failure_defaulted", i+1))
		}
	}
	if len(plan.Understanding.ToolNeeds) == 0 {
		warnings = append(warnings, "understanding_tool_needs_empty")
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
		switch step.OnFailure {
		case "", "repair", "ask_user", "stop":
		default:
			return fmt.Errorf("plan schema invalid: step %d on_failure must be repair, ask_user, or stop", i+1)
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
		{text: repairStrayStringValueBracket(text), warning: "repaired_stray_string_value_bracket"},
		{text: repairMissingStringArrayClosures(text), warning: "repaired_missing_string_array_closures"},
		{text: repairMissingStepObjectClosures(text), warning: "repaired_missing_step_object_closures"},
		{text: repairMissingStepObjectClosures(repairUnescapedStringQuotes(text)), warnings: []string{"repaired_unescaped_string_quotes", "repaired_missing_step_object_closures"}},
		{text: repairMissingStepObjectClosures(repairMissingStringArrayClosures(text)), warnings: []string{"repaired_missing_string_array_closures", "repaired_missing_step_object_closures"}},
		{text: repairMissingStepObjectClosures(repairStrayStringValueBracket(repairUnescapedStringQuotes(text))), warnings: []string{"repaired_unescaped_string_quotes", "repaired_stray_string_value_bracket", "repaired_missing_step_object_closures"}},
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

var strayStringValueBracket = regexp.MustCompile(`(:\s*"(?:\\.|[^"\\])*")\s*\]([,}])`)
var missingStringArrayClosureBeforeField = regexp.MustCompile(`("(?:expected_evidence|success_criteria|depends_on|subtasks|constraints|missing_info|completion_criteria|evidence_expectations|tool_needs)"\s*:\s*\[[^\]\{\[]*"(?:\\.|[^"\\])*")\s*\}(\s*,\s*"(?:on_failure|risk|requires_confirm|expected_evidence|success_criteria|depends_on|tool|args|id|goal|summary|steps|understanding)")`)

func repairStrayStringValueBracket(text string) string {
	return strayStringValueBracket.ReplaceAllString(text, "$1}$2")
}

func repairMissingStringArrayClosures(text string) string {
	return missingStringArrayClosureBeforeField.ReplaceAllString(text, "$1]$2")
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
