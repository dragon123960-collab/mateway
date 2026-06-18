package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/tool"
)

type Runtime struct {
	Config        *config.Root
	Store         session.Store
	Tools         *agentcore.ToolRegistry
	Model         agentcore.Model
	ContractModel agentcore.Model
	Pool          AgentPool
	Hooks         RuntimeHooks
	ProgressSink  func(channel.OutboundMessage)
}

type Response struct {
	Reply     channel.OutboundMessage
	FollowUps []channel.OutboundMessage
	TraceID   string
	TracePath string
	Failed    bool
}

func New(cfg *config.Root) Runtime {
	hooks := defaultRuntimeHooks()
	hooks.Providers = append(hooks.Providers, staticContextHookProvider{config: cfg})
	hooks.Providers = append(hooks.Providers, memorySafeReadHookProvider{config: cfg})
	hooks.Providers = append(hooks.Providers, defaultToolPolicyHookProvider{})
	hooks.Providers = append(hooks.Providers, defaultObserveHookProvider{})
	hooks.Providers = append(hooks.Providers, defaultResponseHookProvider{})
	model := agentcore.Model(HeuristicModel{})
	home := ""
	if cfg != nil {
		model = resolveModelForDefault(cfg)
		home = cfg.App.Home
	}
	return Runtime{
		Config: cfg,
		Store:  session.NewStore(home),
		Tools:  tool.NewRegistry(cfg),
		Model:  model,
		Pool:   NewAgentPool(cfg),
		Hooks:  hooks,
	}
}

func (rt Runtime) Handle(ctx context.Context, msg channel.InboundMessage) (Response, error) {
	start := time.Now()
	state, err := rt.Store.Load(msg.SessionKey)
	if err != nil {
		return Response{}, err
	}
	trace, err := newTraceRecorder(rt.Config)
	if err != nil {
		return Response{}, err
	}
	trace.setIdentity(traceIdentityForMessage(msg))
	defer func() {
		_ = trace.write(map[string]any{"type": "runtime_done", "duration_ms": time.Since(start).Milliseconds()})
	}()
	if IsNewSessionCommand(msg.Text) {
		return rt.resetSession(msg, state, trace, start)
	}
	if resp, handled, err := rt.handlePending(ctx, &state, msg, trace); handled || err != nil {
		if handled && err == nil {
			resp.TraceID = trace.id
			resp.TracePath = trace.path
			_ = trace.write(map[string]any{"type": "reply", "text": resp.Reply.Text, "style": resp.Reply.Style, "runtime_duration_ms": time.Since(start).Milliseconds()})
		}
		return resp, err
	}
	userText := strings.TrimSpace(msg.Text)
	decision := determineContinuation(state, userText)

	if activeTask := state.TaskByID(state.ActiveTask); activeTask != nil && activeTask.Graph != nil {
		decision = graphAwareContinuation(state, userText, activeTask)
	}

	_ = trace.write(map[string]any{
		"type":         "continuation_decision",
		"action":       decision.Action,
		"task_id":      decision.TaskID,
		"node_id":      decision.NodeID,
		"reason":       decision.Reason,
		"context_refs": decision.ContextRefs,
	})

	switch decision.Action {
	case ActionResumeNode, ActionContinueGraph:
		if decision.TaskID != "" {
			state.ActivateTask(decision.TaskID)
		}
	case ActionNewGraph, ActionReferenceCompleted, ActionHistoricalSearch:
		state.ActiveTask = ""
	}

	task := state.EnsureTask(msg.Text)
	if len(decision.ContextRefs) > 0 {
		state.SetTaskContextRefs(task.ID, decision.ContextRefs)
		if trace != nil {
			_ = trace.write(map[string]any{
				"type":         "context_refs_attached",
				"task_id":      task.ID,
				"context_refs": decision.ContextRefs,
			})
		}
	}
	if task.Goal != userText && state.ActiveTask == task.ID {
		userText = mergeTaskAndInstruction(task.Goal, userText)
	}

	phase := tracePhaseExecute
	if decision.Action == ActionContinueGraph || decision.Action == ActionResumeNode {
		phase = tracePhaseFollowupExecute
	}

	profile := rt.Pool.ProfileForMessage(msg)
	trace.setIdentity(map[string]any{"agent_id": profile.ID, "task_id": task.ID})
	_ = trace.write(map[string]any{"type": "request", "text": msg.Text, "effective_text": userText})
	state.AddTraceRef(task.ID, session.TraceRef{TraceID: trace.id, TracePath: trace.path, Phase: phase, MessageID: msg.ID})

	if err := rt.ensureGraphForTask(ctx, msg, &state, task, userText, trace); err != nil {
		if isTransientBootstrapError(err) {
			state.AwaitUserInputActiveTaskWithSummary(friendlyRuntimeError(rt.Config, msg, err), trace.id, trace.path)
		} else {
			state.BlockActiveTask("failed")
		}
		if saveErr := rt.saveState(&state, trace); saveErr != nil {
			return Response{}, saveErr
		}
		text := friendlyRuntimeError(rt.Config, msg, err)
		resp := Response{
			Reply: channel.OutboundMessage{
				Channel:  msg.Channel,
				ThreadID: msg.ThreadID,
				Text:     text,
				Style:    channel.StyleError,
			},
			TraceID:   trace.id,
			TracePath: trace.path,
			Failed:    true,
		}
		_ = trace.write(map[string]any{"type": "graph_bootstrap_failed", "task_id": task.ID, "error": err.Error()})
		_ = trace.write(map[string]any{"type": "reply", "text": resp.Reply.Text, "style": resp.Reply.Style})
		return resp, nil
	}

	return rt.runGraphTask(ctx, msg, &state, task, userText, trace)
}

