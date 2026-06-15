package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/session"
	"gopkg.in/yaml.v3"
)

func (rt Runtime) executeNode(
	ctx context.Context,
	msg channel.InboundMessage,
	state *session.State,
	g *session.TaskGraph,
	node *session.TaskGraphNode,
	userText string,
	trace *traceRecorder,
) error {
	if trace != nil {
		_ = trace.write(map[string]any{
			"type":      "node_execute_start",
			"graph_id":  g.ID,
			"node_id":   node.ID,
			"node_type": node.Type,
			"goal":      node.Goal,
		})
	}

	node.Status = session.NodeStatusRunning
	node.Attempts++
	node.UpdatedAt = time.Now()

	switch node.Type {
	case session.NodeTypeModel:
		return rt.executeModelNode(ctx, msg, state, g, node, userText, trace)
	case session.NodeTypeTool:
		return rt.executeToolNode(ctx, msg, state, g, node, trace)
	case session.NodeTypeSkill:
		return rt.executeSkillNode(ctx, msg, state, g, node, userText, trace)
	case session.NodeTypeHumanReview, session.NodeTypeHumanConfirm:
		return rt.executeHumanNode(ctx, msg, state, g, node, trace)
	default:
		node.Status = session.NodeStatusFailed
		node.FailureReason = fmt.Sprintf("unknown node type %q", node.Type)
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":    "node_execute_failed",
				"node_id": node.ID,
				"error":   node.FailureReason,
			})
		}
		return nil
	}
}

func (rt Runtime) executeModelNode(
	ctx context.Context,
	msg channel.InboundMessage,
	state *session.State,
	g *session.TaskGraph,
	node *session.TaskGraphNode,
	userText string,
	trace *traceRecorder,
) error {
	model := rt.Model
	systemPrompt := buildNodeSystemPrompt(node, g)

	reply, err := model.Next(ctx, agentcore.Context{
		SystemPrompt: systemPrompt,
		Messages: []agentcore.Message{
			{Role: agentcore.RoleUser, Content: node.Goal},
		},
	})
	if err != nil {
		node.Status = session.NodeStatusFailed
		node.FailureReason = err.Error()
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":    "node_execute_failed",
				"node_id": node.ID,
				"error":   err.Error(),
			})
		}
		return nil
	}

	node.ResultSummary = summarize(reply.Content)
	rt.verifyAndTraceNode(ctx, node, trace)
	return nil
}

func (rt Runtime) executeToolNode(
	ctx context.Context,
	msg channel.InboundMessage,
	state *session.State,
	g *session.TaskGraph,
	node *session.TaskGraphNode,
	trace *traceRecorder,
) error {
	executor := strings.TrimSpace(node.Executor)
	if executor == "" {
		node.Status = session.NodeStatusFailed
		node.FailureReason = "tool node has no executor"
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":    "node_execute_failed",
				"node_id": node.ID,
				"error":   node.FailureReason,
			})
		}
		return nil
	}

	tool, ok := rt.Tools.Get(executor)
	if !ok {
		node.Status = session.NodeStatusFailed
		node.FailureReason = fmt.Sprintf("tool %q not found in registry", executor)
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":    "node_execute_failed",
				"node_id": node.ID,
				"error":   node.FailureReason,
			})
		}
		return nil
	}

	call := buildToolCallFromNode(node, executor)
	if trace != nil {
		_ = trace.write(map[string]any{
			"type":      "node_tool_call",
			"node_id":   node.ID,
			"tool":      executor,
			"tool_args": redactPayload(cloneStringAnyMap(call.Args)),
		})
	}

	synthMsg := agentcore.Message{
		Role:      agentcore.RoleAssistant,
		ToolCalls: []agentcore.ToolCall{call},
	}

	result, blocked, err := rt.executeSingleTool(ctx, synthMsg, call, tool, state, g.TaskID, trace)
	if err != nil {
		node.Status = session.NodeStatusFailed
		node.FailureReason = err.Error()
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":    "node_execute_failed",
				"node_id": node.ID,
				"error":   err.Error(),
			})
		}
		return nil
	}

	node.EvidenceRefs = append(node.EvidenceRefs, session.EvidenceRef{
		Kind:      "tool",
		TraceID:   traceID(trace),
		TracePath: tracePath(trace),
		ToolName:  executor,
		Summary:   summarize(result.Content),
	})

	if blocked {
		node.Status = session.NodeStatusBlocked
		node.FailureReason = result.Content
	} else if result.IsError {
		node.Status = session.NodeStatusFailed
		node.FailureReason = summarize(result.Content)
	} else {
		node.ResultSummary = summarize(result.Content)
		rt.verifyAndTraceNode(ctx, node, trace)
		return nil
	}

	if trace != nil {
		_ = trace.write(map[string]any{
			"type":     "node_execute_result",
			"node_id":  node.ID,
			"status":   node.Status,
			"tool":     executor,
			"is_error": result.IsError,
			"summary":  node.ResultSummary,
		})
	}
	return nil
}

