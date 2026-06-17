package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/session"
)

type GraphPlannerOutput struct {
	Goal           string             `json:"goal"`
	Risk           string             `json:"risk"`
	Nodes          []GraphPlannerNode `json:"nodes"`
	TaskAcceptance string             `json:"task_acceptance"`
}

type GraphPlannerNode struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Goal       string         `json:"goal"`
	Depends    []string       `json:"depends,omitempty"`
	Executor   string         `json:"executor,omitempty"`
	Input      map[string]any `json:"input,omitempty"`
	Args       map[string]any `json:"args,omitempty"`
	Inputs     []string       `json:"inputs,omitempty"`
	Outputs    []string       `json:"outputs,omitempty"`
	Acceptance string         `json:"acceptance,omitempty"`
}

type TaskGraphPlan struct {
	Task  TaskPlanLevel  `json:"task"`
	Nodes []TaskPlanNode `json:"nodes"`
}

type TaskPlanLevel struct {
	Goal                 string               `json:"goal"`
	Risk                 string               `json:"risk"`
	Acceptance           string               `json:"acceptance"`
	RequiredCapabilities TaskPlanCapabilities `json:"required_capabilities"`
	FinalOutput          TaskPlanFinalOutput  `json:"final_output"`
}

type TaskPlanCapabilities struct {
	Tools      []string `json:"tools"`
	Skills     []string `json:"skills"`
	HumanGates []string `json:"human_gates"`
}

type TaskPlanFinalOutput struct {
	Text       bool     `json:"text"`
	Structured []string `json:"structured"`
}

type TaskPlanNode struct {
	ID           string         `json:"id"`
	Goal         string         `json:"goal"`
	Type         string         `json:"type"`
	Mode         string         `json:"mode"`
	Depends      []string       `json:"depends,omitempty"`
	Inputs       []string       `json:"inputs,omitempty"`
	Outputs      []string       `json:"outputs,omitempty"`
	AllowedTools []string       `json:"allowed_tools,omitempty"`
	Skill        string         `json:"skill,omitempty"`
	Risk         string         `json:"risk,omitempty"`
	Acceptance   string         `json:"acceptance,omitempty"`
	Input        map[string]any `json:"input,omitempty"`
	Args         map[string]any `json:"args,omitempty"`
	Executor     string         `json:"executor,omitempty"`
}

func (rt Runtime) planTaskGraph(
	ctx context.Context,
	task *session.TaskNode,
	userText string,
	model agentcore.Model,
	skills []discoveredSkill,
	trace *traceRecorder,
) (session.TaskGraph, error) {
	if model == nil {
		return session.TaskGraph{}, fmt.Errorf("planner requires a model")
	}
	if trace != nil {
		_ = trace.write(map[string]any{
			"type":    "graph_planner_start",
			"task_id": task.ID,
		})
	}

	prompt := renderGraphPlannerPrompt(task.Goal, userText, rt.Tools, skills)
	g, err := planGraphWithModel(ctx, model, prompt, task.ID, trace)
	if err != nil {
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":    "graph_planner_failed",
				"task_id": task.ID,
				"error":   err.Error(),
			})
		}
		return session.TaskGraph{}, err
	}

	if errs := validatePlannerToolExecutors(g, rt.Tools); !errs.IsValid() {
		return session.TaskGraph{}, fmt.Errorf("graph tool validation failed: %s", errs.Error())
	}

	if trace != nil {
		_ = trace.write(map[string]any{
			"type":     "graph_planned",
			"task_id":  task.ID,
			"graph_id": g.ID,
			"nodes":    len(g.Nodes),
		})
	}
	return g, nil
}

func planGraphWithModel(ctx context.Context, model agentcore.Model, prompt, taskID string, trace *traceRecorder) (session.TaskGraph, error) {
	graphCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	reply, err := model.Next(graphCtx, agentcore.Context{
		SystemPrompt: graphPlannerSystemPrompt,
		Messages:     []agentcore.Message{{Role: agentcore.RoleUser, Content: prompt}},
	})
	if err != nil {
		return session.TaskGraph{}, err
	}

	if trace != nil {
		_ = trace.write(map[string]any{
			"type":          "graph_planner_output",
			"task_id":       taskID,
			"output_length": len(reply.Content),
		})
	}

	output, err := parseGraphPlannerOutput(reply.Content)
	if err != nil {
		return session.TaskGraph{}, fmt.Errorf("parsing planner output: %w", err)
	}

	g, err := convertPlannerOutput(output, taskID)
	if err != nil {
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":    "graph_validation_failed",
				"task_id": taskID,
				"error":   err.Error(),
			})
		}
		return session.TaskGraph{}, err
	}
	return g, nil
}

