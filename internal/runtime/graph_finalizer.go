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

const finalizerSystemPrompt = `You are a task finalizer. Summarize the completed graph nodes into a concise final answer for the user.

## Rules
- Only use verified node results. Do not invent or assume.
- If a node has acceptance criteria that was not verified, do not use its result.
- Keep the answer short and direct. Do not describe the graph structure.
- Do NOT suggest new actions, call tools, or modify the task.
- If input is required from the user, clearly state what is needed.
- Output only the final answer text. No JSON, no markdown blocks.`

func (rt Runtime) finalizeGraph(
	ctx context.Context,
	msg channel.InboundMessage,
	g *session.TaskGraph,
	vr session.GraphVerificationResult,
	trace *traceRecorder,
) session.GraphFinalizeResult {
	if trace != nil {
		_ = trace.write(map[string]any{
			"type":     "graph_finalize_start",
			"graph_id": g.ID,
			"task_id":  g.TaskID,
			"status":   vr.Status,
		})
	}

	result := session.FinalizeGraph(g, vr)

	if result.Status == session.FinalizeCompleted {
		if direct := directSingleNodeResult(g); direct != "" {
			result.ReplyText = direct
		} else if direct := finalCompletedNodeResult(g); direct != "" {
			result.ReplyText = direct
		}
	}

	if result.Status == session.FinalizeCompleted && rt.Model != nil {
		prompt := renderFinalizerPrompt(g, vr)
		finalCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if directSingleNodeResult(g) == "" && finalCompletedNodeResult(g) == "" {
			if trace != nil {
				_ = trace.write(map[string]any{
					"type":        "model_call_start",
					"model_stage": "finalizer",
					"graph_id":    g.ID,
					"task_id":     g.TaskID,
				})
			}
			reply, err := rt.Model.Next(finalCtx, agentcore.Context{
				SystemPrompt: rt.finalizerSystemPromptForMessage(msg),
				Messages:     []agentcore.Message{{Role: agentcore.RoleUser, Content: prompt}},
			})
			if err == nil && strings.TrimSpace(reply.Content) != "" {
				result.ReplyText = strings.TrimSpace(reply.Content)
			}
			if trace != nil {
				eventType := "model_call_end"
				payload := map[string]any{
					"type":        eventType,
					"model_stage": "finalizer",
					"graph_id":    g.ID,
					"task_id":     g.TaskID,
				}
				if err != nil {
					payload["type"] = "model_call_failed"
					payload["error"] = err.Error()
				}
				_ = trace.write(payload)
			}
		} else if trace != nil {
			_ = trace.write(map[string]any{
				"type":        "model_call_skipped",
				"model_stage": "finalizer",
				"graph_id":    g.ID,
				"task_id":     g.TaskID,
				"reason":      "direct_final_node_result",
			})
		}
	}

	if result.Status == session.FinalizeBlocked && trace != nil {
		_ = trace.write(map[string]any{
			"type":     "graph_blocked",
			"graph_id": g.ID,
			"task_id":  g.TaskID,
			"reason":   result.ReplyText,
		})
	}

	if trace != nil {
		_ = trace.write(map[string]any{
			"type":        "graph_finalized",
			"graph_id":    g.ID,
			"task_id":     g.TaskID,
			"status":      result.Status,
			"reply_style": result.ReplyStyle,
			"keep_task":   result.KeepTask,
		})
	}

	return result
}

func (rt Runtime) finalizerSystemPromptForMessage(msg channel.InboundMessage) string {
	profilePrompt := strings.TrimSpace(buildRuntimeSystemContext(rt.Config, rt.Pool.ProfileForMessage(msg)))
	if profilePrompt == "" {
		return finalizerSystemPrompt
	}
	return profilePrompt + "\n\nFinalizer context:\n" + finalizerSystemPrompt
}

func directSingleNodeResult(g *session.TaskGraph) string {
	if g == nil || len(g.Nodes) != 1 {
		return ""
	}
	n := g.Nodes[0]
	if !isFinalAnswerNode(n) || n.Status != session.NodeStatusCompleted {
		return ""
	}
	return nodeResultText(n)
}

