package runtime

import (
	"context"
	"fmt"
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

	execModel := rt.modelForMessage(msg)
	contract := rt.ensureTaskContract(ctx, msg, state, task, userText, execModel, trace)
	profileID := rt.profileIDForMessage(msg)
	discoveredSkills := skillsForRuntimeContext(rt.Config, profileID)
	agentRegistry := rt.Tools
	if agent := rt.Pool.AgentForMessage(msg); agent != nil && agent.Tools != nil {
		agentRegistry = agent.Tools
	}
	if invalid := validateContractTools(contract, agentRegistry, discoveredSkills); !invalid.IsValid() {
		return fmt.Errorf("task contract references unavailable tools or skills: %s", invalidContractBlockerENWithRegistries(contract, invalid, agentRegistry, rt.Tools))
	}

	var graph session.TaskGraph
	planned, err := rt.planTaskGraph(ctx, task, userText, rt.Model, discoveredSkills, trace)
	if err == nil {
		graph = planned
	} else {
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":    "graph_planner_fallback",
				"task_id": task.ID,
				"error":   err.Error(),
			})
		}
		graph = fallbackGraphFromContract(task, contract, userText)
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
