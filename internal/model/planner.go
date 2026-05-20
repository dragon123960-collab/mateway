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
	prompt := "User task:\n" + user + "\n\nAvailable tools:\n" + toolListForPrompt(tools)
	if strings.TrimSpace(skillPrompt) != "" {
		prompt += "\n\n" + skillPrompt
	}
	prompt += "\n\nReturn the JSON plan now."
	text, err := c.Generate(ctx, plannerSystemPrompt(skillPrompt), []Message{{Role: "user", Content: prompt}})
	if err != nil {
		return Plan{}, err
	}
	return parsePlan(text)
}

func (c Client) RepairPlanJSON(ctx context.Context, user string, plan Plan, results []ToolResult, tools []tool.Definition, skillPrompt string) (Plan, error) {
	planData, _ := json.MarshalIndent(plan, "", "  ")
	resultData, _ := json.MarshalIndent(results, "", "  ")
	prompt := "User task:\n" + user + "\n\nPrevious plan:\n" + string(planData) + "\n\nTool results/errors:\n" + string(resultData) + "\n\nAvailable tools:\n" + toolListForPrompt(tools)
	if strings.TrimSpace(skillPrompt) != "" {
		prompt += "\n\n" + skillPrompt
	}
	prompt += "\n\nReturn a corrected JSON plan. Keep already successful work in mind and only plan the remaining necessary steps."
	text, err := c.Generate(ctx, plannerSystemPrompt(skillPrompt), []Message{{Role: "user", Content: prompt}})
	if err != nil {
		return Plan{}, err
	}
	return parsePlan(text)
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
		"For reading files use file.read.",
		"For writing or editing files use file.write or file.patch and set requires_confirm=true unless the user explicitly confirmed.",
		"For local commands use shell.run. Set requires_confirm=true for dangerous commands.",
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
	text = strings.TrimSpace(text)
	if match := fencedJSON.FindStringSubmatch(text); len(match) == 2 {
		text = strings.TrimSpace(match[1])
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		text = text[start : end+1]
	}
	var plan Plan
	if err := json.Unmarshal([]byte(text), &plan); err != nil {
		return Plan{}, fmt.Errorf("parse plan json: %w; text=%s", err, text)
	}
	if strings.TrimSpace(plan.Summary) == "" {
		plan.Summary = "execute user task"
	}
	for i := range plan.Steps {
		if plan.Steps[i].ID == "" {
			plan.Steps[i].ID = fmt.Sprintf("step-%d", i+1)
		}
		if plan.Steps[i].Args == nil {
			plan.Steps[i].Args = map[string]string{}
		}
	}
	return plan, nil
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
