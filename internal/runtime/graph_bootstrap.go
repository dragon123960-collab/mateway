package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/session"
)

func (rt Runtime) modelForMessage(msg channel.InboundMessage) agentcore.Model {
	agentID := rt.Pool.resolveAgentIDForMessage(msg)
	if _, ok := rt.Pool.agents[agentID]; ok {
		if agent := rt.Pool.AgentForMessage(msg); agent != nil && agent.Model != nil {
			if _, heuristic := agent.Model.(HeuristicModel); heuristic {
				return rt.Model
			}
			return agent.Model
		}
	}
	return rt.Model
}

func (rt Runtime) ensureGraphForTask(
	ctx context.Context,
	msg channel.InboundMessage,
	state *session.State,
	task *session.TaskNode,
	userText string,
	trace *traceRecorder,
) error {
	if task == nil {
		return fmt.Errorf("task is nil")
	}
	if task.Graph != nil && len(task.Graph.Nodes) > 0 {
		return nil
	}

	profileID := rt.profileIDForMessage(msg)
	discoveredSkills := skillsForRuntimeContext(rt.Config, profileID)
	agentRegistry := rt.Tools
	if agent := rt.Pool.AgentForMessage(msg); agent != nil && agent.Tools != nil {
		agentRegistry = agent.Tools
	}
	workspace := strings.TrimSpace(rt.Config.App.Workspace)
	if workspace == "" {
		workspace = filepath.Join(rt.Config.App.Home, "workspace")
	}
	if skill, ok := findWorkflowSkillForTask(userText, discoveredSkills); ok {
		g, contract := buildWorkflowLaneGraph(task.ID, userText, workspace, skill)
		if errs := session.ValidateTaskGraph(&g); !errs.IsValid() {
			return fmt.Errorf("workflow lane graph validation failed: %s", errs.Error())
		}
		if invalid := validateContractTools(contract, agentRegistry, discoveredSkills); !invalid.IsValid() {
			return fmt.Errorf("workflow lane contract references unavailable tools or skills: %s", invalidContractBlockerENWithRegistries(contract, invalid, agentRegistry, rt.Tools))
		}
		task.Graph = &g
		state.SetTaskContract(task.ID, contract)
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":       "workflow_lane_selected",
				"task_id":    task.ID,
				"skill":      skill.Name,
				"skill_path": skill.Path,
				"nodes":      len(g.Nodes),
			})
			_ = trace.write(map[string]any{
				"type":    "task_lane_selected",
				"task_id": task.ID,
				"lane":    workflowLane,
				"reason":  "explicit workflow skill match",
				"trigger": skill.Name,
			})
		}
		taskFrame(trace, task)
		return rt.saveState(state, trace)
	}

	plannerContext := buildRuntimeSystemContextForTask(rt.Config, rt.Pool.ProfileForMessage(msg), userText, session.TaskContract{})
	plan, contract, planErr := rt.planTaskGraphUnified(ctx, task, userText, plannerContext, rt.Model, agentRegistry, discoveredSkills, trace)

	if _, isValidationErr := planErr.(*PlanValidationError); isValidationErr {
		return fmt.Errorf("planner validation failed: %w", planErr)
	}
	if planErr != nil {
		return fmt.Errorf("planner failed: %w", planErr)
	}

	g, convErr := convertTaskGraphPlanWithSkills(plan, task.ID, discoveredSkills)
	if convErr != nil {
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":    "graph_plan_conversion_failed",
				"task_id": task.ID,
				"error":   convErr.Error(),
			})
		}
		return fmt.Errorf("planner produced invalid graph: %w", convErr)
	}

	contract = repairContractSkillUsage(contract, discoveredSkills)
	contract = readSelectedSkillBodies(contract, discoveredSkills, workspace, profileID)
	contract = augmentContractWithSkillPlanItems(contract, discoveredSkills)
	traceSelectedSkillBodies(trace, contract)

	strategy := classifyContractStrategy(task.Goal, userText, contract)
	g.Lane = laneForContractStrategy(strategy)
	if trace != nil {
		_ = trace.write(map[string]any{
			"type":           "task_contract_strategy",
			"task_id":        task.ID,
			"strategy":       string(strategy),
			"summary":        contract.Summary,
			"requires_tools": contract.RequiresTools,
		})
		_ = trace.write(map[string]any{
			"type":              "task_contract_created",
			"task_id":           task.ID,
			"summary":           contract.Summary,
			"requires_tools":    contract.RequiresTools,
			"required_tools":    contract.RequiredTools,
			"required_skills":   requiredSkillsWithoutBody(contract.RequiredSkills),
			"required_evidence": contract.RequiredEvidence,
			"expected_outcome":  contract.ExpectedOutcome,
		})
		_ = trace.write(map[string]any{
			"type":    "task_lane_selected",
			"task_id": task.ID,
			"lane":    g.Lane,
			"reason":  laneSelectionReason(strategy, contract),
			"trigger": string(strategy),
		})
	}

	if invalid := validateContractTools(contract, agentRegistry, discoveredSkills); !invalid.IsValid() {
		return fmt.Errorf("task contract references unavailable tools or skills: %s", invalidContractBlockerENWithRegistries(contract, invalid, agentRegistry, rt.Tools))
	}

	if errs := session.ValidateTaskGraph(&g); !errs.IsValid() {
		return fmt.Errorf("graph validation failed: %s", errs.Error())
	}

	task.Graph = &g
	state.SetTaskContract(task.ID, contract)
	taskFrame(trace, task)
	return rt.saveState(state, trace)
}

func taskFrame(trace *traceRecorder, task *session.TaskNode) {
	frame := &task.Execution
	frame.Mode = "task_graph"
	frame.Status = "running"
	if trace != nil {
		_ = trace.write(map[string]any{
			"type":     "graph_attached",
			"task_id":  task.ID,
			"graph_id": task.Graph.ID,
			"nodes":    len(task.Graph.Nodes),
		})
	}
}

func (rt Runtime) profileIDForMessage(msg channel.InboundMessage) string {
	return strings.TrimSpace(rt.Pool.ProfileForMessage(msg).ID)
}
