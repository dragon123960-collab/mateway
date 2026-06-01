package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/agentprofile"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/i18n"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/schedule"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/tool"
)

type Runtime struct {
	Config *config.Root
	Store  session.Store
	Tools  *agentcore.ToolRegistry
	Model  agentcore.Model
	Pool   AgentPool
	Hooks  RuntimeHooks
}

type Response struct {
	Reply     channel.OutboundMessage
	TraceID   string
	TracePath string
	Failed    bool
}

func New(cfg *config.Root) Runtime {
	hooks := defaultRuntimeHooks()
	hooks.Providers = append(hooks.Providers, staticContextHookProvider{config: cfg})
	hooks.Providers = append(hooks.Providers, memorySafeReadHookProvider{config: cfg})
	hooks.Providers = append(hooks.Providers, ruleFollowupHookProvider{})
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
	_ = trace.write(map[string]any{"type": "request", "session_key": msg.SessionKey, "channel": msg.Channel, "text": msg.Text})
	defer func() {
		_ = trace.write(map[string]any{"type": "runtime_done", "duration_ms": time.Since(start).Milliseconds()})
	}()
	if isNewSessionCommand(msg.Text) {
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
	decision := rt.Hooks.resolveFollowup(ctx, FollowupHookInput{State: state, Text: msg.Text}, trace)
	if decision.Kind == followupClarify {
		task := state.StartTask(msg.Text)
		state.Pending = &session.PendingAction{Kind: "user_input", TaskID: task.ID, Question: decision.ClarifyPrompt, ResumeText: decision.Reason}
		state.BlockActiveTask("await_user_input")
		if err := rt.saveState(&state, trace); err != nil {
			return Response{}, err
		}
		resp := rt.reply(msg, decision.ClarifyPrompt, "clarify")
		resp.TraceID = trace.id
		resp.TracePath = trace.path
		_ = trace.write(map[string]any{"type": "reply", "text": resp.Reply.Text, "style": resp.Reply.Style, "runtime_duration_ms": time.Since(start).Milliseconds()})
		return resp, nil
	}
	userText := strings.TrimSpace(msg.Text)
	if decision.Kind == followupContinuation {
		task := state.ActivateTask(decision.TaskID)
		if task == nil {
			task = state.StartTask(msg.Text)
		}
		if strings.TrimSpace(decision.ResolvedUserText) != "" {
			userText = decision.ResolvedUserText
		}
		return rt.runTask(ctx, msg, &state, task, userText, trace)
	}
	task := state.StartTask(msg.Text)
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
				Text:     "当前会话上下文仍然过大，已停止这次请求。请发送 `/new` 开启干净会话，旧会话会自动归档。",
				Style:    "error",
				Locale:   runtimeLocale(rt.Config, msg),
			},
			TraceID:   trace.id,
			TracePath: trace.path,
			Failed:    true,
		}
		_ = trace.write(map[string]any{"type": "reply", "text": resp.Reply.Text, "style": resp.Reply.Style})
		return resp, nil
	}
	writeCompactTrace(trace, "model_input_compacted", compactStats)
	messages = append(messages, agentcore.Message{Role: agentcore.RoleUser, Content: userText})

	agent := rt.Pool.AgentForMessage(msg)
	if agent == nil {
		agent = agentcore.NewAgent(rt.Model, rt.Tools)
	}
	agent.Messages = messages
	agent.MaxIterations = 6
	profile := rt.Pool.ProfileForMessage(msg)
	discoveredSkills := discoverSkillsForAgent(rt.Config, profile.ID, 12)
	agent.Hooks = rt.hooksForState(state, task.ID, trace, rt.Hooks.contextMessages(ctx, ContextHookInput{
		Message:  msg,
		State:    *state,
		TaskID:   task.ID,
		UserText: userText,
		Profile:  profile,
	}, trace))
	result, err := agent.Continue(ctx)
	if err != nil {
		state.BlockActiveTask("failed")
		if saveErr := rt.saveState(state, trace); saveErr != nil {
			return Response{}, saveErr
		}
		text := friendlyRuntimeError(err)
		resp := Response{
			Reply: channel.OutboundMessage{
				Channel:  msg.Channel,
				ThreadID: msg.ThreadID,
				Text:     text,
				Style:    "error",
				Locale:   runtimeLocale(rt.Config, msg),
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
	usage := usageFromMessages(result.Messages)
	addUsage(&state.Usage, usage)
	writeUsageTrace(trace, usage)
	taskCompleted := false
	if state.Pending == nil {
		if looksLikeInputRequest(result.FinalText) {
			state.Pending = &session.PendingAction{Kind: "user_input", TaskID: task.ID, Question: result.FinalText}
			state.BlockActiveTask("await_user_input")
		} else {
			state.CompleteActiveTaskWithSummary(summarize(result.FinalText), trace.id, trace.path)
			taskCompleted = true
		}
	}
	if err := rt.saveState(state, trace); err != nil {
		return Response{}, err
	}
	if proposalID := pendingAgentProfileProposalID(task); proposalID != "" {
		state.Pending = &session.PendingAction{
			Kind:       "agent_profile_proposal_review",
			TaskID:     task.ID,
			ProposalID: proposalID,
			Question:   runtimeText(rt.Config, msg, "agent_profile.review.question", nil),
		}
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, err
		}
	}
	if scheduleID := pendingScheduleID(result.Messages); scheduleID != "" {
		state.Pending = &session.PendingAction{
			Kind:       "schedule_review",
			TaskID:     task.ID,
			ScheduleID: scheduleID,
			Question:   runtimeText(rt.Config, msg, "schedule.review.question", textValues("schedule_id", scheduleID)),
		}
		state.BlockActiveTask("await_schedule_test")
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, err
		}
		resp := rt.reply(msg, state.Pending.Question, "schedule_review_pending")
		resp.TraceID = trace.id
		resp.TracePath = trace.path
		_ = trace.write(map[string]any{"type": "schedule_review_pending", "schedule_id": scheduleID})
		_ = trace.write(map[string]any{"type": "reply", "text": resp.Reply.Text, "style": resp.Reply.Style})
		return resp, nil
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
			FinalText:  result.FinalText,
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
					Question:   runtimeText(rt.Config, msg, "memory.review.question", nil),
				}
				if err := rt.saveState(state, trace); err != nil {
					return Response{}, err
				}
			}
		}
	}
	text := rt.Hooks.response(ctx, ResponseHookInput{RawText: result.FinalText, LearningResult: learningResult}, trace)
	if learningResult == nil || learningResult.Proposal == nil {
		if nudge, err := memory.PendingProposalNudge(rt.home(), state.Key, time.Now(), rt.memoryProposalNudgeOptions(msg)); err == nil && nudge != "" {
			text = strings.TrimSpace(text) + "\n\n" + nudge
			_ = trace.write(map[string]any{"type": "memory_proposal_nudge", "text": nudge})
		}
	}
	resp := Response{
		Reply: channel.OutboundMessage{
			Channel:  msg.Channel,
			ThreadID: msg.ThreadID,
			Text:     text,
			Locale:   runtimeLocale(rt.Config, msg),
		},
		TraceID:   trace.id,
		TracePath: trace.path,
	}
	_ = trace.write(map[string]any{"type": "reply", "text": resp.Reply.Text})
	return resp, nil
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
	text := "已开启新会话。"
	if archivePath != "" {
		text += "\n旧会话已归档：" + archivePath
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

func isNewSessionCommand(text string) bool {
	switch strings.TrimSpace(strings.ToLower(text)) {
	case "/new", "/新会话", "新会话":
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

func (rt Runtime) home() string {
	home := config.DefaultHome()
	if rt.Config != nil && strings.TrimSpace(rt.Config.App.Home) != "" {
		home = rt.Config.App.Home
	}
	return home
}

func proposalID(proposal *memory.Proposal) string {
	if proposal == nil {
		return ""
	}
	return proposal.ID
}

func pendingScheduleID(messages []agentcore.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != agentcore.RoleAssistant {
			continue
		}
		for _, call := range msg.ToolCalls {
			if call.Name != "schedule.create" {
				continue
			}
			if raw := strings.TrimSpace(fmt.Sprint(call.Args["require_test"])); strings.EqualFold(raw, "false") || strings.EqualFold(raw, "no") {
				return ""
			}
			if id := scheduleIDFromFollowingToolResult(messages, i, call.ID); id != "" {
				return id
			}
		}
	}
	return ""
}

func pendingAgentProfileProposalID(task *session.TaskNode) string {
	if task == nil {
		return ""
	}
	for i := len(task.Steps) - 1; i >= 0; i-- {
		if task.Steps[i].Tool != "file.write" || task.Steps[i].Status != "accepted" {
			continue
		}
		if id := strings.TrimSpace(fmt.Sprint(task.Steps[i].Evidence["proposal_id"])); id != "" {
			return id
		}
	}
	return ""
}

func scheduleIDFromFollowingToolResult(messages []agentcore.Message, assistantIndex int, toolCallID string) string {
	for _, msg := range messages[assistantIndex+1:] {
		if msg.Role == agentcore.RoleAssistant {
			break
		}
		if msg.Role != agentcore.RoleTool || msg.ToolCallID != toolCallID {
			continue
		}
		fields := strings.Fields(msg.Content)
		for i, field := range fields {
			if field == "scheduled" && i+1 < len(fields) {
				return strings.TrimSpace(fields[i+1])
			}
		}
	}
	return ""
}

func appendMemoryReviewBlock(text string, proposal memory.Proposal) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(text))
	b.WriteString("\n\n我发现一条可能值得保存的长期记忆。\n")
	b.WriteString("这只是候选，还没有写入长期记忆。你可以选择保存，或忽略这次建议。\n\n")
	b.WriteString("建议保存：")
	b.WriteString(proposal.Type)
	b.WriteString(" - ")
	b.WriteString(proposal.Title)
	if len(proposal.Sources) > 0 {
		b.WriteString("\n来源：")
		b.WriteString(summarize(strings.Join(proposal.Sources, ", ")))
	}
	b.WriteString("\n\n保存到长期记忆：\n`mateway memory proposal commit ")
	b.WriteString(proposal.ID)
	b.WriteString("`\n\n忽略这条候选：\n`mateway memory proposal reject ")
	b.WriteString(proposal.ID)
	b.WriteString("`\n\n判断口径：如果这是以后会反复用到的项目经验、偏好、流程或工具用法，就保存；如果只是一次性测试或临时结果，就忽略。")
	return b.String()
}