func isTransientBootstrapError(err error) bool {
	if err == nil {
		return false
	}
	raw := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(raw, "context deadline exceeded") ||
		strings.Contains(raw, "client.timeout") ||
		strings.Contains(raw, "temporarily unavailable") ||
		strings.Contains(raw, "rate limit") ||
		strings.Contains(raw, "429")
}

func (rt Runtime) runGraphTask(
	ctx context.Context,
	msg channel.InboundMessage,
	state *session.State,
	task *session.TaskNode,
	userText string,
	trace *traceRecorder,
) (Response, error) {
	g := task.Graph

	session.RecoverRunningNodes(g)
	session.UpdateGraphStatus(g)
	if trace != nil {
		_ = trace.write(map[string]any{
			"type":     "graph_recovery_normalized",
			"graph_id": g.ID,
			"task_id":  task.ID,
			"status":   g.Status,
			"nodes":    graphNodeStatusSnapshot(g),
		})
	}

	_ = trace.write(map[string]any{
		"type":     "graph_lifecycle_start",
		"graph_id": g.ID,
		"task_id":  task.ID,
		"nodes":    len(g.Nodes),
	})

	for {
		ready := session.ReadyNodes(g, len(g.Nodes))
		selected, waiting := selectReadyNodeBatch(g, ready, maxParallelNodes(rt.Config))

		_ = trace.write(map[string]any{
			"type":        "graph_schedule_tick",
			"graph_id":    g.ID,
			"task_id":     task.ID,
			"ready_nodes": ready,
			"selected":    selected,
			"waiting":     waiting,
			"total_nodes": len(g.Nodes),
		})
		_ = trace.write(map[string]any{
			"type":               "scheduler_tick",
			"graph_id":           g.ID,
			"task_id":            task.ID,
			"ready_nodes":        ready,
			"selected_nodes":     selected,
			"waiting_nodes":      waiting,
			"max_parallel_nodes": maxParallelNodes(rt.Config),
		})
		for _, nodeID := range ready {
			node := g.NodeByID(nodeID)
			if node == nil {
				continue
			}
			_ = trace.write(map[string]any{
				"type":      "node_ready",
				"graph_id":  g.ID,
				"task_id":   task.ID,
				"node_id":   node.ID,
				"node_type": node.Type,
				"node_mode": node.Mode,
				"attempt":   node.Attempts + 1,
			})
		}
		for _, nodeID := range waiting {
			node := g.NodeByID(nodeID)
			if node == nil {
				continue
			}
			_ = trace.write(map[string]any{
				"type":      "scheduler_waiting",
				"graph_id":  g.ID,
				"task_id":   task.ID,
				"node_id":   node.ID,
				"node_type": node.Type,
				"node_mode": node.Mode,
				"attempt":   node.Attempts + 1,
				"reason":    schedulerWaitingReason(g, node, selected, maxParallelNodes(rt.Config)),
			})
		}

		if len(selected) == 0 {
			break
		}

		for _, nodeID := range selected {
			node := g.NodeByID(nodeID)
			if node == nil {
				continue
			}
			if node.Status != session.NodeStatusPending {
				continue
			}

			_ = trace.write(map[string]any{
				"type":      "node_scheduled",
				"graph_id":  g.ID,
				"task_id":   task.ID,
				"node_id":   node.ID,
				"node_type": node.Type,
				"node_mode": node.Mode,
				"attempt":   node.Attempts + 1,
			})

			startNodeAttempt(trace, g, node)
		}

		if err := rt.saveState(state, trace); err != nil {
			return Response{}, err
		}

		results, err := rt.runSelectedNodes(ctx, msg, state, g, selected, userText, trace)
		if err != nil {
			return Response{}, err
		}
		for _, result := range results {
			mergeNodeRunResult(state, g, result)
			node := g.NodeByID(result.NodeID)
			if node == nil {
				continue
			}
			if node.Status == session.NodeStatusCompleted {
				writeNodeEvent(trace, g, node, map[string]any{
					"type":           "node_completed",
					"status":         node.Status,
					"result_summary": node.ResultSummary,
				})
			}
			if node.Status == session.NodeStatusRetrying {
				node.Status = session.NodeStatusPending
				_ = trace.write(map[string]any{
					"type":     "node_retry_scheduled",
					"graph_id": g.ID,
					"task_id":  task.ID,
					"node_id":  node.ID,
					"attempt":  node.Attempts,
				})
			}
			if shouldApplyLocalReplan(node) {
				if localReplanDepth(node) >= 1 {
					node.Status = session.NodeStatusFailed
					_ = trace.write(map[string]any{
						"type":     "local_replan_limit_reached",
						"graph_id": g.ID,
						"task_id":  task.ID,
						"node_id":  node.ID,
						"reason":   node.FailureReason,
					})
					session.UpdateGraphStatus(g)
					if err := rt.saveState(state, trace); err != nil {
						return Response{}, err
					}
					continue
				}
				replacement := localReplanReplacementNode(node)
				if errs := applyLocalReplanWithTrace(g, session.LocalReplanRequest{
					FailedNodeID:     node.ID,
					ReplacementNodes: []session.TaskGraphNode{replacement},
				}, trace); !errs.IsValid() {
					node.Status = session.NodeStatusFailed
					node.FailureReason = errs.Error()
					_ = trace.write(map[string]any{
						"type":     "local_replan_blocker",
						"graph_id": g.ID,
						"task_id":  task.ID,
						"node_id":  node.ID,
						"error":    errs.Error(),
					})
				}
			}

			session.UpdateGraphStatus(g)

			if err := rt.saveState(state, trace); err != nil {
				return Response{}, err
			}
		}

	}

	vr := session.VerifyTaskGraphWithContract(g, task.Execution.Contract)

	return rt.FinalizeAndRespond(ctx, msg, state, g, vr, trace)
}