func finalCompletedNodeResult(g *session.TaskGraph) string {
	if g == nil {
		return ""
	}
	downstream := make(map[string]bool)
	for _, n := range g.Nodes {
		for _, dep := range n.Depends {
			downstream[dep] = true
		}
	}
	var candidate *session.TaskGraphNode
	for i := len(g.Nodes) - 1; i >= 0; i-- {
		n := &g.Nodes[i]
		if downstream[n.ID] || !isFinalAnswerNode(*n) || n.Status != session.NodeStatusCompleted {
			continue
		}
		if nodeResultText(*n) == "" {
			continue
		}
		if candidate != nil {
			return ""
		}
		candidate = n
	}
	if candidate == nil {
		return ""
	}
	return nodeResultText(*candidate)
}

func isFinalAnswerNode(n session.TaskGraphNode) bool {
	switch n.Type {
	case session.NodeTypeModel, session.NodeTypeSubtask, session.NodeTypeSkill:
		return true
	default:
		return false
	}
}

func nodeResultText(n session.TaskGraphNode) string {
	if text, ok := n.Output["text"].(string); ok && strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(n.ResultSummary)
}

func (rt Runtime) FinalizeAndRespond(
	ctx context.Context,
	msg channel.InboundMessage,
	state *session.State,
	g *session.TaskGraph,
	vr session.GraphVerificationResult,
	trace *traceRecorder,
) (Response, error) {
	result := rt.finalizeGraph(ctx, msg, g, vr, trace)

	switch result.Status {
	case session.FinalizeCompleted:
		summary := summarize(result.ReplyText)
		state.CompleteActiveTaskWithSummary(summary, traceID(trace), tracePath(trace))
		state.AddExecutionEvent(g.TaskID, session.ExecutionEvent{
			Type:    "graph_completed",
			Status:  "completed",
			Summary: summary,
		})
		taskMemoryObserve(ctx, rt, state, g.TaskID, result.ReplyText, "task_completed", trace)

	case session.FinalizeFailed:
		state.BlockActiveTask("failed")
		state.AddExecutionEvent(g.TaskID, session.ExecutionEvent{
			Type:    "graph_failed",
			Status:  "failed",
			Summary: result.ReplyText,
		})
		taskMemoryObserve(ctx, rt, state, g.TaskID, result.ReplyText, "task_failed", trace)

	case session.FinalizeBlocked:
		state.AddExecutionEvent(g.TaskID, session.ExecutionEvent{
			Type:    "graph_blocked",
			Status:  "blocked",
			Summary: result.ReplyText,
		})
		taskMemoryObserve(ctx, rt, state, g.TaskID, result.ReplyText, "task_blocked", trace)

	case session.FinalizeAwaitingInput:
		ensurePendingForGraph(state, g)
		if state.Pending != nil && strings.TrimSpace(state.Pending.Question) != "" {
			result.ReplyText = state.Pending.Question
		}
		summary := summarize(result.ReplyText)
		state.AwaitUserInputActiveTaskWithSummary(summary, traceID(trace), tracePath(trace))

	case session.FinalizePartial:
	}

	state.ActiveTask = ""
	if result.KeepTask {
		state.ActiveTask = g.TaskID
	}

	finalText := result.ReplyText
	if finalText == "" {
		finalText = "Task processing complete."
	}

	redacted := redactSecretString(finalText)
	text := rt.Hooks.response(ctx, ResponseHookInput{RawText: redacted}, trace)
	text = redactSecretString(text)

	style := mapReplyStyle(result.ReplyStyle)

	if err := rt.saveState(state, trace); err != nil {
		return Response{}, err
	}

	if trace != nil {
		_ = trace.write(map[string]any{"type": "reply", "text": text, "style": string(style)})
	}

	failed := result.Status == session.FinalizeFailed || result.Status == session.FinalizeBlocked

	return Response{
		Reply: channel.OutboundMessage{
			Channel:  msg.Channel,
			ThreadID: msg.ThreadID,
			Text:     text,
			Style:    style,
		},
		TraceID:   traceID(trace),
		TracePath: tracePath(trace),
		Failed:    failed,
	}, nil
}

