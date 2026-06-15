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
	ID         string   `json:"id"`
	Type       string   `json:"type"`
	Goal       string   `json:"goal"`
	Depends    []string `json:"depends,omitempty"`
	Executor   string   `json:"executor,omitempty"`
	Inputs     []string `json:"inputs,omitempty"`
	Outputs    []string `json:"outputs,omitempty"`
	Acceptance string   `json:"acceptance,omitempty"`
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
			Input:    stringSliceToMap(pn.Inputs),
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