func localReplanReplacementNode(node *session.TaskGraphNode) session.TaskGraphNode {
	now := time.Now()
	id := "repair-" + strings.TrimSpace(node.ID)
	if id == "repair-" {
		id = fmt.Sprintf("repair-%d", now.UnixNano())
	}
	goal := strings.TrimSpace(node.Goal)
	if goal == "" {
		goal = "complete the failed node with a smaller, verifiable output"
	}
	reason := strings.TrimSpace(node.FailureReason)
	if reason == "" {
		reason = "previous node attempt was not accepted by verifier"
	}
	mode := session.NodeModeDirect
	if len(node.AllowedTools) > 0 || strings.TrimSpace(node.Mode) == session.NodeModeReact {
		mode = session.NodeModeReact
	}
	return session.TaskGraphNode{
		ID:      id,
		Type:    session.NodeTypeSubtask,
		Mode:    mode,
		Goal:    "Repair and complete: " + goal,
		Status:  session.NodeStatusPending,
		Depends: append([]string(nil), node.Depends...),
		Input: map[string]any{
			"replan_reason":      reason,
			"local_replan_depth": localReplanDepth(node) + 1,
		},
		Output: map[string]any{
			"repair_result": true,
		},
		Acceptance: session.Acceptance{
			Criteria: "Produces a concise result that directly addresses the original node goal and verifier feedback.",
		},
		AllowedTools: append([]string(nil), node.AllowedTools...),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

func shouldApplyLocalReplan(node *session.TaskGraphNode) bool {
	if node == nil {
		return false
	}
	if node.Status == session.NodeStatusNeedsReplan {
		return true
	}
	if node.Status != session.NodeStatusFailed {
		return false
	}
	switch node.Type {
	case session.NodeTypeModel, session.NodeTypeSubtask, session.NodeTypeSkill:
		return true
	default:
		return false
	}
}

func localReplanDepth(node *session.TaskGraphNode) int {
	if node == nil || node.Input == nil {
		return 0
	}
	switch v := node.Input["local_replan_depth"].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return 0
}

type nodeRunResult struct {
	NodeID       string
	Node         session.TaskGraphNode
	Messages     []agentcore.Message
	Usage        session.Usage
	Pending      *session.PendingAction
	TaskStepTail []session.TaskStep
	Err          error
}

func (rt Runtime) runSelectedNodes(
	ctx context.Context,
	msg channel.InboundMessage,
	state *session.State,
	g *session.TaskGraph,
	selected []string,
	userText string,
	trace *traceRecorder,
) ([]nodeRunResult, error) {
	results := make([]nodeRunResult, len(selected))
	var wg sync.WaitGroup
	for i, nodeID := range selected {
		node := g.NodeByID(nodeID)
		if node == nil || node.Status != session.NodeStatusRunning {
			continue
		}
		baseMessages := len(state.Messages)
		baseTaskSteps := taskStepCount(state, g.TaskID)
		sandboxState := cloneStateForNodeRun(*state)
		sandboxGraph := cloneTaskGraph(g)
		sandboxNode := sandboxGraph.NodeByID(nodeID)
		if sandboxNode == nil {
			continue
		}
		wg.Add(1)
		go func(index int, nodeID string, baseMessages, baseTaskSteps int, runState session.State, runGraph session.TaskGraph, runNode *session.TaskGraphNode) {
			defer wg.Done()
			err := rt.executeNodeRun(ctx, msg, &runState, &runGraph, runNode, userText, trace)
			results[index] = nodeRunResult{
				NodeID:       nodeID,
				Node:         *runNode,
				Messages:     appendedMessages(runState.Messages, baseMessages),
				Usage:        usageDelta(state.Usage, runState.Usage),
				Pending:      clonePending(runState.Pending),
				TaskStepTail: appendedTaskSteps(runState, g.TaskID, baseTaskSteps),
				Err:          err,
			}
		}(i, nodeID, baseMessages, baseTaskSteps, sandboxState, sandboxGraph, sandboxNode)
	}
	wg.Wait()
	for _, result := range results {
		if result.Err != nil {
			return results, result.Err
		}
	}
	return results, nil
}

func mergeNodeRunResult(state *session.State, g *session.TaskGraph, result nodeRunResult) {
	if state == nil || g == nil || result.NodeID == "" {
		return
	}
	if node := g.NodeByID(result.NodeID); node != nil {
		*node = result.Node
	}
	if len(result.Messages) > 0 {
		state.Messages = append(state.Messages, result.Messages...)
	}
	addUsage(&state.Usage, result.Usage)
	if result.Pending != nil {
		state.Pending = result.Pending
	}
	for _, step := range result.TaskStepTail {
		state.AddStep(g.TaskID, step)
	}
}

func cloneStateForNodeRun(state session.State) session.State {
	state.Messages = append([]agentcore.Message(nil), state.Messages...)
	state.Tasks = append([]session.TaskNode(nil), state.Tasks...)
	for i := range state.Tasks {
		state.Tasks[i].Steps = append([]session.TaskStep(nil), state.Tasks[i].Steps...)
		if state.Tasks[i].Graph != nil {
			graph := cloneTaskGraph(state.Tasks[i].Graph)
			state.Tasks[i].Graph = &graph
		}
		if state.Tasks[i].Execution.TraceRefs != nil {
			state.Tasks[i].Execution.TraceRefs = append([]session.TraceRef(nil), state.Tasks[i].Execution.TraceRefs...)
		}
		if state.Tasks[i].Execution.Events != nil {
			state.Tasks[i].Execution.Events = append([]session.ExecutionEvent(nil), state.Tasks[i].Execution.Events...)
		}
	}
	if state.Pending != nil {
		state.Pending = clonePending(state.Pending)
	}
	return state
}

func cloneTaskGraph(g *session.TaskGraph) session.TaskGraph {
	if g == nil {
		return session.TaskGraph{}
	}
	clone := *g
	clone.Nodes = append([]session.TaskGraphNode(nil), g.Nodes...)
	for i := range clone.Nodes {
		clone.Nodes[i].Depends = append([]string(nil), clone.Nodes[i].Depends...)
		clone.Nodes[i].AllowedTools = append([]string(nil), clone.Nodes[i].AllowedTools...)
		clone.Nodes[i].Input = cloneAnyMap(clone.Nodes[i].Input)
		clone.Nodes[i].Output = cloneAnyMap(clone.Nodes[i].Output)
		clone.Nodes[i].EvidenceRefs = append([]session.EvidenceRef(nil), clone.Nodes[i].EvidenceRefs...)
	}
	return clone
}

func cloneAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}

func clonePending(pending *session.PendingAction) *session.PendingAction {
	if pending == nil {
		return nil
	}
	clone := *pending
	return &clone
}

func appendedMessages(messages []agentcore.Message, base int) []agentcore.Message {
	if base < 0 || base >= len(messages) {
		if base == len(messages) {
			return nil
		}
		return append([]agentcore.Message(nil), messages...)
	}
	return append([]agentcore.Message(nil), messages[base:]...)
}

func taskStepCount(state *session.State, taskID string) int {
	if state == nil {
		return 0
	}
	if task := state.TaskByID(taskID); task != nil {
		return len(task.Steps)
	}
	return 0
}

func appendedTaskSteps(state session.State, taskID string, base int) []session.TaskStep {
	task := state.TaskByID(taskID)
	if task == nil {
		return nil
	}
	if base < 0 || base >= len(task.Steps) {
		if base == len(task.Steps) {
			return nil
		}
		return append([]session.TaskStep(nil), task.Steps...)
	}
	return append([]session.TaskStep(nil), task.Steps[base:]...)
}

func usageDelta(base, next session.Usage) session.Usage {
	return session.Usage{
		Requests:             next.Requests - base.Requests,
		InputTokens:          next.InputTokens - base.InputTokens,
		OutputTokens:         next.OutputTokens - base.OutputTokens,
		TotalTokens:          next.TotalTokens - base.TotalTokens,
		EstimatedInputTokens: next.EstimatedInputTokens - base.EstimatedInputTokens,
		SavedEstimatedTokens: next.SavedEstimatedTokens - base.SavedEstimatedTokens,
		CompactedMessages:    next.CompactedMessages - base.CompactedMessages,
		CompactedToolResults: next.CompactedToolResults - base.CompactedToolResults,
		CacheHits:            next.CacheHits - base.CacheHits,
		CacheReadTokens:      next.CacheReadTokens - base.CacheReadTokens,
		CacheWriteTokens:     next.CacheWriteTokens - base.CacheWriteTokens,
		CacheInputTokens:     next.CacheInputTokens - base.CacheInputTokens,
		CacheOutputTokens:    next.CacheOutputTokens - base.CacheOutputTokens,
		Cost:                 next.Cost - base.Cost,
	}
}

func graphNodeStatusSnapshot(g *session.TaskGraph) []map[string]any {
	if g == nil {
		return nil
	}
	out := make([]map[string]any, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		out = append(out, map[string]any{
			"node_id":  n.ID,
			"status":   n.Status,
			"attempts": n.Attempts,
		})
	}
	return out
}

func maxParallelNodes(cfg *config.Root) int {
	if cfg == nil {
		return 1
	}
	return cfg.Execution.MaxParallelNodesValue()
}

func selectReadyNodeBatch(g *session.TaskGraph, ready []string, maxParallel int) ([]string, []string) {
	if g == nil || maxParallel <= 0 || len(ready) == 0 {
		return nil, nil
	}
	selected := make([]string, 0, minInt(maxParallel, len(ready)))
	waiting := make([]string, 0, len(ready))
	hasSensitive := false
	for _, nodeID := range ready {
		node := g.NodeByID(nodeID)
		if node == nil {
			continue
		}
		sensitive := isParallelSensitiveNode(node)
		if len(selected) >= maxParallel {
			waiting = append(waiting, nodeID)
			continue
		}
		if sensitive && len(selected) > 0 {
			waiting = append(waiting, nodeID)
			continue
		}
		if hasSensitive {
			waiting = append(waiting, nodeID)
			continue
		}
		selected = append(selected, nodeID)
		if sensitive {
			hasSensitive = true
		}
	}
	return selected, waiting
}

func isParallelSensitiveNode(node *session.TaskGraphNode) bool {
	if node == nil {
		return true
	}
	if node.Type == session.NodeTypeHumanConfirm || node.Type == session.NodeTypeHumanReview || strings.EqualFold(strings.TrimSpace(node.Mode), session.NodeModeHuman) {
		return true
	}
	for _, key := range []string{"risk", "mutation", "human_gate", "requires_human_confirmation"} {
		value, ok := node.Input[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case bool:
			if v {
				return true
			}
		case string:
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "high", "dangerous", "guarded_mutation", "mutation", "true", "yes", "required", "confirm":
				return true
			}
		}
	}
	return false
}