func parseGraphPlannerOutput(raw string) (GraphPlannerOutput, error) {
	text := extractJSONBlock(raw)
	var output GraphPlannerOutput
	if err := json.Unmarshal([]byte(text), &output); err != nil {
		return GraphPlannerOutput{}, fmt.Errorf("invalid planner JSON: %w", err)
	}
	return normalizePlannerOutput(output), nil
}

func convertPlannerOutput(output GraphPlannerOutput, taskID string) (session.TaskGraph, error) {
	now := time.Now()
	nodes := make([]session.TaskGraphNode, len(output.Nodes))
	for i, pn := range output.Nodes {
		id := normalizeGraphNodeID(pn.ID, i)
		nodes[i] = session.TaskGraphNode{
			ID:       id,
			Type:     strings.TrimSpace(pn.Type),
			Goal:     strings.TrimSpace(pn.Goal),
			Status:   session.NodeStatusPending,
			Depends:  normalizeDepends(pn.Depends),
			Executor: strings.TrimSpace(pn.Executor),
			Input:    plannerNodeInput(pn),
			Output:   stringSliceToMap(pn.Outputs),
			Acceptance: session.Acceptance{
				Criteria: strings.TrimSpace(pn.Acceptance),
			},
			CreatedAt: now,
			UpdatedAt: now,
		}
	}

	g := session.TaskGraph{
		ID:        "graph-" + taskID,
		TaskID:    taskID,
		Status:    session.GraphStatusPlanned,
		Nodes:     nodes,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if errs := session.ValidateTaskGraph(&g); !errs.IsValid() {
		return session.TaskGraph{}, fmt.Errorf("graph validation failed: %s", errs.Error())
	}

	return g, nil
}

func validatePlannerToolExecutors(g session.TaskGraph, tools *agentcore.ToolRegistry) session.GraphValidationErrors {
	if tools == nil {
		return nil
	}
	var errs session.GraphValidationErrors
	for _, n := range g.Nodes {
		if n.Type != session.NodeTypeTool {
			continue
		}
		exec := strings.TrimSpace(n.Executor)
		if exec == "" {
			continue
		}
		if _, ok := tools.Get(exec); !ok {
			errs = append(errs, session.GraphValidationError{
				Message: fmt.Sprintf("planner specified unknown tool executor %q", exec),
				NodeID:  n.ID,
			})
		}
	}
	return errs
}

func normalizePlannerOutput(output GraphPlannerOutput) GraphPlannerOutput {
	output.Goal = strings.TrimSpace(output.Goal)
	output.Risk = normalizeRisk(output.Risk)
	output.TaskAcceptance = strings.TrimSpace(output.TaskAcceptance)
	for i := range output.Nodes {
		output.Nodes[i].Type = strings.TrimSpace(output.Nodes[i].Type)
		output.Nodes[i].Goal = strings.TrimSpace(output.Nodes[i].Goal)
		output.Nodes[i].Executor = strings.TrimSpace(output.Nodes[i].Executor)
		output.Nodes[i].Acceptance = strings.TrimSpace(output.Nodes[i].Acceptance)
		output.Nodes[i].ID = strings.TrimSpace(output.Nodes[i].ID)
	}
	return output
}

func normalizeGraphNodeID(id string, index int) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Sprintf("node-%d", index+1)
	}
	return strings.ToLower(strings.ReplaceAll(id, " ", "-"))
}

