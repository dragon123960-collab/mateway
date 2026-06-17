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

	return rt.executeNodeRun(ctx, msg, state, g, node, userText, trace)
}

func (rt Runtime) executeNodeRun(
	ctx context.Context,
	msg channel.InboundMessage,
	state *session.State,
	g *session.TaskGraph,
	node *session.TaskGraphNode,
	userText string,
	trace *traceRecorder,
) error {
	switch node.Type {
	case session.NodeTypeModel, session.NodeTypeSubtask:
		// TODO(task-graph-runtime): subtask/react nodes should run node-local
		// ReAct with allowed_tools constraint. For now they execute as single
		// model calls (stub for phase 01).
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
				"type":     "node_execute_failed",
				"graph_id": g.ID,
				"node_id":  node.ID,
				"error":    node.FailureReason,
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
	model := rt.modelForMessage(msg)
	systemPrompt := buildNodeSystemPrompt(node, g)
	content := renderModelNodeInput(g, node, userText)
	input := userAgentMessage(content, msg.Parts)

	reply, err := model.Next(ctx, agentcore.Context{
		SystemPrompt: systemPrompt,
		Messages: []agentcore.Message{
			input,
		},
	})
	if err != nil {
		node.Status = session.NodeStatusFailed
		node.FailureReason = err.Error()
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":     "node_execute_failed",
				"graph_id": g.ID,
				"node_id":  node.ID,
				"error":    err.Error(),
			})
		}
		return nil
	}

	state.Messages = redactMessagesForStorage(append(state.Messages, input, reply))
	if reply.Usage != nil {
		usage := session.Usage{}
		addUsage(&usage, usageFromMessages([]agentcore.Message{reply}))
		addUsage(&state.Usage, usage)
		writeUsageTrace(trace, usage)
	}
	if node.Output == nil {
		node.Output = make(map[string]any)
	}
	node.Output["text"] = redactSecretString(reply.Content)
	node.ResultSummary = summarize(reply.Content)
	if node.ResultSummary == "" && len(reply.ToolCalls) > 0 {
		for _, tc := range reply.ToolCalls {
			if tc.Name != "" {
				node.ResultSummary = fmt.Sprintf("tool call: %s", tc.Name)
				break
			}
		}
	}
	rt.verifyAndTraceNode(ctx, g.ID, node, trace)
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
				"type":     "node_execute_failed",
				"graph_id": g.ID,
				"node_id":  node.ID,
				"error":    node.FailureReason,
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
				"type":     "node_execute_failed",
				"graph_id": g.ID,
				"node_id":  node.ID,
				"error":    node.FailureReason,
			})
		}
		return nil
	}

	call := buildToolCallFromNode(node, executor)
	if trace != nil {
		_ = trace.write(map[string]any{
			"type":      "node_tool_call",
			"graph_id":  g.ID,
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
				"type":     "node_execute_failed",
				"graph_id": g.ID,
				"node_id":  node.ID,
				"error":    err.Error(),
			})
		}
		return nil
	}

	node.EvidenceRefs = append(node.EvidenceRefs, session.EvidenceRef{
		Kind:      "tool",
		TraceID:   traceID(trace),
		TracePath: tracePath(trace),
		ToolName:  executor,
		Summary:   summarizeToolEvidence(result),
	})

	if blocked {
		node.Status = session.NodeStatusBlocked
		node.FailureReason = result.Content
	} else if result.IsError {
		node.Status = session.NodeStatusFailed
		node.FailureReason = summarize(result.Content)
	} else {
		node.ResultSummary = summarize(result.Content)
		rt.verifyAndTraceNode(ctx, g.ID, node, trace)
		return nil
	}

	if trace != nil {
		_ = trace.write(map[string]any{
			"type":     "node_execute_result",
			"graph_id": g.ID,
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
				"type":     "node_execute_failed",
				"graph_id": g.ID,
				"node_id":  node.ID,
				"error":    node.FailureReason,
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
				"type":     "node_execute_failed",
				"graph_id": g.ID,
				"node_id":  node.ID,
				"error":    node.FailureReason,
			})
		}
		return nil
	}

	if strings.EqualFold(registered.Granularity, "workflow") {
		node.Status = session.NodeStatusFailed
		node.FailureReason = fmt.Sprintf("skill %q has granularity=workflow and cannot be executed as a single node; it must be split into atomic nodes by the planner", skillName)
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":     "node_execute_failed",
				"graph_id": g.ID,
				"node_id":  node.ID,
				"error":    node.FailureReason,
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
				"type":     "node_execute_failed",
				"graph_id": g.ID,
				"node_id":  node.ID,
				"error":    node.FailureReason,
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
				"type":     "node_execute_failed",
				"graph_id": g.ID,
				"node_id":  node.ID,
				"error":    err.Error(),
			})
		}
		return nil
	}

	node.ResultSummary = summarize(reply.Content)
	rt.verifyAndTraceNode(ctx, g.ID, node, trace)
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
	question = strings.TrimSpace(question + "\n\nReply 1 to confirm and continue, or 2 to cancel and block this task.")

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
			"graph_id":     g.ID,
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