func schedulerWaitingReason(g *session.TaskGraph, node *session.TaskGraphNode, selected []string, maxParallel int) string {
	if node == nil {
		return "unknown"
	}
	if isParallelSensitiveNode(node) {
		for _, selectedID := range selected {
			selectedNode := g.NodeByID(selectedID)
			if selectedNode != nil && selectedNode.ID != node.ID && isParallelSensitiveNode(selectedNode) {
				return "parallel_sensitive_node"
			}
		}
		if len(selected) > 0 && selected[0] != node.ID {
			return "parallel_sensitive_node"
		}
	}
	if len(selected) >= maxParallel {
		return "max_parallel_nodes"
	}
	return "scheduler_batch_limit"
}

func renderPartialReply(cfg *config.Root, msg channel.InboundMessage, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return runtimeText(cfg, msg, "runtime.partial.empty", nil)
	}
	if containsAny(strings.ToLower(text), runtimeCueList(cfg, "router.partial.already_marked")) {
		return text
	}
	return runtimeText(cfg, msg, "runtime.partial.prefix", textValues("text", text))
}

func (rt Runtime) saveState(state *session.State, trace *traceRecorder) error {
	if state == nil {
		return nil
	}
	compacted, stats := compactMessagesForStorage(redactMessagesForStorage(state.Messages))
	state.Messages = compacted
	writeCompactTrace(trace, "session_compacted", stats)
	return rt.Store.Save(*state)
}