func normalizeDepends(depends []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, d := range depends {
		d = strings.TrimSpace(d)
		if d != "" && !seen[d] {
			out = append(out, d)
			seen[d] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeRisk(risk string) string {
	risk = strings.TrimSpace(risk)
	switch strings.ToLower(risk) {
	case "low", "medium", "high":
		return strings.ToLower(risk)
	default:
		return "medium"
	}
}

func stringSliceToMap(ss []string) map[string]any {
	if len(ss) == 0 {
		return nil
	}
	m := make(map[string]any, len(ss))
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if s != "" {
			m[s] = true
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

func plannerNodeInput(pn GraphPlannerNode) map[string]any {
	if len(pn.Input) > 0 {
		return clonePlannerMap(pn.Input)
	}
	if len(pn.Args) > 0 {
		return clonePlannerMap(pn.Args)
	}
	return stringSliceToMap(pn.Inputs)
}

func clonePlannerMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func extractJSONBlock(raw string) string {
	raw = strings.TrimSpace(raw)
	if idx := strings.Index(raw, "{"); idx >= 0 {
		raw = raw[idx:]
	}
	if idx := strings.LastIndex(raw, "}"); idx >= 0 {
		raw = raw[:idx+1]
	}
	return raw
}

type PlanValidationError struct {
	Message string
}

func (e *PlanValidationError) Error() string { return e.Message }

func (rt Runtime) planTaskGraphUnified(
	ctx context.Context,
	task *session.TaskNode,
	userText string,
	model agentcore.Model,
	tools *agentcore.ToolRegistry,
	skills []discoveredSkill,
	trace *traceRecorder,
) (TaskGraphPlan, session.TaskContract, error) {
	if model == nil {
		return TaskGraphPlan{}, session.TaskContract{}, fmt.Errorf("unified planner requires a model")
	}
	if trace != nil {
		_ = trace.write(map[string]any{
			"type":    "unified_planner_start",
			"task_id": task.ID,
		})
	}

	prompt := renderUnifiedPlannerPrompt(task.Goal, userText, tools, skills)
	plan, rawJSON, err := planWithUnifiedPlanner(ctx, model, prompt, task.ID, trace)
	if err != nil {
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":    "unified_planner_failed",
				"task_id": task.ID,
				"error":   err.Error(),
			})
		}
		return TaskGraphPlan{}, session.TaskContract{}, err
	}

	contract := taskContractFromPlan(plan)
	if errs := validatePlanTools(plan, tools, skills); !errs.IsValid() {
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":    "unified_planner_invalid_tools",
				"task_id": task.ID,
				"error":   errs.Error(),
			})
		}
		return plan, contract, &PlanValidationError{Message: "planner referenced unknown tools or skills: " + errs.Error()}
	}

	if trace != nil {
		_ = trace.write(map[string]any{
			"type":       "unified_plan_generated",
			"task_id":    task.ID,
			"nodes":      len(plan.Nodes),
			"output_len": len(rawJSON),
		})
		_ = trace.write(map[string]any{
			"type":    "unified_plan_validated",
			"task_id": task.ID,
			"nodes":   len(plan.Nodes),
		})
	}
	return plan, contract, nil
}

func planWithUnifiedPlanner(ctx context.Context, model agentcore.Model, prompt, taskID string, trace *traceRecorder) (TaskGraphPlan, string, error) {
	graphCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	reply, err := model.Next(graphCtx, agentcore.Context{
		SystemPrompt: unifiedPlannerSystemPrompt,
		Messages:     []agentcore.Message{{Role: agentcore.RoleUser, Content: prompt}},
	})
	if err != nil {
		return TaskGraphPlan{}, "", err
	}
	raw := reply.Content

	if trace != nil {
		_ = trace.write(map[string]any{
			"type":          "unified_planner_raw_output",
			"task_id":       taskID,
			"output_length": len(raw),
		})
	}

	plan, err := parseTaskGraphPlan(raw)
	if err != nil {
		return TaskGraphPlan{}, raw, fmt.Errorf("parsing unified planner output: %w", err)
	}
	return plan, raw, nil
}

func parseTaskGraphPlan(raw string) (TaskGraphPlan, error) {
	text := extractJSONBlock(raw)
	var plan TaskGraphPlan
	if err := json.Unmarshal([]byte(text), &plan); err != nil {
		return TaskGraphPlan{}, fmt.Errorf("invalid unified planner JSON: %w", err)
	}
	plan = normalizeTaskGraphPlan(plan)
	if strings.TrimSpace(plan.Task.Goal) == "" {
		return TaskGraphPlan{}, fmt.Errorf("task.goal is empty")
	}
	if len(plan.Nodes) == 0 {
		return TaskGraphPlan{}, fmt.Errorf("plan has no nodes")
	}
	return plan, nil
}

func normalizeTaskGraphPlan(plan TaskGraphPlan) TaskGraphPlan {
	plan.Task.Goal = strings.TrimSpace(plan.Task.Goal)
	plan.Task.Risk = normalizeRisk(plan.Task.Risk)
	plan.Task.Acceptance = strings.TrimSpace(plan.Task.Acceptance)
	for i := range plan.Task.RequiredCapabilities.Tools {
		plan.Task.RequiredCapabilities.Tools[i] = strings.TrimSpace(plan.Task.RequiredCapabilities.Tools[i])
	}
	for i := range plan.Task.RequiredCapabilities.Skills {
		plan.Task.RequiredCapabilities.Skills[i] = strings.TrimSpace(plan.Task.RequiredCapabilities.Skills[i])
	}
	for i := range plan.Nodes {
		plan.Nodes[i].ID = strings.TrimSpace(plan.Nodes[i].ID)
		plan.Nodes[i].Type = strings.TrimSpace(plan.Nodes[i].Type)
		plan.Nodes[i].Mode = strings.TrimSpace(plan.Nodes[i].Mode)
		plan.Nodes[i].Goal = strings.TrimSpace(plan.Nodes[i].Goal)
		plan.Nodes[i].Skill = strings.TrimSpace(plan.Nodes[i].Skill)
		plan.Nodes[i].Acceptance = strings.TrimSpace(plan.Nodes[i].Acceptance)
		plan.Nodes[i].Executor = strings.TrimSpace(plan.Nodes[i].Executor)
	}
	return plan
}

