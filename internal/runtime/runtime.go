package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	continuity := judgeTaskContinuity(state, userText)
	if continuity.Continue && continuity.TaskID != "" {
		state.ActivateTask(continuity.TaskID)
		_ = trace.write(map[string]any{"type": "task_continuity", "task_id": continuity.TaskID, "reason": continuity.Reason})
	}
	if shouldStartNewTaskInsteadOfSteering(state, userText) {
		state.ActiveTask = ""
	}
	task := state.EnsureTask(msg.Text)
	if task.Goal != userText && state.ActiveTask == task.ID {
		userText = mergeTaskAndInstruction(task.Goal, userText)
	}
	phase := tracePhaseExecute
	if continuity.IsFollowup {
		phase = tracePhaseFollowupExecute
	}
	trace.setIdentity(map[string]any{"task_id": task.ID})
	_ = trace.write(map[string]any{"type": "request", "text": msg.Text, "effective_text": userText})
	state.AddTraceRef(task.ID, session.TraceRef{TraceID: trace.id, TracePath: trace.path, Phase: phase, MessageID: msg.ID})
	return rt.runTask(ctx, msg, &state, task, userText, phase, trace)
}

func (rt Runtime) runTask(ctx context.Context, msg channel.InboundMessage, state *session.State, task *session.TaskNode, userText string, phase string, trace *traceRecorder) (Response, error) {
	messages, compactStats, err := prepareMessagesForModel(state.Messages)
	if err != nil {
		_ = trace.write(map[string]any{
			"type":         "context_budget_exceeded",
			"before_chars": compactStats.BeforeChars,
			"after_chars":  compactStats.AfterChars,
			"error":        err.Error(),
		})
		state.BlockActiveTask("failed")
		if saveErr := rt.saveState(state, trace); saveErr != nil {
			return Response{}, saveErr
		}
		resp := Response{
			Reply: channel.OutboundMessage{
				Channel:  msg.Channel,
				ThreadID: msg.ThreadID,
				Text:     runtimeText(rt.Config, msg, "runtime.context_budget_exceeded", nil),
				Style:    channel.StyleError,
			},
			TraceID:   trace.id,
			TracePath: trace.path,
			Failed:    true,
		}
		_ = trace.write(map[string]any{"type": "reply", "text": resp.Reply.Text, "style": resp.Reply.Style})
		return resp, nil
	}
	writeCompactTrace(trace, "model_input_compacted", compactStats)
	if strings.TrimSpace(userText) != "" || len(msg.Parts) > 0 {
		messages = append(messages, userAgentMessage(userText, msg.Parts))
	}

	agent := rt.Pool.AgentForMessage(msg)
	if agent == nil {
		agent = agentcore.NewAgent(rt.Model, rt.Tools)
	}
	profile := rt.Pool.ProfileForMessage(msg)
	trace.setIdentity(map[string]any{
		"agent_id": profile.ID,
		"task_id":  task.ID,
	})
	contract := rt.ensureTaskContract(ctx, msg, state, task, userText, agent.Model, trace)
	if phase == tracePhaseExecute && state.Pending == nil && !taskHasTracePhase(task, tracePhasePlanReview) && shouldPauseForTaskPlan(contract) {
		state.Pending = &session.PendingAction{
			Kind:     session.PendingKindTaskPlanConfirm,
			TaskID:   task.ID,
			Question: "1 execute, 2 replan",
		}
		state.AddTraceRef(task.ID, session.TraceRef{TraceID: trace.id, TracePath: trace.path, Phase: tracePhasePlanReview, MessageID: msg.ID})
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, err
		}
		_ = trace.write(map[string]any{"type": "task_plan_review", "task_id": task.ID})
		return Response{
			Reply: channel.OutboundMessage{
				Channel:  msg.Channel,
				ThreadID: msg.ThreadID,
				Text:     renderTaskPlanForReview(contract, userText),
				Style:    channel.StyleInputRequired,
			},
			TraceID:   trace.id,
			TracePath: trace.path,
		}, nil
	}
	discoveredSkills := discoverSkillsForAgent(rt.Config, profile.ID, 12)
	systemPrompt := prependTaskFocus(buildRuntimeSystemContext(rt.Config, profile), task, userText)
	if contractContext := renderTaskContractContext(contract); contractContext != "" {
		systemPrompt = strings.TrimSpace(systemPrompt + "\n\n" + contractContext)
	}
	if phase != tracePhasePlanReview && len(contract.PlanItems) > 0 {
		rt.emitProgress(msg, *state, task.ID, taskExecutionEventCount(*state, task.ID), channel.ProgressStep{
			Title:   "plan",
			Status:  "running",
			Summary: renderTaskPlanForExecution(contract, userText),
		})
	}
	systemPrompt = appendPreviousTaskContext(systemPrompt, *state, task.ID)
	contextMessages := rt.Hooks.contextMessages(ctx, ContextHookInput{
		Message:  msg,
		State:    *state,
		TaskID:   task.ID,
		UserText: userText,
		Profile:  profile,
	}, trace)
	if len(contextMessages) > 0 {
		messages = append(messages, contextMessages...)
	}
	modelCfg := rt.Pool.ModelConfigForMessage(msg)
	_ = trace.write(map[string]any{
		"type":                   "model_route_selected",
		"model":                  modelCfg.Model,
		"model_name":             modelCfg.Name,
		"provider":               modelCfg.Provider,
		"context_window_tokens":  modelCfg.ContextWindow,
		"max_output_tokens":      modelCfg.MaxTokensValue(),
		"context_budget_enabled": rt.Config.Execution.ContextBudget.EnabledValue(),
	})
	var budgetResults []contextBudgetResult
	agent.Model = budgetedModel{
		inner:       agent.Model,
		config:      rt.Config,
		modelConfig: modelCfg,
		trace:       trace,
		state:       *state,
		taskID:      task.ID,
		results:     &budgetResults,
	}
	agent.SystemPrompt = systemPrompt
	agent.Messages = messages
	agent.MaxParallelTools = maxParallelTools(rt.Config)
	agent.MaxIterations = maxIterations(rt.Config)
	runCtx, stopActivityWatch, activityTimedOut := rt.withActivityWatchdog(ctx, trace, task.ID)
	defer stopActivityWatch()
	agentHooks := rt.hooksForState(state, msg, task.ID, userText, trace, nil)
	agent.Hooks = agentHooks
	result, err := agent.Continue(runCtx)
	if err != nil {
		state.BlockActiveTask("failed")
		if saveErr := rt.saveState(state, trace); saveErr != nil {
			return Response{}, saveErr
		}
		if activityTimedOut() {
			text := runtimeText(rt.Config, msg, "runtime.activity_timeout", nil)
			resp := rt.reply(msg, renderPartialReply(rt.Config, msg, text), channel.StylePartial)
			resp.Reply.Progress = progressStepsForTask(*state, task.ID)
			resp.TraceID = trace.id
			resp.TracePath = trace.path
			resp.Failed = true
			_ = trace.write(map[string]any{"type": "reply", "text": resp.Reply.Text, "style": resp.Reply.Style})
			return resp, nil
		}
		text := friendlyRuntimeError(rt.Config, msg, err)
		resp := Response{
			Reply: channel.OutboundMessage{
				Channel:  msg.Channel,
				ThreadID: msg.ThreadID,
				Text:     text,
				Style:    channel.StyleError,
				Progress: progressStepsForTask(*state, task.ID),
			},
			TraceID:   trace.id,
			TracePath: trace.path,
			Failed:    true,
		}
		_ = trace.write(map[string]any{"type": "model_error", "error": err.Error(), "friendly": text})
		_ = trace.write(map[string]any{"type": "reply", "text": resp.Reply.Text, "style": resp.Reply.Style})
		return resp, nil
	}

	state.Messages = redactMessagesForStorage(result.Messages)
	finalText := redactSecretString(result.FinalText)
	usage := usageFromMessages(result.Messages)
	addUsage(&usage, usageFromBudgetResults(budgetResults))
	addUsage(&state.Usage, usage)
	writeUsageTrace(trace, usage)
	taskCompleted := false
	emptyActionPromise := looksLikeEmptyActionPromise(finalText)
	if state.Pending == nil {
		contractValidation := validateTaskContract(taskContractFromState(*state, task.ID), taskFromState(*state, task.ID))
		if result.StopReason != "" {
			state.BlockActiveTask("failed")
			state.AddExecutionEvent(task.ID, session.ExecutionEvent{Type: result.StopReason, Status: "failed", Summary: result.StopReason, Evidence: map[string]any{"iterations": result.Iterations}})
			_ = trace.write(map[string]any{"type": result.StopReason, "task_id": task.ID, "status": "failed", "iterations": result.Iterations})
		} else if !contractValidation.Satisfied {
			state.BlockActiveTask("failed")
			state.AddExecutionEvent(task.ID, session.ExecutionEvent{Type: "task_contract_unsatisfied", Status: "failed", Summary: strings.Join(contractValidation.Missing, "; "), Evidence: map[string]any{"missing": contractValidation.Missing}})
			_ = trace.write(map[string]any{"type": "task_contract_unsatisfied", "task_id": task.ID, "status": "failed", "missing": contractValidation.Missing})
		} else if looksLikeInputRequest(finalText) {
			state.AwaitUserInputActiveTaskWithSummary(summarize(finalText), trace.id, trace.path)
			updateSessionSummary(state, task.ID, finalText, "await_user_input", trace)
			state.AddExecutionEvent(task.ID, session.ExecutionEvent{Type: "await_user_input", Status: "await_user_input", Summary: summarize(finalText)})
			_ = trace.write(map[string]any{"type": "await_user_input", "task_id": task.ID, "status": "await_user_input"})
		} else if emptyActionPromise || looksLikeUnexecutedCommitment(finalText) {
			state.BlockActiveTask("failed")
			state.AddExecutionEvent(task.ID, session.ExecutionEvent{Type: "empty_action_promise", Status: "failed", Summary: summarize(finalText)})
			_ = trace.write(map[string]any{"type": "empty_action_promise", "task_id": task.ID, "status": "failed"})
		} else {
			completeNoToolPlanItems(state, task.ID, finalText)
			state.CompleteActiveTaskWithSummary(summarize(finalText), trace.id, trace.path)
			updateSessionSummary(state, task.ID, finalText, "completed", trace)
			state.AddExecutionEvent(task.ID, session.ExecutionEvent{Type: "completed", Status: "completed", Summary: summarize(finalText)})
			taskCompleted = true
		}
	}
	if err := rt.saveState(state, trace); err != nil {
		return Response{}, err
	}

	var learningResult *memory.LearningResult
	if taskCompleted {
		home := rt.home()
		observe := rt.Hooks.observe(ctx, ObserveHookInput{
			Kind:       "task_completed",
			Home:       home,
			SessionKey: state.Key,
			State:      *state,
			TaskID:     task.ID,
			FinalText:  finalText,
			TraceID:    trace.id,
			TracePath:  trace.path,
			Skills:     memorySkills(discoveredSkills),
			UserText:   userText,
		}, trace)
		learningResult = observe.LearningResult
		if learningResult != nil {
			_ = trace.write(map[string]any{
				"type":            "self_learning",
				"diary_path":      learningResult.DiaryPath,
				"reflection_path": learningResult.ReflectionPath,
				"proposal_id":     proposalID(learningResult.Proposal),
			})
			if learningResult.Proposal != nil {
				state.Pending = &session.PendingAction{
					Kind:       "memory_proposal_review",
					TaskID:     task.ID,
					ProposalID: learningResult.Proposal.ID,
					Question:   "1 save, 2 ignore",
				}
				if err := rt.saveState(state, trace); err != nil {
					return Response{}, err
				}
			}
		}
	}
	text := redactSecretString(rt.Hooks.response(ctx, ResponseHookInput{RawText: finalText, LearningResult: learningResult}, trace))
	var followUps []channel.OutboundMessage
	if learningResult != nil && learningResult.Proposal != nil {
		followUps = append(followUps, channel.OutboundMessage{
			Channel:  msg.Channel,
			ThreadID: msg.ThreadID,
			Text:     renderMemoryProposalReview(rt.Config, msg, *learningResult.Proposal),
			Style:    channel.StyleMemoryProposalReview,
		})
	}
	if learningResult == nil || learningResult.Proposal == nil {
		if nudge, err := memory.PendingProposalNudge(rt.home(), state.Key, time.Now(), rt.memoryProposalNudgeOptions(msg)); err == nil && nudge != "" {
			text = strings.TrimSpace(text) + "\n\n" + nudge
			_ = trace.write(map[string]any{"type": "memory_proposal_nudge", "text": nudge})
		}
	}
	style := channel.MessageStyle("")
	failed := result.StopReason != "" || emptyActionPromise
	if failed && style == "" {
		style = channel.StylePartial
	}
	resp := Response{
		Reply: channel.OutboundMessage{
			Channel:  msg.Channel,
			ThreadID: msg.ThreadID,
			Text:     text,
			Style:    style,
		},
		FollowUps: followUps,
		TraceID:   trace.id,
		TracePath: trace.path,
		Failed:    failed,
	}
	_ = trace.write(map[string]any{"type": "reply", "text": resp.Reply.Text, "style": resp.Reply.Style})
	for _, followUp := range followUps {
		_ = trace.write(map[string]any{"type": "follow_up_reply", "text": followUp.Text, "style": followUp.Style})
	}
	return resp, nil
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
	default:
		return runtimeText(cfg, msg, "runtime.error.generic", nil)
	}
}