func (rt Runtime) resetSession(msg channel.InboundMessage, state session.State, trace *traceRecorder, start time.Time) (Response, error) {
	archivePath := ""
	if hasSessionState(state) {
		path, err := rt.Store.Archive(state)
		if err != nil {
			return Response{}, err
		}
		archivePath = path
		_ = trace.write(map[string]any{"type": "session_archived", "path": path, "session_key": state.Key, "messages": len(state.Messages), "tasks": len(state.Tasks)})
	}
	reset := session.State{Key: state.Key}
	if reset.Key == "" {
		reset.Key = msg.SessionKey
	}
	if err := rt.saveState(&reset, trace); err != nil {
		return Response{}, err
	}
	_ = trace.write(map[string]any{"type": "session_reset", "session_key": reset.Key, "archive_path": archivePath})
	text := runtimeText(rt.Config, msg, "runtime.session_reset.done", nil)
	if archivePath != "" {
		text += "\n" + runtimeText(rt.Config, msg, "runtime.session_reset.archived", textValues("archive_path", archivePath))
	}
	resp := rt.reply(msg, text, channel.StyleSessionReset)
	resp.TraceID = trace.id
	resp.TracePath = trace.path
	_ = trace.write(map[string]any{"type": "reply", "text": resp.Reply.Text, "style": resp.Reply.Style, "runtime_duration_ms": time.Since(start).Milliseconds()})
	return resp, nil
}