func convertTaskGraphPlan(plan TaskGraphPlan, taskID string) (session.TaskGraph, error) {
	now := time.Now()
	nodes := make([]session.TaskGraphNode, len(plan.Nodes))
	for i, pn := range plan.Nodes {
		id := normalizeGraphNodeID(pn.ID, i)
		nodeType := normalizePlanNodeType(pn)
		nodeMode := determineNodeMode(pn)
		nodeInput := planNodeInput(pn)
		if skill := strings.TrimSpace(pn.Skill); skill != "" {
			if nodeInput == nil {
				nodeInput = make(map[string]any)
			}
			nodeInput["skill"] = skill
		}
		nodes[i] = session.TaskGraphNode{
			ID:           id,
			Type:         nodeType,
			Mode:         nodeMode,
			Goal:         strings.TrimSpace(pn.Goal),
			Status:       session.NodeStatusPending,
			Depends:      normalizeDepends(pn.Depends),
			Executor:     strings.TrimSpace(pn.Executor),
			Input:        nodeInput,
			Output:       stringSliceToMap(pn.Outputs),
			AllowedTools: cleanStringSlice(pn.AllowedTools),
			Acceptance: session.Acceptance{
				Criteria: strings.TrimSpace(pn.Acceptance),
			},
			CreatedAt: now,
			UpdatedAt: now,
		}
	}

	g := session.TaskGraph{
		ID:        "graph-" + taskID,
		TaskID:    taskID,
		Status:    session.GraphStatusPlanned,
		Nodes:     nodes,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if errs := session.ValidateTaskGraph(&g); !errs.IsValid() {
		return session.TaskGraph{}, fmt.Errorf("graph validation failed: %s", errs.Error())
	}

	return g, nil
}

func normalizePlanNodeType(pn TaskPlanNode) string {
	t := strings.ToLower(strings.TrimSpace(pn.Type))
	switch t {
	case "subtask":
		return session.NodeTypeSubtask
	case "human_confirm":
		return session.NodeTypeHumanConfirm
	case "human_review":
		return session.NodeTypeHumanReview
	default:
		return t
	}
}

func determineNodeMode(pn TaskPlanNode) string {
	mode := strings.TrimSpace(pn.Mode)
	if mode != "" {
		return mode
	}
	t := strings.ToLower(strings.TrimSpace(pn.Type))
	switch t {
	case "human_confirm", "human_review":
		return ""
	default:
		return session.NodeModeReact
	}
}

func planNodeInput(pn TaskPlanNode) map[string]any {
	if len(pn.Input) > 0 {
		return clonePlannerMap(pn.Input)
	}
	if len(pn.Args) > 0 {
		return clonePlannerMap(pn.Args)
	}
	return stringSliceToMap(pn.Inputs)
}

func taskContractFromPlan(plan TaskGraphPlan) session.TaskContract {
	requiredTools := cleanStringSlice(plan.Task.RequiredCapabilities.Tools)
	requiredToolsSet := make(map[string]bool)
	for _, t := range requiredTools {
		requiredToolsSet[t] = true
	}

	var planItems []session.TaskPlanItem
	var evidenceItems []session.TaskEvidenceContract
	for i, pn := range plan.Nodes {
		nodeID := normalizeGraphNodeID(pn.ID, i)
		_ = nodeID
		tool := ""
		if len(pn.AllowedTools) > 0 {
			tool = strings.TrimSpace(pn.AllowedTools[0])
		} else if pn.Executor != "" {
			tool = strings.TrimSpace(pn.Executor)
		}
		if tool != "" && !requiredToolsSet[tool] {
			requiredTools = append(requiredTools, tool)
			requiredToolsSet[tool] = true
		}
		for _, t := range pn.AllowedTools {
			t = strings.TrimSpace(t)
			if t != "" && !requiredToolsSet[t] {
				requiredTools = append(requiredTools, t)
				requiredToolsSet[t] = true
			}
		}
		if pn.Executor != "" && !requiredToolsSet[pn.Executor] {
			requiredTools = append(requiredTools, pn.Executor)
			requiredToolsSet[pn.Executor] = true
		}
		if pn.Skill != "" {
			tool = "file.read"
		}
		if tool != "" && pn.Acceptance != "" {
			evidenceItems = append(evidenceItems, session.TaskEvidenceContract{
				Tool:        tool,
				Description: strings.TrimSpace(pn.Acceptance),
			})
		}
		planItems = append(planItems, session.TaskPlanItem{
			ID:       fmt.Sprintf("plan-%d", i+1),
			Title:    strings.TrimSpace(pn.Goal),
			Status:   "pending",
			Tool:     tool,
			Criteria: strings.TrimSpace(pn.Acceptance),
		})
	}

	contract := session.TaskContract{
		Summary:          strings.TrimSpace(plan.Task.Goal),
		RequiredTools:    requiredTools,
		RequiredEvidence: evidenceItems,
		ExpectedOutcome:  strings.TrimSpace(plan.Task.Acceptance),
		PlanItems:        planItems,
		CreatedAt:        time.Now(),
	}
	if len(requiredTools) > 0 {
		contract.RequiresTools = true
	}

	skills := cleanStringSlice(plan.Task.RequiredCapabilities.Skills)
	for _, s := range skills {
		contract.RequiredSkills = append(contract.RequiredSkills, session.RequiredSkill{
			Name:   s,
			Reason: "required by unified planner",
		})
	}
	return contract
}

func validatePlanTools(plan TaskGraphPlan, tools *agentcore.ToolRegistry, skills []discoveredSkill) session.GraphValidationErrors {
	if tools == nil {
		return nil
	}
	skillNames := make(map[string]bool)
	for _, s := range skills {
		skillNames[strings.ToLower(strings.TrimSpace(s.Name))] = true
	}
	var errs session.GraphValidationErrors
	for i, pn := range plan.Nodes {
		id := normalizeGraphNodeID(pn.ID, i)
		for _, toolName := range pn.AllowedTools {
			toolName = strings.TrimSpace(toolName)
			if toolName == "" {
				continue
			}
			if _, ok := tools.Get(toolName); !ok {
				if skillNames[strings.ToLower(toolName)] {
					errs = append(errs, session.GraphValidationError{
						Message: fmt.Sprintf("skill name %q used in allowed_tools; skills must go in node.skill or task.required_capabilities.skills", toolName),
						NodeID:  id,
					})
				} else {
					errs = append(errs, session.GraphValidationError{
						Message: fmt.Sprintf("planner specified unknown tool %q in allowed_tools", toolName),
						NodeID:  id,
					})
				}
			}
		}
		if exec := strings.TrimSpace(pn.Executor); exec != "" {
			if _, ok := tools.Get(exec); !ok {
				if skillNames[strings.ToLower(exec)] {
					errs = append(errs, session.GraphValidationError{
						Message: fmt.Sprintf("skill name %q used as executor; skills must go in node.skill or task.required_capabilities.skills", exec),
						NodeID:  id,
					})
				} else {
					errs = append(errs, session.GraphValidationError{
						Message: fmt.Sprintf("planner specified unknown executor %q", exec),
						NodeID:  id,
					})
				}
			}
		}
	}
	for _, t := range plan.Task.RequiredCapabilities.Tools {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := tools.Get(t); !ok {
			if skillNames[strings.ToLower(t)] {
				errs = append(errs, session.GraphValidationError{
					Message: fmt.Sprintf("skill name %q used in task.required_capabilities.tools; skills must go in required_capabilities.skills", t),
				})
			} else {
				errs = append(errs, session.GraphValidationError{
					Message: fmt.Sprintf("task requires unknown tool %q", t),
				})
			}
		}
	}
	for _, s := range plan.Task.RequiredCapabilities.Skills {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !skillNames[strings.ToLower(s)] {
			errs = append(errs, session.GraphValidationError{
				Message: fmt.Sprintf("task requires unknown skill %q", s),
			})
		}
	}
	for i, pn := range plan.Nodes {
		skill := strings.TrimSpace(pn.Skill)
		if skill == "" {
			continue
		}
		id := normalizeGraphNodeID(pn.ID, i)
		if !skillNames[strings.ToLower(skill)] {
			errs = append(errs, session.GraphValidationError{
				Message: fmt.Sprintf("node references unknown skill %q", skill),
				NodeID:  id,
			})
		}
	}
	return errs
}

func cleanStringSlice(ss []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