func (rt Runtime) executeSingleTool(
	ctx context.Context,
	msg agentcore.Message,
	call agentcore.ToolCall,
	tool agentcore.Tool,
	state *session.State,
	taskID string,
	trace *traceRecorder,
) (agentcore.ToolResult, bool, error) {
	maxRetries := runtimeToolRetryBudget(rt.Config, call.Name)

	if trace != nil {
		_ = trace.write(map[string]any{
			"type":    "tool_execution_start",
			"tool":    call.Name,
			"task_id": taskID,
		})
	}

	var lastResult agentcore.ToolResult
	var blocked bool
	attempt := 0

	deadline := runtimeToolTimeout(rt.Config, call.Name)

	for {
		attempt++

		attemptCtx := ctx
		attemptCancel := func() {}
		if deadline > 0 {
			attemptCtx, attemptCancel = context.WithTimeout(ctx, deadline)
		}

		beforeResult, err := rt.applyBeforeToolCall(attemptCtx, msg, call, tool, state, taskID, trace)
		if err != nil {
			attemptCancel()
			return agentcore.ToolResult{}, false, err
		}
		if beforeResult.Block {
			attemptCancel()
			content := beforeResult.Reason
			if content == "" {
				content = "tool execution blocked by policy"
			}
			return agentcore.ToolResult{
				ToolCallID: call.ID,
				Content:    content,
				IsError:    true,
			}, true, nil
		}
		if beforeResult.Context != nil {
			attemptCtx = beforeResult.Context
		}

		toolStart := time.Now()
		lastResult = rt.Tools.Execute(attemptCtx, call)
		elapsed := time.Since(toolStart)
		attemptCancel()

		if lastResult.Evidence == nil {
			lastResult.Evidence = map[string]any{}
		}
		lastResult.Evidence["elapsed_ms"] = elapsed.Milliseconds()
		if deadline > 0 {
			lastResult.Evidence["deadline_ms"] = deadline.Milliseconds()
			if attemptCtx.Err() == context.DeadlineExceeded {
				lastResult.IsError = true
				lastResult.Evidence["timed_out"] = true
				if strings.TrimSpace(lastResult.Content) == "" || strings.Contains(lastResult.Content, context.DeadlineExceeded.Error()) {
					lastResult.Content = fmt.Sprintf("tool %q timed out after %s", call.Name, deadline)
				}
			}
		}

		if !lastResult.IsError || attempt > maxRetries {
			break
		}
		if !agentcore.IsRetryableToolResult(call.Name, lastResult) {
			break
		}
	}

	if attempt > 1 {
		if lastResult.Evidence == nil {
			lastResult.Evidence = map[string]any{}
		}
		lastResult.Evidence["retry_count"] = attempt
	}

	afterResult := rt.applyAfterToolCall(ctx, msg, call, tool, lastResult, state, taskID, trace)
	if afterResult.ToolResult != nil {
		lastResult = *afterResult.ToolResult
	}

	if trace != nil {
		_ = trace.write(map[string]any{
			"type":     "tool_execution_end",
			"tool":     call.Name,
			"task_id":  taskID,
			"is_error": lastResult.IsError,
			"retries":  attempt - 1,
		})
	}

	return lastResult, blocked, nil
}

func (rt Runtime) applyBeforeToolCall(
	ctx context.Context,
	msg agentcore.Message,
	call agentcore.ToolCall,
	tool agentcore.Tool,
	state *session.State,
	taskID string,
	trace *traceRecorder,
) (agentcore.BeforeToolCallResult, error) {
	policy := rt.Hooks.toolPolicy(ctx, ToolPolicyHookInput{
		ToolCall: call,
		Tool:     tool,
		Config:   rt.Config,
	}, trace)
	if policy.Block {
		return agentcore.BeforeToolCallResult{
			Block:  true,
			Reason: policy.Reason,
		}, nil
	}
	return agentcore.BeforeToolCallResult{}, nil
}