func hasSessionState(state session.State) bool {
	return len(state.Messages) > 0 || len(state.Tasks) > 0 || state.Pending != nil || state.ActiveTask != "" || state.Usage.Requests > 0
}

func writeCompactTrace(trace *traceRecorder, eventType string, stats messageCompactStats) {
	if trace == nil {
		return
	}
	if stats.BeforeMessages == 0 && stats.AfterMessages == 0 {
		return
	}
	_ = trace.write(map[string]any{
		"type":             eventType,
		"before_messages":  stats.BeforeMessages,
		"after_messages":   stats.AfterMessages,
		"before_chars":     stats.BeforeChars,
		"after_chars":      stats.AfterChars,
		"truncated_tools":  stats.TruncatedTools,
		"dropped_system":   stats.DroppedSystem,
		"dropped_old_msgs": stats.DroppedOld,
	})
}

func userAgentMessage(text string, parts []channel.MessagePart) agentcore.Message {
	msg := agentcore.Message{Role: agentcore.RoleUser, Content: strings.TrimSpace(text)}
	msg.Parts = channelPartsToAgentParts(text, parts)
	return msg
}

func channelPartsToAgentParts(text string, parts []channel.MessagePart) []agentcore.MessagePart {
	out := make([]agentcore.MessagePart, 0, len(parts)+1)
	if strings.TrimSpace(text) != "" {
		out = append(out, agentcore.MessagePart{Type: agentcore.PartText, Text: strings.TrimSpace(text)})
	}
	for _, part := range parts {
		converted := agentcore.MessagePart{
			Type:     agentcore.PartType(part.Type),
			Text:     part.Text,
			URI:      part.URI,
			MimeType: part.MimeType,
			Name:     part.Name,
			Size:     part.Size,
			SHA256:   part.SHA256,
			Metadata: part.Metadata,
		}
		if converted.Type == "" {
			converted.Type = agentcore.PartFile
		}
		out = append(out, converted)
	}
	return out
}

func IsNewSessionCommand(text string) bool {
	switch strings.TrimSpace(strings.ToLower(text)) {
	case "/new":
		return true
	default:
		return false
	}
}

func usageFromMessages(messages []agentcore.Message) session.Usage {
	var usage session.Usage
	for _, msg := range messages {
		if msg.Usage == nil {
			continue
		}
		usage.Requests++
		usage.InputTokens += msg.Usage.InputTokens
		usage.OutputTokens += msg.Usage.OutputTokens
		if msg.Usage.CacheHit {
			usage.CacheHits++
		}
		usage.CacheReadTokens += msg.Usage.CacheReadTokens
		usage.CacheWriteTokens += msg.Usage.CacheWriteTokens
		usage.CacheInputTokens += msg.Usage.CacheInputTokens
		usage.CacheOutputTokens += msg.Usage.CacheOutputTokens
		total := msg.Usage.TotalTokens
		if total == 0 {
			total = msg.Usage.InputTokens + msg.Usage.OutputTokens
		}
		usage.TotalTokens += total
	}
	return usage
}

func addUsage(total *session.Usage, delta session.Usage) {
	if total == nil {
		return
	}
	total.Requests += delta.Requests
	total.InputTokens += delta.InputTokens
	total.OutputTokens += delta.OutputTokens
	total.TotalTokens += delta.TotalTokens
	total.EstimatedInputTokens += delta.EstimatedInputTokens
	total.SavedEstimatedTokens += delta.SavedEstimatedTokens
	total.CompactedMessages += delta.CompactedMessages
	total.CompactedToolResults += delta.CompactedToolResults
	total.CacheHits += delta.CacheHits
	total.CacheReadTokens += delta.CacheReadTokens
	total.CacheWriteTokens += delta.CacheWriteTokens
	total.CacheInputTokens += delta.CacheInputTokens
	total.CacheOutputTokens += delta.CacheOutputTokens
	total.Cost += delta.Cost
}

func writeUsageTrace(trace *traceRecorder, usage session.Usage) {
	if trace == nil || usage.Requests == 0 {
		return
	}
	_ = trace.write(map[string]any{
		"type":                   "model_usage",
		"requests":               usage.Requests,
		"input_tokens":           usage.InputTokens,
		"output_tokens":          usage.OutputTokens,
		"total_tokens":           usage.TotalTokens,
		"estimated_input_tokens": usage.EstimatedInputTokens,
		"saved_estimated_tokens": usage.SavedEstimatedTokens,
		"compacted_messages":     usage.CompactedMessages,
		"compacted_tool_results": usage.CompactedToolResults,
		"cache_hits":             usage.CacheHits,
		"cache_read_tokens":      usage.CacheReadTokens,
		"cache_write_tokens":     usage.CacheWriteTokens,
		"cache_input_tokens":     usage.CacheInputTokens,
		"cache_output_tokens":    usage.CacheOutputTokens,
	})
}

