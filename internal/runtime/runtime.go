package runtime

import (
	"context"
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
	Config       *config.Root
	Store        session.Store
	Tools        *agentcore.ToolRegistry
	Model        agentcore.Model
	Pool         AgentPool
	Hooks        RuntimeHooks
	ProgressSink func(channel.OutboundMessage)
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
	return Runtime{
		Config: cfg,
		Store:  session.NewStore(cfg.App.Home),
		Tools:  tool.NewRegistry(cfg),
		Model:  HeuristicModel{},
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
	_ = trace.write(map[string]any{"type": "request", "text": msg.Text})
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
	task := state.EnsureTask(msg.Text)
	if task.Goal != userText && state.ActiveTask == task.ID {
		userText = mergeTaskAndInstruction(task.Goal, userText)
	}
	return rt.runTask(ctx, msg, &state, task, userText, trace)
}

func (rt Runtime) runTask(ctx context.Context, msg channel.InboundMessage, state *session.State, task *session.TaskNode, userText string, trace *traceRecorder) (Response, error) {
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
				Style:    "error",
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
	discoveredSkills := discoverSkillsForAgent(rt.Config, profile.ID, 12)
	systemPrompt := prependTaskFocus(buildRuntimeSystemContext(rt.Config, profile), task, userText)
	systemPrompt = appendPreviousTaskContext(systemPrompt, *state, task.ID)
	agent.SystemPrompt = systemPrompt
	agent.Messages = messages
	agent.MaxParallelTools = maxParallelTools(rt.Config)
	agent.MaxIterations = maxIterations(rt.Config)
	runCtx, stopActivityWatch, activityTimedOut := rt.withActivityWatchdog(ctx, trace, task.ID)
	defer stopActivityWatch()
	agentHooks := rt.hooksForState(state, msg, task.ID, userText, trace, rt.Hooks.contextMessages(ctx, ContextHookInput{
		Message:  msg,
		State:    *state,
		TaskID:   task.ID,
		UserText: userText,
		Profile:  profile,
	}, trace))
	agent.Hooks = agentHooks
	result, err := agent.Continue(runCtx)
	if err != nil {
		state.BlockActiveTask("failed")
		if saveErr := rt.saveState(state, trace); saveErr != nil {
			return Response{}, saveErr
		}
		if activityTimedOut() {
			text := runtimeText(rt.Config, msg, "runtime.activity_timeout", nil)
			resp := rt.reply(msg, renderPartialReply(rt.Config, msg, text), "partial")
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
				Style:    "error",
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
	addUsage(&state.Usage, usage)
	writeUsageTrace(trace, usage)
	taskCompleted := false
	emptyActionPromise := looksLikeEmptyActionPromise(finalText)
	if state.Pending == nil {
		if result.StopReason != "" {
			state.BlockActiveTask("failed")
			state.AddExecutionEvent(task.ID, session.ExecutionEvent{Type: result.StopReason, Status: "failed", Summary: result.StopReason, Evidence: map[string]any{"iterations": result.Iterations}})
			_ = trace.write(map[string]any{"type": result.StopReason, "task_id": task.ID, "status": "failed", "iterations": result.Iterations})
		} else if looksLikeInputRequest(finalText) {
			state.AwaitUserInputActiveTaskWithSummary(summarize(finalText), trace.id, trace.path)
			state.AddExecutionEvent(task.ID, session.ExecutionEvent{Type: "await_user_input", Status: "await_user_input", Summary: summarize(finalText)})
			_ = trace.write(map[string]any{"type": "await_user_input", "task_id": task.ID, "status": "await_user_input"})
		} else if emptyActionPromise {
			state.BlockActiveTask("failed")
			state.AddExecutionEvent(task.ID, session.ExecutionEvent{Type: "empty_action_promise", Status: "failed", Summary: summarize(finalText)})
			_ = trace.write(map[string]any{"type": "empty_action_promise", "task_id": task.ID, "status": "failed"})
		} else {
			state.CompleteActiveTaskWithSummary(summarize(finalText), trace.id, trace.path)
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
			Style:    "memory_proposal_review",
		})
	}
	if learningResult == nil || learningResult.Proposal == nil {
		if nudge, err := memory.PendingProposalNudge(rt.home(), state.Key, time.Now(), rt.memoryProposalNudgeOptions(msg)); err == nil && nudge != "" {
			text = strings.TrimSpace(text) + "\n\n" + nudge
			_ = trace.write(map[string]any{"type": "memory_proposal_nudge", "text": nudge})
		}
	}
	style := ""
	failed := result.StopReason != "" || emptyActionPromise
	if failed && style == "" {
		style = "partial"
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

func (rt Runtime) memoryProposalNudgeOptions(msg channel.InboundMessage) memory.ProposalNudgeOptions {
	options := memory.ProposalNudgeOptions{
		Channel:      msg.Channel,
		Channels:     []string{"cli"},
		Interval:     24 * time.Hour,
		MaxProposals: 3,
	}
	if rt.Config == nil {
		return options
	}
	cfg := rt.Config.Memory.ProposalNudge
	if !cfg.EnabledValue() {
		if cfg.Enabled == nil && strings.TrimSpace(cfg.Interval) == "" && len(cfg.Channels) == 0 && cfg.MaxProposals == 0 {
			return options
		}
		options.Channels = nil
		options.Channels = []string{"__disabled__"}
		return options
	}
	if len(cfg.Channels) > 0 {
		options.Channels = cfg.Channels
	}
	if parsed, err := time.ParseDuration(strings.TrimSpace(cfg.Interval)); err == nil && parsed > 0 {
		options.Interval = parsed
	}
	if cfg.MaxProposals > 0 {
		options.MaxProposals = cfg.MaxProposals
	}
	return options
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
	resp := rt.reply(msg, text, "session_reset")
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
	total.Cost += delta.Cost
}

func writeUsageTrace(trace *traceRecorder, usage session.Usage) {
	if trace == nil || usage.Requests == 0 {
		return
	}
	_ = trace.write(map[string]any{
		"type":          "model_usage",
		"requests":      usage.Requests,
		"input_tokens":  usage.InputTokens,
		"output_tokens": usage.OutputTokens,
		"total_tokens":  usage.TotalTokens,
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

func proposalID(proposal *memory.Proposal) string {
	if proposal == nil {
		return ""
	}
	return proposal.ID
}

func renderMemoryProposalReview(cfg *config.Root, msg channel.InboundMessage, proposal memory.Proposal) string {
	var b strings.Builder
	b.WriteString(runtimeText(cfg, msg, "memory.proposal_review.header", nil))
	b.WriteString(proposal.ID)
	b.WriteString(" ")
	b.WriteString(strings.TrimSpace(proposal.Title))
	b.WriteString(runtimeText(cfg, msg, "memory.proposal_review.type", nil))
	b.WriteString(defaultText(proposal.Type, "experience"))
	b.WriteString(" / ")
	b.WriteString(defaultText(proposal.Scope, "agent"))
	if strings.TrimSpace(proposal.Confidence) != "" {
		b.WriteString(runtimeText(cfg, msg, "memory.proposal_review.confidence", nil))
		b.WriteString(strings.TrimSpace(proposal.Confidence))
	}
	if summary := proposalSummary(proposal); summary != "" {
		b.WriteString(runtimeText(cfg, msg, "memory.proposal_review.summary", nil))
		b.WriteString(summary)
	}
	if len(proposal.Sources) > 0 {
		b.WriteString(runtimeText(cfg, msg, "memory.proposal_review.sources", nil))
		b.WriteString(summarize(strings.Join(proposal.Sources, ", ")))
	}
	values := textValues("proposal_id", proposal.ID)
	b.WriteString(runtimeText(cfg, msg, "memory.proposal_review.show", values))
	b.WriteString(runtimeText(cfg, msg, "memory.proposal_review.commit", values))
	b.WriteString(runtimeText(cfg, msg, "memory.proposal_review.reject", values))
	b.WriteString(runtimeText(cfg, msg, "memory.proposal_review.reply", nil))
	return b.String()
}

func defaultText(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func proposalSummary(proposal memory.Proposal) string {
	body := strings.TrimSpace(proposal.Body)
	body = strings.TrimPrefix(body, "# "+strings.TrimSpace(proposal.Title))
	body = strings.TrimSpace(body)
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if line != "" && !strings.HasPrefix(line, "#") {
			return summarize(line)
		}
	}
	return ""
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

func (rt Runtime) hooksForState(state *session.State, msg channel.InboundMessage, taskID, userText string, trace *traceRecorder, steering []agentcore.Message) agentcore.Hooks {
	steeringSent := false
	hooks := agentcore.Hooks{
		Emit: func(ctx context.Context, event agentcore.Event) error {
			if err := trace.emit(ctx, event); err != nil {
				return err
			}
			if event.Type == agentcore.EventToolExecutionProgress {
				rt.emitProgress(msg, *state, taskID, channel.ProgressStep{
					Title:      event.ToolCall.Name,
					Status:     "running",
					Tool:       event.ToolCall.Name,
					Summary:    summarizeToolCall(event.ToolCall),
					DurationMS: event.Duration.Milliseconds(),
				})
			}
			return nil
		},
		ToolTimeout: func(input agentcore.ToolExecutionContext) time.Duration {
			return runtimeToolTimeout(rt.Config, input.ToolCall.Name)
		},
		ToolProgressInterval: func(input agentcore.ToolExecutionContext) time.Duration {
			return runtimeToolProgressInterval(rt.Config, input.ToolCall.Name)
		},
		GetSteeringMessages: func(context.Context) ([]agentcore.Message, error) {
			if steeringSent {
				return nil, nil
			}
			steeringSent = true
			return append([]agentcore.Message(nil), steering...), nil
		},
		BeforeToolCall: func(_ context.Context, input agentcore.BeforeToolCallContext) (agentcore.BeforeToolCallResult, error) {
			policy := rt.Hooks.toolPolicy(context.Background(), ToolPolicyHookInput{ToolCall: input.ToolCall, Tool: input.Tool, Config: rt.Config}, trace)
			if policy.Block {
				state.AddExecutionEvent(taskID, session.ExecutionEvent{
					Type:    "tool_blocked",
					Status:  "failed",
					Tool:    input.ToolCall.Name,
					Summary: policy.Reason,
					Evidence: map[string]any{
						"reason": policy.Reason,
					},
				})
				_ = trace.write(map[string]any{"type": "tool_blocked", "task_id": taskID, "tool": input.ToolCall.Name, "reason": policy.Reason})
				rt.emitProgress(msg, *state, taskID, channel.ProgressStep{Tool: input.ToolCall.Name, Status: "blocked", Summary: policy.Reason})
				return agentcore.BeforeToolCallResult{Block: true, Reason: policy.Reason}, nil
			}
			rt.emitProgress(msg, *state, taskID, channel.ProgressStep{Tool: input.ToolCall.Name, Status: "running", Summary: summarizeToolCall(input.ToolCall)})
			return agentcore.BeforeToolCallResult{}, nil
		},
		AfterToolCall: func(_ context.Context, input agentcore.AfterToolCallContext) (agentcore.AfterToolCallResult, error) {
			observe := rt.Hooks.observe(context.Background(), ObserveHookInput{
				Kind:       "tool_result",
				State:      *state,
				TaskID:     taskID,
				ToolCall:   input.ToolCall,
				Tool:       input.Tool,
				ToolResult: input.ToolResult,
			}, trace)
			if observe.TaskStep != nil {
				state.AddStep(taskID, *observe.TaskStep)
				evidence := map[string]any{
					"accepted": observe.TaskStep.Accepted,
					"mutation": observe.TaskStep.Mutation,
					"risk":     observe.TaskStep.Risk,
				}
				for key, value := range input.ToolResult.Evidence {
					switch key {
					case "elapsed_ms", "timed_out", "deadline_ms", "output_truncated":
						evidence[key] = value
					}
				}
				state.AddExecutionEvent(taskID, session.ExecutionEvent{
					Type:     "tool_result",
					Status:   observe.TaskStep.Status,
					Tool:     input.ToolCall.Name,
					StepID:   observe.TaskStep.ID,
					Summary:  observe.TaskStep.Summary,
					Evidence: evidence,
				})
				switch observe.TaskStep.Status {
				case "accepted":
				case "failed", "suspect":
				}
				rt.emitProgress(msg, *state, taskID, progressStepFromExecutionEvent(session.ExecutionEvent{
					Type:     "tool_result",
					Status:   observe.TaskStep.Status,
					Tool:     input.ToolCall.Name,
					Summary:  observe.TaskStep.Summary,
					Evidence: evidence,
				}))
			}
			result := compactToolResultForModel(input.ToolCall, input.ToolResult, rt.home(), trace.id)
			return agentcore.AfterToolCallResult{ToolResult: &result}, nil
		},
	}
	var followUps []agentcore.Message
	followupSent := false
	hooks.ShouldStopAfterTurn = func(_ context.Context, turn agentcore.TurnContext) (bool, error) {
		if followupSent || turnHasToolEvidence(turn) || !needsAction(userText) || !looksLikeUnexecutedAction(turn.Message.Content) {
			return false, nil
		}
		followupSent = true
		if len(followUps) == 0 {
			followUps = append(followUps, agentcore.Message{
				Role:    agentcore.RoleUser,
				Content: "You promised an action but did not execute any tool. Continue now with the smallest safe tool call, or state the concrete blocker that prevents execution.",
			})
			_ = trace.write(map[string]any{"type": "deliverable_gate_followup", "task_id": taskID, "reason": "unexecuted_commitment"})
		}
		return false, nil
	}
	hooks.GetFollowUpMessages = func(context.Context) ([]agentcore.Message, error) {
		out := followUps
		followUps = nil
		return out, nil
	}
	return hooks
}

var runtimeToolTimeout = func(cfg *config.Root, toolName string) time.Duration {
	_ = cfg
	switch strings.TrimSpace(toolName) {
	case "project.index", "file.read":
		return 30 * time.Second
	case "terminal.run":
		return 120 * time.Second
	default:
		return 60 * time.Second
	}
}

var runtimeToolProgressInterval = func(cfg *config.Root, toolName string) time.Duration {
	_ = cfg
	if strings.TrimSpace(toolName) == "" {
		return 0
	}
	return 30 * time.Second
}

func (rt Runtime) handlePending(_ context.Context, state *session.State, msg channel.InboundMessage, trace *traceRecorder) (Response, bool, error) {
	if state.Pending == nil {
		return Response{}, false, nil
	}
	if state.Pending.Kind != "memory_proposal_review" {
		_ = trace.write(map[string]any{"type": "pending_discarded", "pending_kind": state.Pending.Kind, "task_id": state.Pending.TaskID})
		state.Pending = nil
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, true, err
		}
		return Response{}, false, nil
	}
	action, ok := parseNumericMemoryProposalReviewAction(msg.Text)
	if !ok {
		_ = trace.write(map[string]any{"type": "pending_control_invalid_reply", "task_id": state.Pending.TaskID, "pending_kind": "memory_proposal_review"})
		resp := rt.reply(msg, "Please reply with 1 to save this memory proposal or 2 to ignore it. To start a separate task, send /new first.", "input_required")
		resp.TraceID = trace.id
		resp.TracePath = trace.path
		return resp, true, nil
	}
	taskID := state.Pending.TaskID
	proposalID := state.Pending.ProposalID
	state.Pending = nil
	_ = trace.write(map[string]any{"type": "pending_control_executed", "task_id": taskID, "pending_kind": "memory_proposal_review", "command": action})
	store := memory.ProposalStore{Home: rt.home(), MemoryRoot: memoryRootForConfig(rt.Config)}
	if action == "commit" {
		proposal, target, err := store.Commit(proposalID)
		if err != nil {
			if saveErr := rt.saveState(state, trace); saveErr != nil {
				return Response{}, true, saveErr
			}
			return rt.reply(msg, runtimeText(rt.Config, msg, "memory.commit.error", textValues("error", err.Error())), "error"), true, nil
		}
		_ = trace.write(map[string]any{"type": "memory_proposal_review_committed", "proposal_id": proposal.ID, "target": target})
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, true, err
		}
		return rt.reply(msg, runtimeText(rt.Config, msg, "memory.commit.done", textValues("target", target)), "completed"), true, nil
	}
	proposal, err := store.Reject(proposalID, "user selected numeric reject from conversation")
	if err != nil {
		if saveErr := rt.saveState(state, trace); saveErr != nil {
			return Response{}, true, saveErr
		}
		return rt.reply(msg, runtimeText(rt.Config, msg, "memory.reject.error", textValues("error", err.Error())), "error"), true, nil
	}
	_ = trace.write(map[string]any{"type": "memory_proposal_review_rejected", "proposal_id": proposal.ID})
	if err := rt.saveState(state, trace); err != nil {
		return Response{}, true, err
	}
	return rt.reply(msg, runtimeText(rt.Config, msg, "memory.reject.done", nil), "completed"), true, nil
}

func parseNumericMemoryProposalReviewAction(text string) (string, bool) {
	switch strings.TrimSpace(text) {
	case "1":
		return "commit", true
	case "2":
		return "reject", true
	default:
		return "", false
	}
}

func acceptToolResult(tool agentcore.Tool, result agentcore.ToolResult) (string, map[string]any) {
	evidence := map[string]any{}
	for key, value := range result.Evidence {
		evidence[key] = value
	}
	if tool != nil {
		contract := agentcore.ContractFor(tool)
		risk := tool.Risk()
		evidence["risk"] = string(risk)
		evidence["mutation"] = risk == agentcore.RiskGuardedMutation || risk == agentcore.RiskDangerous
		if contract.Acceptance != "" {
			evidence["acceptance_criteria"] = contract.Acceptance
		}
		if contract.Evidence != "" {
			evidence["evidence_contract"] = contract.Evidence
		}
	}
	if result.IsError {
		evidence["acceptance"] = "failed"
		return "failed", evidence
	}
	if len(result.Evidence) == 0 && strings.TrimSpace(result.Content) == "" {
		evidence["acceptance"] = "suspect"
		return "suspect", evidence
	}
	evidence["acceptance"] = "accepted"
	return "accepted", evidence
}

func (rt Runtime) reply(msg channel.InboundMessage, text, style string) Response {
	return Response{Reply: channel.OutboundMessage{Channel: msg.Channel, ThreadID: msg.ThreadID, Text: text, Style: style}}
}

func (rt Runtime) emitProgress(msg channel.InboundMessage, state session.State, taskID string, current channel.ProgressStep) {
	if rt.ProgressSink == nil {
		return
	}
	steps := progressStepsForTask(state, taskID)
	if strings.TrimSpace(current.Title) != "" || strings.TrimSpace(current.Tool) != "" {
		steps = append(steps, current)
	}
	rt.ProgressSink(channel.OutboundMessage{
		Channel:  msg.Channel,
		ThreadID: msg.ThreadID,
		Text:     "Processing...",
		Style:    "processing",
		Progress: steps,
	})
}

func summarizeToolCall(call agentcore.ToolCall) string {
	switch call.Name {
	case "terminal.run":
		return compactProgressSummary(fmt.Sprint(call.Args["command"]))
	case "project.index", "file.read", "file.write", "file.delete":
		return compactProgressSummary(fmt.Sprint(call.Args["path"]))
	case "web.search":
		return compactProgressSummary(fmt.Sprint(call.Args["query"]))
	case "web.fetch":
		return compactProgressSummary(fmt.Sprint(call.Args["url"]))
	default:
		return ""
	}
}

func progressStepsForTask(state session.State, taskID string) []channel.ProgressStep {
	task := taskFromState(state, taskID)
	events := task.Execution.Events
	out := make([]channel.ProgressStep, 0, len(events))
	for _, event := range events {
		step := progressStepFromExecutionEvent(event)
		if strings.TrimSpace(step.Title) == "" {
			continue
		}
		out = append(out, step)
	}
	const limit = 8
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func progressStepFromExecutionEvent(event session.ExecutionEvent) channel.ProgressStep {
	title := strings.TrimSpace(event.Type)
	if event.Tool != "" {
		title = strings.TrimSpace(event.Tool)
	}
	step := channel.ProgressStep{
		Title:   title,
		Status:  strings.TrimSpace(event.Status),
		Tool:    strings.TrimSpace(event.Tool),
		Summary: compactProgressSummary(event.Summary),
	}
	if accepted, ok := event.Evidence["accepted"].(bool); ok && accepted {
		step.Status = firstNonEmpty(step.Status, "accepted")
	}
	if timedOut, ok := event.Evidence["timed_out"].(bool); ok {
		step.TimedOut = timedOut
	}
	if elapsed, ok := int64Evidence(event.Evidence["elapsed_ms"]); ok {
		step.DurationMS = elapsed
	}
	return step
}

func int64Evidence(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	default:
		return 0, false
	}
}

func compactProgressSummary(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" {
		return ""
	}
	const limit = 80
	if len(text) <= limit {
		return text
	}
	return text[:limit] + fmt.Sprintf("... (%d chars)", len(text))
}

func summarize(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= 160 {
		return text
	}
	return text[:160] + fmt.Sprintf("... (%d chars)", len(text))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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

type HeuristicModel struct{}

func (HeuristicModel) Next(_ context.Context, ctx agentcore.Context) (agentcore.Message, error) {
	last := lastConversationMessage(ctx.Messages)
	if last.Role == agentcore.RoleTool {
		return agentcore.Message{Role: agentcore.RoleAssistant, Content: last.Content}, nil
	}
	text := strings.TrimSpace(last.Content)
	if path, ok := strings.CutPrefix(text, "/read "); ok {
		return agentcore.Message{
			Role: agentcore.RoleAssistant,
			ToolCalls: []agentcore.ToolCall{{
				ID:   "call_1",
				Name: "file.read",
				Args: map[string]any{"path": strings.TrimSpace(path)},
			}},
		}, nil
	}
	if path, ok := strings.CutPrefix(text, "/index "); ok {
		return agentcore.Message{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "project.index", Args: map[string]any{"path": strings.TrimSpace(path)}}}}, nil
	}
	if rest, ok := strings.CutPrefix(text, "/write "); ok {
		path, content, _ := strings.Cut(rest, " ")
		return agentcore.Message{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "file.write", Args: map[string]any{"path": strings.TrimSpace(path), "content": strings.TrimSpace(content)}}}}, nil
	}
	if command, ok := strings.CutPrefix(text, "/run "); ok {
		return agentcore.Message{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "terminal.run", Args: map[string]any{"command": strings.TrimSpace(command)}}}}, nil
	}
	if query, ok := strings.CutPrefix(text, "/search "); ok {
		return agentcore.Message{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "web.search", Args: map[string]any{"query": strings.TrimSpace(query)}}}}, nil
	}
	if rest, ok := strings.CutPrefix(text, "/schedule "); ok {
		parts := strings.SplitN(strings.TrimSpace(rest), " ", 2)
		if len(parts) == 2 {
			return agentcore.Message{Role: agentcore.RoleAssistant, ToolCalls: []agentcore.ToolCall{{ID: "call_1", Name: "schedule.create", Args: map[string]any{"run_at": parts[0], "text": parts[1], "session_key": "cli:scheduled"}}}}, nil
		}
	}
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: runtimeText(nil, channel.InboundMessage{}, "runtime.heuristic.echo", textValues("text", text))}, nil
}

func lastConversationMessage(messages []agentcore.Message) agentcore.Message {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != agentcore.RoleSystem {
			return messages[i]
		}
	}
	return agentcore.Message{}
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
		containsAny(lower, runtimeCueList(nil, "router.input_request.question"))
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
	raw := strings.TrimSpace(fmt.Sprint(err))
	lower := strings.ToLower(raw)
	switch {
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