func taskMemoryObserve(
	ctx context.Context,
	rt Runtime,
	state *session.State,
	taskID string,
	finalText string,
	kind string,
	trace *traceRecorder,
) {
	home := rt.home()
	graphTask := state.TaskByID(taskID)
	var graphSummary *session.GraphMemorySummary
	if graphTask != nil && graphTask.Graph != nil {
		graphSummary = session.BuildGraphMemorySummary(graphTask.Graph, graphTask.Goal)
	}
	if trace != nil {
		_ = trace.write(map[string]any{
			"type":     "memory_observe_start",
			"kind":     kind,
			"task_id":  taskID,
			"graph_id": graphIDFromSummary(graphSummary),
		})
	}
	observe := rt.Hooks.observe(ctx, ObserveHookInput{
		Kind:         kind,
		Home:         home,
		SessionKey:   state.Key,
		State:        *state,
		TaskID:       taskID,
		FinalText:    finalText,
		TraceID:      traceID(trace),
		TracePath:    tracePath(trace),
		GraphSummary: graphSummary,
	}, trace)
	if observe.LearningResult != nil {
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":            "memory_written",
				"kind":            kind,
				"task_id":         taskID,
				"graph_id":        graphIDFromSummary(graphSummary),
				"diary_path":      observe.LearningResult.DiaryPath,
				"reflection_path": observe.LearningResult.ReflectionPath,
			})
		}
		if observe.LearningResult.Proposal != nil {
			state.Pending = &session.PendingAction{
				Kind:       "memory_proposal_review",
				TaskID:     taskID,
				ProposalID: observe.LearningResult.Proposal.ID,
				Question:   "1 save, 2 ignore",
			}
		}
	}
}

func graphIDFromSummary(summary *session.GraphMemorySummary) string {
	if summary == nil {
		return ""
	}
	return summary.GraphID
}

func ensurePendingForGraph(state *session.State, g *session.TaskGraph) {
	if state.Pending != nil {
		if state.Pending.GraphID == "" {
			state.Pending.GraphID = g.ID
		}
		return
	}

	var awaitingNode *session.TaskGraphNode
	for _, n := range g.Nodes {
		if n.Status == session.NodeStatusAwaitingInput {
			awaitingNode = &n
			break
		}
	}

	question := "Please provide input to continue the task."
	kind := session.PendingKindHumanReview
	if awaitingNode != nil {
		question = strings.TrimSpace(firstNonEmpty(awaitingNode.Acceptance.Criteria, awaitingNode.Goal, question))
		if awaitingNode.Type == session.NodeTypeHumanConfirm || awaitingNode.Type == session.NodeTypeHumanReview {
			question = strings.TrimSpace(question + "\n\nReply 1 to confirm and continue, or 2 to cancel and block this task.")
		}
		if awaitingNode.Type == session.NodeTypeHumanConfirm {
			kind = session.PendingKindHumanConfirm
		}
	}

	state.Pending = &session.PendingAction{
		Kind:     kind,
		TaskID:   g.TaskID,
		GraphID:  g.ID,
		NodeID:   nodeID(awaitingNode),
		Question: question,
	}
}

func nodeID(node *session.TaskGraphNode) string {
	if node == nil {
		return ""
	}
	return node.ID
}

func mapReplyStyle(finalizerStyle string) channel.MessageStyle {
	switch finalizerStyle {
	case "error":
		return channel.StyleError
	case "input_required":
		return channel.StyleInputRequired
	case "partial":
		return channel.StylePartial
	default:
		return ""
	}
}

func renderFinalizerPrompt(g *session.TaskGraph, vr session.GraphVerificationResult) string {
	var sb strings.Builder
	sb.WriteString("Produce a final answer based on the following verified node results.\n\n")
	sb.WriteString(fmt.Sprintf("Task Goal: %s\n\n", taskGoalFromGraph(g)))
	sb.WriteString("Verified Results:\n")
	for _, n := range g.Nodes {
		if n.Status != session.NodeStatusCompleted && n.Status != session.NodeStatusSkipped {
			continue
		}
		if n.Acceptance.Criteria != "" && !n.Acceptance.Verified {
			continue
		}
		sb.WriteString(fmt.Sprintf("- %s: %s\n", n.Goal, n.ResultSummary))
	}
	sb.WriteString("\nProvide a brief final answer for the user.")
	return sb.String()
}

func taskGoalFromGraph(g *session.TaskGraph) string {
	for _, n := range g.Nodes {
		if strings.TrimSpace(n.Goal) != "" {
			return n.Goal
		}
	}
	return g.TaskID
}