func fallbackFinalReply(raw string) string {
	if strings.Contains(strings.ToUpper(raw), "[TOOL_CALL]") {
		return "模型生成了无效的工具调用格式，已停止执行，避免误操作。请重试或把任务说得更具体。"
	}
	return "我还没有生成可用回复。"
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

func (rt Runtime) hooksForState(state *session.State, taskID string, trace *traceRecorder, steering []agentcore.Message) agentcore.Hooks {
	steeringSent := false
	return agentcore.Hooks{
		Emit: trace.emit,
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
				state.Pending = &session.PendingAction{
					Kind:       "confirm_tool",
					TaskID:     taskID,
					ToolCall:   input.ToolCall,
					ResumeText: policy.ResumeText,
				}
				state.BlockActiveTask("await_confirm")
				return agentcore.BeforeToolCallResult{Block: true, Reason: policy.Reason}, nil
			}
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
			}
			return agentcore.AfterToolCallResult{}, nil
		},
	}
}

func (rt Runtime) handlePending(ctx context.Context, state *session.State, msg channel.InboundMessage, trace *traceRecorder) (Response, bool, error) {
	if state.Pending == nil {
		return Response{}, false, nil
	}
	text := strings.TrimSpace(msg.Text)
	switch state.Pending.Kind {
	case "confirm_tool":
		if rt.isCancel(msg, text) {
			state.Pending = nil
			state.BlockActiveTask("cancelled")
			if err := rt.saveState(state, trace); err != nil {
				return Response{}, true, err
			}
			return rt.reply(msg, "已取消。", "cancelled"), true, nil
		}
		if !rt.isConfirm(msg, text) {
			return rt.reply(msg, runtimeText(rt.Config, msg, "approval.confirm.generic", nil), "approval_pending"), true, nil
		}
		call := state.Pending.ToolCall
		state.Pending = nil
		_ = trace.write(map[string]any{"type": "pending_confirmed", "tool_call": call})
		result := rt.Tools.Execute(ctx, call)
		toolDef, _ := rt.Tools.Get(call.Name)
		observe := rt.Hooks.observe(ctx, ObserveHookInput{
			Kind:       "tool_result",
			State:      *state,
			TaskID:     state.ActiveTask,
			ToolCall:   call,
			Tool:       toolDef,
			ToolResult: result,
		}, trace)
		status := ""
		evidence := map[string]any{}
		if observe.TaskStep != nil {
			state.AddStep(state.ActiveTask, *observe.TaskStep)
			status = observe.TaskStep.Status
			evidence = observe.TaskStep.Evidence
		}
		_ = trace.write(map[string]any{"type": "tool_execution_end", "tool_call": call, "tool_result": redactToolResult(result), "acceptance": status, "evidence": redactSecrets(evidence)})
		state.Messages = append(state.Messages,
			agentcore.Message{Role: agentcore.RoleUser, Content: text},
			agentcore.Message{Role: agentcore.RoleTool, ToolCallID: call.ID, Content: result.Content},
		)
		if !result.IsError {
			state.CompleteActiveTaskWithSummary(summarize(result.Content), trace.id, trace.path)
		}
		if proposalID := strings.TrimSpace(fmt.Sprint(evidence["proposal_id"])); proposalID != "" {
			state.Pending = &session.PendingAction{
				Kind:       "agent_profile_proposal_review",
				TaskID:     state.ActiveTask,
				ProposalID: proposalID,
				Question:   runtimeText(rt.Config, msg, "agent_profile.review.question", nil),
			}
		}
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, true, err
		}
		if result.IsError {
			return rt.reply(msg, result.Content, "error"), true, nil
		}
		return rt.reply(msg, result.Content, "completed"), true, nil
	case "memory_proposal_review":
		action, ok := rt.parseMemoryProposalReviewAction(msg, text)
		if !ok {
			if rt.shouldBypassMemoryProposalReview(msg, text) {
				_ = trace.write(map[string]any{"type": "memory_proposal_review_bypassed", "proposal_id": state.Pending.ProposalID, "text": text})
				state.Pending = nil
				if err := rt.saveState(state, trace); err != nil {
					return Response{}, true, err
				}
				return Response{}, false, nil
			}
			_ = trace.write(map[string]any{"type": "memory_proposal_review_deferred", "proposal_id": state.Pending.ProposalID, "text": text})
			state.Pending = nil
			if err := rt.saveState(state, trace); err != nil {
				return Response{}, true, err
			}
			return Response{}, false, nil
		}
		proposalID := state.Pending.ProposalID
		state.Pending = nil
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
		proposal, err := store.Reject(proposalID, "user ignored from conversation")
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
	case "agent_profile_proposal_review":
		action, ok := rt.parseAgentProfileProposalReviewAction(msg, text)
		if !ok {
			if rt.shouldBypassAgentProfileProposalReview(msg, text) {
				_ = trace.write(map[string]any{"type": "agent_profile_proposal_review_deferred", "proposal_id": state.Pending.ProposalID, "text": text})
				state.Pending = nil
				if err := rt.saveState(state, trace); err != nil {
					return Response{}, true, err
				}
				return Response{}, false, nil
			}
			return rt.reply(msg, runtimeText(rt.Config, msg, "agent_profile.review.pending", nil), "agent_profile_review_pending"), true, nil
		}
		proposalID := state.Pending.ProposalID
		state.Pending = nil
		store := agentprofile.NewStore(rt.Config)
		if action == "promote" {
			proposal, backupDir, err := store.Promote(proposalID)
			if err != nil {
				if saveErr := rt.saveState(state, trace); saveErr != nil {
					return Response{}, true, saveErr
				}
				return rt.reply(msg, runtimeText(rt.Config, msg, "agent_profile.promote.error", textValues("error", err.Error())), "error"), true, nil
			}
			_ = trace.write(map[string]any{"type": "agent_profile_proposal_promoted", "proposal_id": proposal.ID, "target": proposal.TargetPath, "backup_dir": backupDir})
			if err := rt.saveState(state, trace); err != nil {
				return Response{}, true, err
			}
			return rt.reply(msg, runtimeText(rt.Config, msg, "agent_profile.promote.done", textValues("target", proposal.TargetPath, "backup", backupDir)), "completed"), true, nil
		}
		proposal, err := store.Reject(proposalID, "user rejected from conversation")
		if err != nil {
			if saveErr := rt.saveState(state, trace); saveErr != nil {
				return Response{}, true, saveErr
			}
			return rt.reply(msg, runtimeText(rt.Config, msg, "agent_profile.reject.error", textValues("error", err.Error())), "error"), true, nil
		}
		_ = trace.write(map[string]any{"type": "agent_profile_proposal_rejected", "proposal_id": proposal.ID})
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, true, err
		}
		return rt.reply(msg, runtimeText(rt.Config, msg, "agent_profile.reject.done", nil), "completed"), true, nil
	case "schedule_review":
		action, ok := rt.parseScheduleReviewAction(msg, text)
		if !ok {
			if shouldBypassScheduleReview(text) {
				_ = trace.write(map[string]any{"type": "schedule_review_bypassed", "schedule_id": state.Pending.ScheduleID, "text": text})
				state.Pending = nil
				if err := rt.saveState(state, trace); err != nil {
					return Response{}, true, err
				}
				return Response{}, false, nil
			}
			return rt.reply(msg, runtimeText(rt.Config, msg, "schedule.review.pending", textValues("schedule_id", state.Pending.ScheduleID)), "schedule_review_pending"), true, nil
		}
		scheduleID := state.Pending.ScheduleID
		taskID := state.Pending.TaskID
		state.Pending = nil
		if action == "cancel" {
			store := schedule.Store{Home: rt.home()}
			if _, err := store.Pause(scheduleID); err != nil {
				if saveErr := rt.saveState(state, trace); saveErr != nil {
					return Response{}, true, saveErr
				}
				return rt.reply(msg, runtimeText(rt.Config, msg, "schedule.cancel.error", textValues("error", err.Error())), "error"), true, nil
			}
			blockTask(state, taskID, "cancelled")
			if err := rt.saveState(state, trace); err != nil {
				return Response{}, true, err
			}
			return rt.reply(msg, runtimeText(rt.Config, msg, "schedule.cancel.done", nil), "cancelled"), true, nil
		}
		task, record, err := rt.testAndActivateSchedule(ctx, scheduleID)
		if err != nil {
			if saveErr := rt.saveState(state, trace); saveErr != nil {
				return Response{}, true, saveErr
			}
			return rt.reply(msg, runtimeText(rt.Config, msg, "schedule.test.error", textValues("error", err.Error())), "error"), true, nil
		}
		state.ActivateTask(taskID)
		state.CompleteActiveTaskWithSummary("定时任务试运行成功："+task.ID, trace.id, trace.path)
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, true, err
		}
		_ = trace.write(map[string]any{"type": "schedule_review_tested", "schedule_id": task.ID, "run_id": record.ID, "status": record.Status})
		return rt.reply(msg, runtimeText(rt.Config, msg, "schedule.test.done", textValues("task_id", task.ID, "run_at", task.RunAt)), "completed"), true, nil
	case "user_input":
		if shouldBypassUserInputPending(state.Pending, text) {
			taskID := state.Pending.TaskID
			state.Pending = nil
			blockTask(state, taskID, "interrupted")
			if state.ActiveTask == taskID {
				state.ActiveTask = ""
			}
			_ = trace.write(map[string]any{"type": "pending_user_input_bypassed", "task_id": taskID, "text": text, "reason": "standalone task request"})
			if err := rt.saveState(state, trace); err != nil {
				return Response{}, true, err
			}
			return Response{}, false, nil
		}
		taskID := state.Pending.TaskID
		state.Pending = nil
		state.Messages = append(state.Messages,
			agentcore.Message{Role: agentcore.RoleUser, Content: text},
		)
		_ = trace.write(map[string]any{"type": "pending_user_input", "task_id": taskID, "text": text})
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, true, err
		}
		return Response{}, false, nil
	default:
		state.Pending = nil
		return Response{}, false, nil
	}
}