func renderNodeDependencyContext(g *session.TaskGraph, node *session.TaskGraphNode) string {
	if g == nil || node == nil || len(node.Depends) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Completed dependency results:\n")
	for _, depID := range node.Depends {
		dep := g.NodeByID(depID)
		if dep == nil || dep.Status != session.NodeStatusCompleted {
			continue
		}
		result := strings.TrimSpace(dep.ResultSummary)
		if text, ok := dep.Output["text"].(string); ok && strings.TrimSpace(text) != "" {
			result = strings.TrimSpace(text)
		}
		if result == "" {
			continue
		}
		sb.WriteString("- ")
		sb.WriteString(dep.ID)
		sb.WriteString(": ")
		sb.WriteString(result)
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

func renderModelNodeInput(g *session.TaskGraph, node *session.TaskGraphNode, userText string) string {
	var sb strings.Builder
	sb.WriteString("Current node goal:\n")
	sb.WriteString(strings.TrimSpace(node.Goal))
	if strings.TrimSpace(userText) != "" {
		sb.WriteString("\n\nOriginal user request:\n")
		sb.WriteString(strings.TrimSpace(userText))
	}
	if dependencyContext := renderNodeDependencyContext(g, node); dependencyContext != "" {
		sb.WriteString("\n\n")
		sb.WriteString(dependencyContext)
	}
	sb.WriteString("\n\nProduce only the output needed to satisfy this node's goal and acceptance criteria.")
	return strings.TrimSpace(sb.String())
}

func summarizeToolEvidence(result agentcore.ToolResult) string {
	parts := []string{}
	if text := strings.TrimSpace(redactSecretString(result.Content)); text != "" {
		parts = append(parts, text)
	} else if !result.IsError && strings.TrimSpace(fmt.Sprint(result.Evidence["command"])) != "" {
		parts = append(parts, "command completed successfully with no output")
	}
	evidence := redactMapEvidence(result.Evidence)
	for _, key := range []string{
		"path", "bytes", "sha256", "content_preview",
		"command", "decision", "policy_classification", "elapsed_ms", "deadline_ms", "output_truncated", "workdir",
	} {
		value, ok := evidence[key]
		if !ok || value == nil {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(value))
		if text == "" {
			continue
		}
		parts = append(parts, key+"="+text)
	}
	if len(parts) == 0 {
		return ""
	}
	return trimAndTruncateRunesWithSuffix(strings.Join(parts, "; "), 480)
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

func (rt Runtime) verifyAndTraceNode(ctx context.Context, graphID string, node *session.TaskGraphNode, trace *traceRecorder) {
	if trace != nil {
		_ = trace.write(map[string]any{
			"type":     "node_verify_start",
			"graph_id": graphID,
			"node_id":  node.ID,
		})
	}

	result := session.VerifyNode(node)

	if result.Status == session.VerificationPassed && node.Acceptance.Criteria != "" {
		modelResult := rt.verifyNodeWithModel(ctx, graphID, node, trace)
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
			"graph_id":      graphID,
			"node_id":       node.ID,
			"node_status":   node.Status,
			"verify_status": result.Status,
			"verify_reason": result.Reason,
			"verified":      node.Acceptance.Verified,
		})
	}
}