func traceIdentityForMessage(msg channel.InboundMessage) map[string]any {
	identity := map[string]any{
		"session_key": msg.SessionKey,
		"channel":     msg.Channel,
		"message_id":  msg.ID,
		"user_id":     msg.UserID,
		"thread_id":   msg.ThreadID,
	}
	if accountID := strings.TrimSpace(msg.Metadata["account_id"]); accountID != "" {
		identity["account_id"] = accountID
	}
	if peerID := strings.TrimSpace(msg.Metadata["peer_id"]); peerID != "" {
		identity["peer_id"] = peerID
	}
	if messageType := strings.TrimSpace(msg.Metadata["message_type"]); messageType != "" {
		identity["message_type"] = messageType
	}
	return identity
}

func (rt Runtime) home() string {
	home := config.DefaultHome()
	if rt.Config != nil && strings.TrimSpace(rt.Config.App.Home) != "" {
		home = rt.Config.App.Home
	}
	return home
}

func maxParallelTools(cfg *config.Root) int {
	if cfg == nil || cfg.Execution.MaxParallelTools <= 0 {
		return 4
	}
	return cfg.Execution.MaxParallelTools
}

func maxIterations(cfg *config.Root) int {
	if cfg == nil {
		return 50
	}
	return cfg.Execution.MaxIterationsValue()
}

func inactivityTimeout(cfg *config.Root) time.Duration {
	if cfg == nil {
		return 5 * time.Minute
	}
	timeout := cfg.Execution.InactivityTimeoutDuration()
	if timeout <= 0 {
		return 0
	}
	return timeout
}