func (rt Runtime) parseMemoryProposalReviewAction(msg channel.InboundMessage, text string) (string, bool) {
	action, ok := runtimeAlias(rt.Config, msg, text, "memory_commit", "memory_reject")
	switch {
	case ok && action == "memory_commit":
		return "commit", true
	case ok && action == "memory_reject":
		return "reject", true
	default:
		return "", false
	}
}

func (rt Runtime) shouldBypassMemoryProposalReview(msg channel.InboundMessage, text string) bool {
	normalized := normalizeFollowupText(text)
	if normalized == "" {
		return false
	}
	if rt.isConfirm(msg, normalized) || rt.isCancel(msg, normalized) {
		return false
	}
	if isShortContextDependent(normalized) {
		return false
	}
	return true
}

func (rt Runtime) parseAgentProfileProposalReviewAction(msg channel.InboundMessage, text string) (string, bool) {
	action, ok := runtimeAlias(rt.Config, msg, text, "promote", "reject")
	switch {
	case ok && action == "promote":
		return "promote", true
	case ok && action == "reject":
		return "reject", true
	default:
		return "", false
	}
}

func (rt Runtime) shouldBypassAgentProfileProposalReview(msg channel.InboundMessage, text string) bool {
	normalized := normalizeFollowupText(text)
	if normalized == "" || rt.isConfirm(msg, normalized) || rt.isCancel(msg, normalized) || isShortContextDependent(normalized) {
		return false
	}
	return true
}

