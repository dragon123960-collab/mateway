package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

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

	plan, contract, planErr := rt.planTaskGraphUnified(ctx, task, userText, rt.Model, agentRegistry, discoveredSkills, trace)
	if planErr != nil {
		return fmt.Errorf("planner failed: %w", planErr)
	}

	graph, convErr := convertTaskGraphPlan(plan, task.ID)
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

	workspace := strings.TrimSpace(rt.Config.App.Workspace)
	if workspace == "" {
		workspace = filepath.Join(rt.Config.App.Home, "workspace")
	}
	contract = repairContractSkillUsage(contract, discoveredSkills)
	contract = readSelectedSkillBodies(contract, discoveredSkills, workspace, profileID)
	contract = augmentContractWithSkillPlanItems(contract, discoveredSkills)
	traceSelectedSkillBodies(trace, contract)

	strategy := classifyContractStrategy(task.Goal, userText, contract)
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
	}

	if invalid := validateContractTools(contract, agentRegistry, discoveredSkills); !invalid.IsValid() {
		return fmt.Errorf("task contract references unavailable tools or skills: %s", invalidContractBlockerENWithRegistries(contract, invalid, agentRegistry, rt.Tools))
	}

	if errs := session.ValidateTaskGraph(&graph); !errs.IsValid() {
		return fmt.Errorf("graph validation failed: %s", errs.Error())
	}

	task.Graph = &graph
	state.SetTaskContract(task.ID, contract)
	frame := state.EnsureExecutionFrame(task.ID)
	if frame != nil {
		frame.Mode = "task_graph"
		frame.Status = "running"
	}
	if trace != nil {
		_ = trace.write(map[string]any{
			"type":     "graph_attached",
			"task_id":  task.ID,
			"graph_id": graph.ID,
			"nodes":    len(graph.Nodes),
		})
	}
	return rt.saveState(state, trace)
}

func (rt Runtime) profileIDForMessage(msg channel.InboundMessage) string {
	return strings.TrimSpace(rt.Pool.ProfileForMessage(msg).ID)
}

func fallbackGraphFromContract(task *session.TaskNode, contract session.TaskContract, userText string) session.TaskGraph {
	now := time.Now()
	nodes := fallbackNodesFromContract(contract, task.Goal, userText, now)
	return session.TaskGraph{
		ID:        "graph-" + task.ID,
		TaskID:    task.ID,
		Status:    session.GraphStatusPlanned,
		Nodes:     nodes,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func fallbackNodesFromContract(contract session.TaskContract, goal, userText string, now time.Time) []session.TaskGraphNode {
	modelID := "answer"
	modelGoal := firstNonEmpty(contract.ExpectedOutcome, contract.Summary, userText, goal)
	if contract.RequiresTools || len(contract.RequiredTools) > 0 || contractHasToolPlanItems(contract) {
		modelID = "synthesize"
		modelGoal = firstNonEmpty(
			contract.ExpectedOutcome,
			contract.Summary,
			"explain that graph planning failed before executable tool inputs could be derived",
			userText,
			goal,
		)
	}
	modelNode := session.TaskGraphNode{
		ID:        modelID,
		Type:      session.NodeTypeModel,
		Goal:      modelGoal,
		Status:    session.NodeStatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if contract.RequiresTools || len(contract.RequiredTools) > 0 || contractHasToolPlanItems(contract) {
		modelNode.Status = session.NodeStatusBlocked
		modelNode.FailureReason = "graph planning failed before executable tool inputs could be derived"
	}
	return []session.TaskGraphNode{modelNode}
}

func contractHasToolPlanItems(contract session.TaskContract) bool {
	for _, item := range contract.PlanItems {
		if strings.TrimSpace(item.Tool) != "" {
			return true
		}
	}
	return false
}