func (rt Runtime) applyAfterToolCall(
	ctx context.Context,
	msg agentcore.Message,
	call agentcore.ToolCall,
	tool agentcore.Tool,
	result agentcore.ToolResult,
	state *session.State,
	taskID string,
	trace *traceRecorder,
) agentcore.AfterToolCallResult {
	rdResult := redactToolResult(result)
	compactResult := compactToolResultForModel(call, rdResult, rt.home(), traceID(trace))

	observeResult := rt.Hooks.observe(ctx, ObserveHookInput{
		Kind:       "tool_result",
		Home:       rt.home(),
		State:      *state,
		TaskID:     taskID,
		ToolCall:   call,
		Tool:       tool,
		ToolResult: compactResult,
	}, trace)
	if observeResult.TaskStep != nil {
		state.AddStep(taskID, *observeResult.TaskStep)
	}

	return agentcore.AfterToolCallResult{ToolResult: &compactResult}
}

func (rt Runtime) executeSkillNode(
	ctx context.Context,
	msg channel.InboundMessage,
	state *session.State,
	g *session.TaskGraph,
	node *session.TaskGraphNode,
	userText string,
	trace *traceRecorder,
) error {
	model := rt.Model

	skillName := strings.TrimSpace(node.Executor)
	if skillName == "" {
		node.Status = session.NodeStatusFailed
		node.FailureReason = "skill node has no executor"
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":    "node_execute_failed",
				"node_id": node.ID,
				"error":   node.FailureReason,
			})
		}
		return nil
	}

	registered, regErr := findRegisteredSkill(rt.Config, skillName)
	if registered == nil {
		reason := fmt.Sprintf("skill %q is not registered; only registered skills can be executed as nodes", skillName)
		if regErr != nil {
			reason = regErr.Error()
		}
		node.Status = session.NodeStatusFailed
		node.FailureReason = reason
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":    "node_execute_failed",
				"node_id": node.ID,
				"error":   node.FailureReason,
			})
		}
		return nil
	}

	if strings.EqualFold(registered.Granularity, "workflow") {
		node.Status = session.NodeStatusFailed
		node.FailureReason = fmt.Sprintf("skill %q has granularity=workflow and cannot be executed as a single node; it must be split into atomic nodes by the planner", skillName)
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":    "node_execute_failed",
				"node_id": node.ID,
				"error":   node.FailureReason,
			})
		}
		return nil
	}

	data, err := os.ReadFile(registered.Path)
	if err != nil {
		node.Status = session.NodeStatusFailed
		node.FailureReason = fmt.Sprintf("failed to read skill %q at %q: %v", skillName, registered.Path, err)
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":    "node_execute_failed",
				"node_id": node.ID,
				"error":   node.FailureReason,
			})
		}
		return nil
	}

	skillInstruction := string(data)
	systemPrompt := buildNodeSystemPrompt(node, g) + "\n\n--- Skill Instruction ---\n" + skillInstruction

	reply, err := model.Next(ctx, agentcore.Context{
		SystemPrompt: systemPrompt,
		Messages: []agentcore.Message{
			{Role: agentcore.RoleUser, Content: node.Goal},
		},
	})
	if err != nil {
		node.Status = session.NodeStatusFailed
		node.FailureReason = err.Error()
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":    "node_execute_failed",
				"node_id": node.ID,
				"error":   err.Error(),
			})
		}
		return nil
	}

	node.ResultSummary = summarize(reply.Content)
	rt.verifyAndTraceNode(ctx, node, trace)
	return nil
}

func findRegisteredSkill(cfg *config.Root, name string) (*discoveredSkill, error) {
	if cfg == nil {
		return nil, nil
	}
	nameLower := strings.ToLower(strings.TrimSpace(name))
	if nameLower == "" {
		return nil, nil
	}
	skills := discoverSkills(cfg, 0)
	for i := range skills {
		if strings.ToLower(strings.TrimSpace(skills[i].Name)) == nameLower {
			sk := &skills[i]
			if !hasMetadataYAML(sk.Path) {
				return nil, fmt.Errorf("skill %q has no .mateway/metadata.yaml; only registered skills can be executed as nodes", name)
			}
			if granularity, err := readGraphGranularityFromMeta(sk.Path); err == nil && strings.EqualFold(granularity, "workflow") {
				return nil, fmt.Errorf("skill %q has graph.granularity=workflow and cannot be executed as a single node; it must be split into atomic nodes by the planner", name)
			}
			return sk, nil
		}
	}
	return nil, nil
}

type graphSkillMeta struct {
	Graph struct {
		Granularity string `yaml:"granularity"`
	} `yaml:"graph"`
}