func (rt Runtime) parseScheduleReviewAction(msg channel.InboundMessage, text string) (string, bool) {
	action, ok := runtimeAlias(rt.Config, msg, text, "run", "cancel")
	switch {
	case ok && action == "run":
		return "test", true
	case ok && action == "cancel":
		return "cancel", true
	default:
		return "", false
	}
}

func shouldBypassScheduleReview(text string) bool {
	normalized := normalizeFollowupText(text)
	return looksLikeStandaloneTaskRequest(normalized)
}

func (rt Runtime) testAndActivateSchedule(ctx context.Context, scheduleID string) (schedule.Task, schedule.RunRecord, error) {
	store := schedule.Store{Home: rt.home()}
	task, err := store.Read(scheduleID)
	if err != nil {
		return task, schedule.RunRecord{}, err
	}
	startedAt := time.Now()
	sessionKey := strings.TrimSpace(task.SessionKey)
	if sessionKey == "" {
		sessionKey = "schedule:" + task.ID
	}
	runRuntime := New(rt.Config)
	resp, runErr := runRuntime.Handle(ctx, channel.InboundMessage{
		ID:         task.ID,
		Channel:    "schedule",
		SessionKey: sessionKey,
		Text:       task.Text,
		Metadata:   map[string]string{"scheduled_task_id": task.ID, "scheduled_run_kind": "test"},
	})
	status := "success"
	errText := ""
	output := ""
	tracePath := ""
	if runErr != nil {
		status = "error"
		errText = runErr.Error()
	} else {
		output = strings.TrimSpace(resp.Reply.Text)
		tracePath = resp.TracePath
		if resp.Failed {
			status = "error"
			errText = strings.TrimSpace(resp.Reply.Text)
		}
	}
	record, recordErr := store.RecordRun(schedule.RunRecord{
		TaskID:     task.ID,
		Kind:       "test",
		Status:     status,
		StartedAt:  startedAt.Format(time.RFC3339),
		FinishedAt: time.Now().Format(time.RFC3339),
		SessionKey: sessionKey,
		Output:     output,
		TracePath:  tracePath,
		Error:      errText,
	})
	if recordErr != nil {
		return task, record, recordErr
	}
	if status != "success" {
		if markErr := store.MarkError(task, time.Now(), record); markErr != nil {
			return task, record, markErr
		}
		return task, record, fmt.Errorf(firstNonEmpty(errText, "scheduled task test failed"))
	}
	if err := store.MarkTested(task, time.Now(), record); err != nil {
		return task, record, err
	}
	task, err = store.Read(scheduleID)
	return task, record, err
}