func (rt Runtime) withActivityWatchdog(ctx context.Context, trace *traceRecorder, taskID string) (context.Context, func(), func() bool) {
	timeout := inactivityTimeout(rt.Config)
	if timeout <= 0 {
		return ctx, func() {}, func() bool { return false }
	}
	watchCtx, cancel := context.WithCancel(ctx)
	activity := make(chan struct{}, 1)
	done := make(chan struct{})
	var timedOut atomic.Bool
	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		for {
			select {
			case <-activity:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(timeout)
			case <-timer.C:
				timedOut.Store(true)
				_ = trace.write(map[string]any{"type": "task_inactivity_timeout", "task_id": taskID, "timeout": timeout.String()})
				cancel()
				return
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	if trace != nil {
		trace.onWrite = func() {
			select {
			case activity <- struct{}{}:
			default:
			}
		}
	}
	return watchCtx, func() {
		if trace != nil {
			trace.onWrite = nil
		}
		close(done)
		cancel()
	}, timedOut.Load
}

func defaultText(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func fallbackFinalReply(raw string) string {
	if strings.Contains(strings.ToUpper(raw), "[TOOL_CALL]") {
		return runtimeText(nil, channel.InboundMessage{}, "runtime.invalid_tool_call", nil)
	}
	return runtimeText(nil, channel.InboundMessage{}, "runtime.empty_reply", nil)
}

func memorySkills(skills []discoveredSkill) []memory.SkillEvidence {
	var out []memory.SkillEvidence
	for _, skill := range skills {
		if strings.TrimSpace(skill.Name) == "" {
			continue
		}
		out = append(out, memory.SkillEvidence{
			Name:        skill.Name,
			Path:        skill.Path,
			Scope:       skillScope(skill.Path),
			Description: skill.Description,
		})
	}
	return out
}

func (rt Runtime) reply(msg channel.InboundMessage, text string, style channel.MessageStyle) Response {
	return Response{Reply: channel.OutboundMessage{Channel: msg.Channel, ThreadID: msg.ThreadID, Text: text, Style: style}}
}

func containsAny(text string, markers []string) bool {
	for _, marker := range markers {
		if marker != "" && strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func turnHasToolEvidence(turn agentcore.TurnContext) bool {
	if len(turn.ToolResults) > 0 {
		return true
	}
	for _, msg := range turn.Messages {
		if msg.Role == agentcore.RoleTool {
			return true
		}
	}
	return false
}

func needsAction(text string) bool {
	lower := strings.ToLower(text)
	for _, marker := range []string{
		"read", "check", "inspect", "look at", "review", "fix", "run", "test", "build", "create", "write", "update", "delete", "commit", "trace", "source code", "file", "directory", "repo", "/users/", "~/.mateway",
		"读", "看", "检查", "查看", "分析", "修", "运行", "测试", "构建", "创建", "写", "更新", "删除", "提交", "记忆", "服务器", "地址",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikeUnexecutedCommitment(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"i will ", "i'll ", "let me ", "i'm going to ", "i am going to ", "next i will ", "i need to ", "i should ", "i plan to ", "will check", "will inspect", "will run", "will create", "will update",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksLikeUnexecutedAction(text string) bool {
	return looksLikeUnexecutedCommitment(text) || looksLikeEmptyActionPromise(text)
}

func mergeTaskAndInstruction(goal, instruction string) string {
	goal = strings.TrimSpace(goal)
	instruction = strings.TrimSpace(instruction)
	switch {
	case goal == "":
		return instruction
	case instruction == "":
		return goal
	case strings.EqualFold(goal, instruction):
		return instruction
	default:
		return "Active task:\n" + goal + "\n\nNew user message:\n" + instruction
	}
}

func looksLikeInputRequest(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if containsAny(trimmed, runtimeCueList(nil, "router.input_request.contains")) {
		return true
	}
	if strings.HasSuffix(trimmed, "？") {
		for _, cue := range runtimeCueList(nil, "router.input_request.question") {
			if strings.Contains(trimmed, cue) {
				return true
			}
		}
	}
	lower := strings.ToLower(trimmed)
	return containsAny(lower, runtimeCueList(nil, "router.input_request.contains")) ||
		containsAny(lower, runtimeCueList(nil, "router.input_request.question")) ||
		looksLikeContinuationOffer(trimmed)
}

func looksLikeContinuationOffer(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	return containsAny(lower, []string{
		"if you'd like", "if you would like", "i can ", "i could ", "would you like me to",
		"reply with", "just reply", "choose one", "tell me which", "告诉我", "你可以回复",
	})
}

func shouldStartNewTaskInsteadOfSteering(state session.State, userText string) bool {
	userText = strings.TrimSpace(userText)
	if userText == "" || state.ActiveTask == "" {
		return false
	}
	var active *session.TaskNode
	for i := range state.Tasks {
		if state.Tasks[i].ID == state.ActiveTask {
			active = &state.Tasks[i]
			break
		}
	}
	if active == nil {
		return false
	}
	if active.Status == "failed" {
		return shouldBreakFailedTaskSteering(active.Goal, userText)
	}
	if active.Status != "await_user_input" || !looksLikeContinuationOffer(active.Summary) {
		return false
	}
	lower := strings.ToLower(userText)
	for _, marker := range []string{"yes", "y", "ok", "okay", "sure", "continue", "go ahead", "1", "2", "3", "4", "继续", "好的", "可以", "行"} {
		if lower == marker {
			return false
		}
	}
	if strings.HasPrefix(lower, "now ") || strings.HasPrefix(lower, "new ") || strings.HasPrefix(lower, "另外") || strings.HasPrefix(lower, "还有") {
		return true
	}
	return needsAction(userText) && len(strings.Fields(userText)) >= 4
}

func shouldBreakFailedTaskSteering(goal, userText string) bool {
	userText = strings.TrimSpace(userText)
	if userText == "" {
		return false
	}
	lower := strings.ToLower(userText)
	for _, marker := range []string{"continue", "继续", "接着", "重试", "retry", "again", "再试"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	if !needsAction(userText) {
		return false
	}
	goalTerms := significantTerms(goal)
	userTerms := significantTerms(userText)
	if len(userTerms) == 0 {
		return false
	}
	overlap := 0
	for term := range userTerms {
		if goalTerms[term] {
			overlap++
		}
	}
	return overlap == 0
}

func significantTerms(text string) map[string]bool {
	out := map[string]bool{}
	for _, field := range strings.Fields(strings.ToLower(text)) {
		field = strings.Trim(field, ".,;:!?()[]{}\"'`，。！？；：“”‘’")
		if len([]rune(field)) < 2 {
			continue
		}
		out[field] = true
	}
	return out
}

func looksLikeEmptyActionPromise(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if len([]rune(trimmed)) > 120 {
		return false
	}
	if !strings.HasSuffix(trimmed, ":") && !strings.HasSuffix(trimmed, "：") {
		return false
	}
	lower := strings.ToLower(trimmed)
	if containsAny(lower, []string{"i will", "i'll", "let me", "first", "then", "now i", "checking", "confirm"}) {
		return true
	}
	return len([]rune(trimmed)) <= 40
}

func friendlyRuntimeError(cfg *config.Root, msg channel.InboundMessage, err error) string {
	if errors.Is(err, errContextBudgetHardLimit) {
		return runtimeText(cfg, msg, "runtime.context_budget_exceeded", nil)
	}
	raw := strings.TrimSpace(fmt.Sprint(err))
	lower := strings.ToLower(raw)
	switch {
	case err == context.Canceled || strings.Contains(lower, "context canceled"):
		return runtimeText(cfg, msg, "runtime.error.cancelled", nil)
	case strings.Contains(lower, "context deadline exceeded") || strings.Contains(lower, "client.timeout"):
		return runtimeText(cfg, msg, "runtime.error.timeout", nil)
	case strings.Contains(lower, "model api key is empty"):
		return runtimeText(cfg, msg, "runtime.error.missing_api_key", nil)
	case strings.Contains(lower, "all models failed"):
		return runtimeText(cfg, msg, "runtime.error.all_models_failed", nil)
	case strings.Contains(lower, "task contract references unavailable tools or skills"):
		return raw
	default:
		return runtimeText(cfg, msg, "runtime.error.generic", nil)
	}
}