func readGraphGranularityFromMeta(skillMdPath string) (string, error) {
	dir := filepath.Dir(skillMdPath)
	metadataPath := filepath.Join(dir, ".mateway", "metadata.yaml")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return "", err
	}
	var meta graphSkillMeta
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return "", err
	}
	return strings.TrimSpace(meta.Graph.Granularity), nil
}

func hasMetadataYAML(skillMdPath string) bool {
	dir := filepath.Dir(skillMdPath)
	metadataPath := filepath.Join(dir, ".mateway", "metadata.yaml")
	_, err := os.Stat(metadataPath)
	return err == nil
}

func (rt Runtime) executeHumanNode(
	_ context.Context,
	_ channel.InboundMessage,
	state *session.State,
	g *session.TaskGraph,
	node *session.TaskGraphNode,
	trace *traceRecorder,
) error {
	kind := session.PendingKindHumanReview
	if node.Type == session.NodeTypeHumanConfirm {
		kind = session.PendingKindHumanConfirm
	}

	question := node.Goal
	if question == "" {
		question = node.Acceptance.Criteria
	}
	if question == "" {
		question = "Please review and confirm."
	}

	state.Pending = &session.PendingAction{
		Kind:     kind,
		TaskID:   g.TaskID,
		GraphID:  g.ID,
		NodeID:   node.ID,
		Question: question,
	}

	node.Status = session.NodeStatusAwaitingInput
	node.UpdatedAt = time.Now()

	if trace != nil {
		_ = trace.write(map[string]any{
			"type":         "node_execute_result",
			"node_id":      node.ID,
			"status":       node.Status,
			"pending_kind": string(kind),
		})
	}
	return nil
}

func buildToolCallFromNode(node *session.TaskGraphNode, executor string) agentcore.ToolCall {
	args := make(map[string]any)
	for k, v := range node.Input {
		args[k] = v
	}
	if args == nil {
		args = map[string]any{}
	}
	return agentcore.ToolCall{
		ID:   fmt.Sprintf("node-%s", node.ID),
		Name: executor,
		Args: args,
	}
}

func traceID(trace *traceRecorder) string {
	if trace == nil {
		return ""
	}
	return trace.id
}

func tracePath(trace *traceRecorder) string {
	if trace == nil {
		return ""
	}
	return trace.path
}

func buildNodeSystemPrompt(node *session.TaskGraphNode, g *session.TaskGraph) string {
	var sb strings.Builder
	sb.WriteString("You are executing a single step in a task graph.\n")
	sb.WriteString("Complete ONLY this step. Do not perform other steps beyond your assigned goal.\n\n")
	sb.WriteString(fmt.Sprintf("Your goal: %s\n", node.Goal))
	if node.Acceptance.Criteria != "" {
		sb.WriteString(fmt.Sprintf("Acceptance criteria: %s\n", node.Acceptance.Criteria))
	}
	if node.Input != nil && len(node.Input) > 0 {
		sb.WriteString("Input context:")
		for k, v := range node.Input {
			sb.WriteString(fmt.Sprintf("\n  %s: %v", k, v))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\nThis step is part of a task graph. ")
	sb.WriteString("Provide a concise result. Do not mention the graph structure unless relevant to your output.\n")
	_ = g
	return sb.String()
}

func cloneStringAnyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	clone := make(map[string]any, len(m))
	for k, v := range m {
		clone[k] = v
	}
	return clone
}

func isHumanNode(node *session.TaskGraphNode) bool {
	return node.Type == session.NodeTypeHumanReview || node.Type == session.NodeTypeHumanConfirm
}

func (rt Runtime) verifyAndTraceNode(ctx context.Context, node *session.TaskGraphNode, trace *traceRecorder) {
	if trace != nil {
		_ = trace.write(map[string]any{
			"type":    "node_verify_start",
			"node_id": node.ID,
		})
	}

	result := session.VerifyNode(node)

	if result.Status == session.VerificationPassed && node.Acceptance.Criteria != "" {
		modelResult := rt.verifyNodeWithModel(ctx, node, trace)
		if modelResult.Status != "" {
			result = modelResult
		}
	}

	if result.Status == session.VerificationNeedsInput && !isHumanNode(node) {
		result.Status = session.VerificationBlocked
		result.Reason = "model verifier requested input, but only human nodes can await input; blocked instead"
		result.Confidence = "low"
	}

	session.ApplyNodeVerification(node, result)

	if trace != nil {
		_ = trace.write(map[string]any{
			"type":          "node_verified",
			"node_id":       node.ID,
			"node_status":   node.Status,
			"verify_status": result.Status,
			"verify_reason": result.Reason,
			"verified":      node.Acceptance.Verified,
		})
	}
}