func blockTask(state *session.State, taskID, status string) {
	if state == nil || strings.TrimSpace(taskID) == "" {
		return
	}
	for i := range state.Tasks {
		if state.Tasks[i].ID == taskID {
			state.Tasks[i].Status = status
			state.Tasks[i].UpdatedAt = time.Now()
			return
		}
	}
}

func shouldBypassUserInputPending(pending *session.PendingAction, text string) bool {
	if pending == nil || pending.Kind != "user_input" {
		return false
	}
	normalized := normalizeFollowupText(text)
	if normalized == "" || isConfirm(normalized) || isCancel(normalized) {
		return false
	}
	if isFollowupCue(normalized) || isHistoricalCue(normalized) || isRetryCue(normalized) || isShortContextDependent(normalized) {
		return false
	}
	return looksLikeStandaloneTaskRequest(normalized)
}

func looksLikeStandaloneTaskRequest(text string) bool {
	taskVerbs := []string{
		"请读取", "请总结", "请查看", "请检查", "请搜索", "请创建", "请生成", "请列出",
		"帮我读取", "帮我总结", "帮我查看", "帮我检查", "帮我搜索", "帮我创建", "帮我生成",
		"读取", "总结", "查看", "检查", "搜索", "创建", "生成", "列出",
		"read", "summarize", "check", "search", "create", "generate", "list",
	}
	hasVerb := false
	for _, verb := range taskVerbs {
		if strings.Contains(text, verb) {
			hasVerb = true
			break
		}
	}
	if !hasVerb {
		return false
	}
	for _, marker := range []string{"readme", ".md", ".txt", ".json", ".yaml", ".yml", "/", "~", "项目", "文件", "目录", "邮件", "网页", "网站"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func acceptToolResult(tool agentcore.Tool, result agentcore.ToolResult) (string, map[string]any) {
	evidence := map[string]any{}
	for key, value := range result.Evidence {
		evidence[key] = value
	}
	if tool != nil {
		contract := agentcore.ContractFor(tool)
		if contract.Acceptance != "" {
			evidence["acceptance_criteria"] = contract.Acceptance
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
	return Response{Reply: channel.OutboundMessage{Channel: msg.Channel, ThreadID: msg.ThreadID, Text: text, Style: style, Locale: runtimeLocale(rt.Config, msg)}}
}

func (rt Runtime) isConfirm(msg channel.InboundMessage, text string) bool {
	_, ok := runtimeAlias(rt.Config, msg, text, "confirm")
	return ok
}

func (rt Runtime) isCancel(msg channel.InboundMessage, text string) bool {
	_, ok := runtimeAlias(rt.Config, msg, text, "cancel")
	return ok
}

func isConfirm(text string) bool {
	_, ok := i18n.New(i18n.Config{}).MatchAlias("", text, "confirm")
	return ok
}

func isCancel(text string) bool {
	_, ok := i18n.New(i18n.Config{}).MatchAlias("", text, "cancel")
	return ok
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
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: "收到：" + text}, nil
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
	if strings.Contains(trimmed, "需要你") || strings.Contains(trimmed, "请提供") || strings.Contains(trimmed, "请补充") {
		return true
	}
	return strings.HasSuffix(trimmed, "？") && (strings.Contains(trimmed, "哪个") || strings.Contains(trimmed, "什么") || strings.Contains(trimmed, "是否"))
}

func friendlyRuntimeError(err error) string {
	raw := strings.TrimSpace(fmt.Sprint(err))
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "context deadline exceeded") || strings.Contains(lower, "client.timeout"):
		return "模型服务这次响应超时了，任务已经停在安全位置。你可以直接回复“重试”或把问题再发一遍，我会接着当前上下文继续。"
	case strings.Contains(lower, "model api key is empty"):
		return "当前模型配置缺少 API Key，任务没有继续执行。请检查模型配置后重试。"
	case strings.Contains(lower, "all models failed"):
		return "当前可用模型都调用失败了，任务已经停在安全位置。你可以稍后回复“重试”，或切换/检查 fallback 模型配置。"
	default:
		if raw == "" {
			return "任务执行失败了，已经停在安全位置。你可以补充信息后重试。"
		}
		return "任务执行失败了，已经停在安全位置。你可以补充信息后重试。"
	}
}
