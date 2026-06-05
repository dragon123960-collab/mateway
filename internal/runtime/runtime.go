package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/agentprofile"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/i18n"
	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/schedule"
	"github.com/dongping/mateway/internal/script"
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
	FollowUps []channel.OutboundMessage
	TraceID   string
	TracePath string
	Failed    bool
}

func New(cfg *config.Root) Runtime {
	hooks := defaultRuntimeHooks()
	hooks.Providers = append(hooks.Providers, staticContextHookProvider{config: cfg})
	hooks.Providers = append(hooks.Providers, memorySafeReadHookProvider{config: cfg})
	hooks.Providers = append(hooks.Providers, modelFollowupHookProvider{})
	hooks.Providers = append(hooks.Providers, modelPendingIntentHookProvider{})
	hooks.Providers = append(hooks.Providers, defaultCompletionReviewHookProvider{})
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
	if resp, handled, err := rt.maybeRecallArchivedTask(ctx, &state, msg, trace); handled || err != nil {
		if handled && err == nil {
			resp.TraceID = trace.id
			resp.TracePath = trace.path
			_ = trace.write(map[string]any{"type": "reply", "text": resp.Reply.Text, "style": resp.Reply.Style, "runtime_duration_ms": time.Since(start).Milliseconds()})
		}
		return resp, err
	}
	agent := rt.Pool.AgentForMessage(msg)
	if agent == nil {
		agent = agentcore.NewAgent(rt.Model, rt.Tools)
	}
	followupModel := rt.Pool.RoleModelForMessage(msg, "followup", agent.Model)
	decision := rt.Hooks.resolveFollowup(ctx, FollowupHookInput{State: state, Text: msg.Text, Model: followupModel, Locale: runtimeLocale(rt.Config, msg), CatalogDir: runtimeCatalogDir(rt.Config)}, trace)
	if decision.Kind == followupClarify {
		task := state.StartTask(msg.Text)
		applyCompletionContract(task, msg.Text)
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
			applyCompletionContract(task, msg.Text)
		} else if strings.TrimSpace(task.CompletionContract.SuccessCondition) == "" {
			applyCompletionContract(task, task.Goal)
		}
		if strings.TrimSpace(decision.ResolvedUserText) != "" {
			userText = decision.ResolvedUserText
		}
		return rt.runTask(ctx, msg, &state, task, userText, trace)
	}
	task := state.StartTask(msg.Text)
	applyCompletionContract(task, msg.Text)
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
	if strings.TrimSpace(userText) != "" || len(msg.Parts) > 0 {
		messages = append(messages, userAgentMessage(userText, msg.Parts))
	}

	agent := rt.Pool.AgentForMessage(msg)
	if agent == nil {
		agent = agentcore.NewAgent(rt.Model, rt.Tools)
	}
	profile := rt.Pool.ProfileForMessage(msg)
	discoveredSkills := discoverSkillsForAgent(rt.Config, profile.ID, 12)
	agent.SystemPrompt = prependTaskFocus(buildRuntimeSystemContext(rt.Config, profile), task, userText)
	agent.Messages = messages
	agent.MaxParallelTools = maxParallelTools(rt.Config)
	agent.MaxIterations = maxIterations(rt.Config)
	reviewModel := rt.Pool.RoleModelForMessage(msg, "review", agent.Model)
	runCtx, stopActivityWatch, activityTimedOut := rt.withActivityWatchdog(ctx, trace, task.ID)
	defer stopActivityWatch()
	agentHooks, runtimeStopReason, latestCompletionReview := rt.hooksForState(state, task.ID, userText, runtimeLocale(rt.Config, msg), reviewModel, trace, rt.Hooks.contextMessages(ctx, ContextHookInput{
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
	if result.StopReason == "" {
		result.StopReason = runtimeStopReason()
	}

	state.Messages = redactMessagesForStorage(result.Messages)
	finalText := redactSecretString(result.FinalText)
	usage := usageFromMessages(result.Messages)
	addUsage(&state.Usage, usage)
	writeUsageTrace(trace, usage)
	if state.Pending != nil && state.Pending.Kind == "confirm_tool" {
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, err
		}
		question := strings.TrimSpace(state.Pending.Question)
		if question == "" {
			question = runtimeText(rt.Config, msg, "approval.confirm.generic", nil)
		}
		resp := rt.reply(msg, question, "approval_pending")
		resp.TraceID = trace.id
		resp.TracePath = trace.path
		_ = trace.write(map[string]any{"type": "approval_pending", "task_id": state.Pending.TaskID, "tool_call": state.Pending.ToolCall})
		_ = trace.write(map[string]any{"type": "reply", "text": resp.Reply.Text, "style": resp.Reply.Style})
		return resp, nil
	}
	taskCompleted := false
	blockedByFinalWarning := false
	if state.Pending == nil {
		if result.StopReason != "" {
			blockedByFinalWarning = true
			state.BlockActiveTask("failed")
			state.AddExecutionEvent(task.ID, session.ExecutionEvent{Type: result.StopReason, Status: "failed", Summary: result.StopReason, Evidence: map[string]any{"iterations": result.Iterations}})
			_ = trace.write(map[string]any{"type": result.StopReason, "task_id": task.ID, "status": "failed", "iterations": result.Iterations})
		} else if !completionReviewAllowsFinalAnswer(activeTaskSnapshot(state, task.ID), latestCompletionReview()) && looksLikeInputRequest(finalText) {
			state.Pending = &session.PendingAction{Kind: "user_input", TaskID: task.ID, Question: finalText}
			state.BlockActiveTask("await_user_input")
			state.AddExecutionEvent(task.ID, session.ExecutionEvent{Type: "await_user_input", Status: "awaiting_user_input", Summary: summarize(finalText)})
		} else if warning := finalTextWarning(finalText); warning != "" && !completionReviewAllowsFinalAnswer(activeTaskSnapshot(state, task.ID), latestCompletionReview()) {
			blockedByFinalWarning = true
			state.BlockActiveTask("failed")
			state.AddExecutionEvent(task.ID, session.ExecutionEvent{Type: "blocked", Status: "failed", Summary: warning, Evidence: map[string]any{"text": finalText}})
			_ = trace.write(map[string]any{"type": "task_blocked", "task_id": task.ID, "status": "failed", "reason": warning, "text": finalText})
		} else if warning := completionContractWarning(activeTaskSnapshot(state, task.ID), finalText); warning != "" {
			blockedByFinalWarning = true
			state.BlockActiveTask("failed")
			state.AddExecutionEvent(task.ID, session.ExecutionEvent{Type: "completion_contract_blocked", Status: "failed", Summary: warning, Evidence: map[string]any{"text": finalText}})
			_ = trace.write(map[string]any{"type": "completion_contract_blocked", "task_id": task.ID, "status": "failed", "reason": warning, "contract": activeTaskSnapshot(state, task.ID).CompletionContract, "text": finalText})
		} else {
			state.CompleteActiveTaskWithSummary(summarize(finalText), trace.id, trace.path)
			state.AddExecutionEvent(task.ID, session.ExecutionEvent{Type: "completed", Status: "completed", Summary: summarize(finalText)})
			taskCompleted = true
		}
	}
	if err := rt.saveState(state, trace); err != nil {
		return Response{}, err
	}
	if proposalID := pendingAgentProfileProposalID(task); proposalID != "" {
		question := renderAgentProfileProposalReview(rt.Config, msg, proposalID)
		state.Pending = &session.PendingAction{
			Kind:       "agent_profile_proposal_review",
			TaskID:     task.ID,
			ProposalID: proposalID,
			Question:   question,
		}
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, err
		}
	}
	if scheduleID := pendingScheduleID(result.Messages); scheduleID != "" {
		question := renderScheduleReview(rt.Config, msg, rt.home(), scheduleID)
		state.Pending = &session.PendingAction{
			Kind:       "schedule_review",
			TaskID:     task.ID,
			ScheduleID: scheduleID,
			Question:   question,
		}
		state.BlockActiveTask("await_schedule_test")
		state.AddExecutionEvent(task.ID, session.ExecutionEvent{Type: "await_schedule_review", Status: "awaiting_user_input", Summary: "schedule review pending", Evidence: map[string]any{"schedule_id": scheduleID}})
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
					Question:   runtimeText(rt.Config, msg, "memory.review.question", nil),
				}
				if err := rt.saveState(state, trace); err != nil {
					return Response{}, err
				}
			}
		}
	}
	text := redactSecretString(rt.Hooks.response(ctx, ResponseHookInput{RawText: finalText, LearningResult: learningResult, Locale: runtimeLocale(rt.Config, msg), CatalogDir: runtimeCatalogDir(rt.Config)}, trace))
	var followUps []channel.OutboundMessage
	if learningResult != nil && learningResult.Proposal != nil {
		followUps = append(followUps, channel.OutboundMessage{
			Channel:  msg.Channel,
			ThreadID: msg.ThreadID,
			Text:     renderMemoryProposalReview(rt.Config, msg, *learningResult.Proposal),
			Style:    "memory_proposal_review",
			Locale:   runtimeLocale(rt.Config, msg),
		})
	}
	if learningResult == nil || learningResult.Proposal == nil {
		if nudge, err := memory.PendingProposalNudge(rt.home(), state.Key, time.Now(), rt.memoryProposalNudgeOptions(msg)); err == nil && nudge != "" {
			text = strings.TrimSpace(text) + "\n\n" + nudge
			_ = trace.write(map[string]any{"type": "memory_proposal_nudge", "text": nudge})
		}
	}
	if blockedByFinalWarning {
		text = renderPartialReply(rt.Config, msg, text)
	}
	style := ""
	if blockedByFinalWarning && style == "" {
		style = "partial"
	}
	resp := Response{
		Reply: channel.OutboundMessage{
			Channel:  msg.Channel,
			ThreadID: msg.ThreadID,
			Text:     text,
			Style:    style,
			Locale:   runtimeLocale(rt.Config, msg),
		},
		FollowUps: followUps,
		TraceID:   trace.id,
		TracePath: trace.path,
		Failed:    blockedByFinalWarning,
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
		Locale:       runtimeLocale(rt.Config, msg),
		CatalogDir:   runtimeCatalogDir(rt.Config),
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

type archivedTaskCandidate struct {
	ArchiveID string
	Task      session.TaskNode
}

func (rt Runtime) maybeRecallArchivedTask(_ context.Context, state *session.State, msg channel.InboundMessage, trace *traceRecorder) (Response, bool, error) {
	if state == nil || len(state.Tasks) > 0 || state.Pending != nil {
		return Response{}, false, nil
	}
	text := strings.TrimSpace(msg.Text)
	normalized := normalizeFollowupText(text)
	if !isHistoricalCue(normalized) && !isFollowupCue(normalized) && !isRetryCue(normalized) {
		return Response{}, false, nil
	}
	candidates, err := rt.archivedTaskCandidates(state.Key, text, 5)
	if err != nil {
		_ = trace.write(map[string]any{"type": "archive_task_recall_error", "error": err.Error()})
		return Response{}, false, nil
	}
	if len(candidates) == 0 {
		return Response{}, false, nil
	}
	if len(candidates) == 1 {
		candidate := candidates[0]
		state.Pending = &session.PendingAction{
			Kind:       "archive_task_recall",
			TaskID:     candidate.Task.ID,
			ArchiveID:  candidate.ArchiveID,
			Question:   renderArchiveRecallQuestion(rt.Config, msg, candidate),
			ResumeText: text,
		}
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, true, err
		}
		_ = trace.write(map[string]any{"type": "archive_task_recall_pending", "archive_id": candidate.ArchiveID, "task_id": candidate.Task.ID, "candidates": 1})
		return rt.reply(msg, state.Pending.Question, "archive_recall_pending"), true, nil
	}
	textOut := renderArchiveRecallCandidates(rt.Config, msg, candidates)
	_ = trace.write(map[string]any{"type": "archive_task_recall_clarify", "candidates": len(candidates)})
	return rt.reply(msg, textOut, "clarify"), true, nil
}

func (rt Runtime) archivedTaskCandidates(sessionKey, text string, limit int) ([]archivedTaskCandidate, error) {
	ids, err := rt.Store.ListArchives(sessionKey)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	normalized := normalizeFollowupText(text)
	var out []archivedTaskCandidate
	for i := len(ids) - 1; i >= 0 && len(out) < limit; i-- {
		archived, _, err := rt.Store.LoadArchive(sessionKey, ids[i])
		if err != nil {
			continue
		}
		for j := len(archived.Tasks) - 1; j >= 0 && len(out) < limit; j-- {
			task := archived.Tasks[j]
			haystack := normalizeFollowupText(task.Goal + " " + task.Summary)
			if tokenOverlap(normalized, haystack) >= 1 || containsAny(normalized, runtimeCueList(rt.Config, "router.archive_recall.cues")) {
				out = append(out, archivedTaskCandidate{ArchiveID: ids[i], Task: task})
			}
		}
	}
	return out, nil
}

func renderArchiveRecallQuestion(cfg *config.Root, msg channel.InboundMessage, candidate archivedTaskCandidate) string {
	goal := summarize(candidate.Task.Goal)
	if goal == "" {
		goal = candidate.Task.ID
	}
	return runtimeText(cfg, msg, "runtime.archive_recall.question", textValues("goal", goal))
}

func renderArchiveRecallCandidates(cfg *config.Root, msg channel.InboundMessage, candidates []archivedTaskCandidate) string {
	var b strings.Builder
	b.WriteString(runtimeText(cfg, msg, "runtime.archive_recall.candidates", nil))
	for i, candidate := range candidates {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, summarize(candidate.Task.Goal)))
	}
	return strings.TrimSpace(b.String())
}

func archivedTaskRecallText(task session.TaskNode, archiveID, current string) string {
	return "Continue from an archived task in the current new session.\nArchive ID: " + strings.TrimSpace(archiveID) +
		"\nArchived task ID: " + strings.TrimSpace(task.ID) +
		"\nArchived task goal: " + strings.TrimSpace(task.Goal) +
		"\nArchived task summary: " + strings.TrimSpace(task.Summary) +
		"\nCurrent user request: " + strings.TrimSpace(current)
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

func maxNoProgressTurns(cfg *config.Root) int {
	if cfg == nil || cfg.Execution.MaxNoProgressTurns <= 0 {
		return 2
	}
	return cfg.Execution.MaxNoProgressTurns
}

func maxRepeatedToolFailures(cfg *config.Root) int {
	if cfg == nil || cfg.Execution.MaxRepeatedToolFailures <= 0 {
		return 3
	}
	return cfg.Execution.MaxRepeatedToolFailures
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

func proposalIDFromEvidence(evidence map[string]any) string {
	id, ok := evidence["proposal_id"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(id)
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

func scheduleCreateRequiresTest(call agentcore.ToolCall) bool {
	raw := strings.TrimSpace(fmt.Sprint(call.Args["require_test"]))
	return !strings.EqualFold(raw, "false") && !strings.EqualFold(raw, "no")
}

func scheduleIDFromToolResult(result agentcore.ToolResult) string {
	if result.Evidence != nil {
		if id, ok := result.Evidence["id"].(string); ok && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	fields := strings.Fields(result.Content)
	for i, field := range fields {
		if field == "scheduled" && i+1 < len(fields) {
			return strings.TrimSpace(fields[i+1])
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
		id, ok := task.Steps[i].Evidence["proposal_id"].(string)
		if ok && strings.TrimSpace(id) != "" {
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

func renderAgentProfileProposalReview(cfg *config.Root, msg channel.InboundMessage, proposalID string) string {
	proposal, err := agentprofile.NewStore(cfg).Read(proposalID)
	if err != nil {
		return runtimeText(cfg, msg, "agent_profile.review.question", nil)
	}
	if strings.HasPrefix(runtimeLocale(cfg, msg), "zh") {
		var b strings.Builder
		b.WriteString("检测到 agent 核心 md 修改草稿，等待审核。\n\n")
		b.WriteString("草稿：")
		b.WriteString(proposal.ID)
		b.WriteString("\n目标：")
		b.WriteString(proposal.TargetPath)
		if summary := diffSummary(proposal.Diff); summary != "" {
			b.WriteString("\n摘要：")
			b.WriteString(summary)
		}
		b.WriteString("\n\n回复“确认”生效，回复“忽略”放弃；也可以继续发新任务。")
		return b.String()
	}
	var b strings.Builder
	b.WriteString("Detected an agent core md draft waiting for review.\n\n")
	b.WriteString("Draft: ")
	b.WriteString(proposal.ID)
	b.WriteString("\nTarget: ")
	b.WriteString(proposal.TargetPath)
	if summary := diffSummary(proposal.Diff); summary != "" {
		b.WriteString("\nSummary: ")
		b.WriteString(summary)
	}
	b.WriteString("\n\nReply \"confirm\" to promote it, or \"ignore\" to reject it. You can also send a new task.")
	return b.String()
}

func renderScheduleReview(cfg *config.Root, msg channel.InboundMessage, home, scheduleID string) string {
	task, err := (schedule.Store{Home: home}).Get(scheduleID)
	if err != nil {
		return runtimeText(cfg, msg, "schedule.review.question", textValues("schedule_id", scheduleID))
	}
	if strings.HasPrefix(runtimeLocale(cfg, msg), "zh") {
		var b strings.Builder
		b.WriteString("定时任务已记录为待试运行。\n\n")
		b.WriteString("任务：")
		b.WriteString(task.ID)
		b.WriteString("\n内容：")
		b.WriteString(summarize(task.Text))
		b.WriteString("\n执行时间：")
		b.WriteString(task.RunAt)
		if strings.TrimSpace(task.Interval) != "" {
			b.WriteString("\n重复间隔：")
			b.WriteString(task.Interval)
		}
		b.WriteString("\n\n回复“执行”现在试运行，试运行成功后我会激活它；回复“取消”放弃。也可以稍后手动执行：`mateway schedule test ")
		b.WriteString(task.ID)
		b.WriteString("`。")
		return b.String()
	}
	var b strings.Builder
	b.WriteString("The scheduled task is recorded and waiting for a test run.\n\n")
	b.WriteString("Task: ")
	b.WriteString(task.ID)
	b.WriteString("\nText: ")
	b.WriteString(summarize(task.Text))
	b.WriteString("\nRun at: ")
	b.WriteString(task.RunAt)
	if strings.TrimSpace(task.Interval) != "" {
		b.WriteString("\nInterval: ")
		b.WriteString(task.Interval)
	}
	b.WriteString("\n\nReply \"run\" to test it now; I will activate it after a successful test. Reply \"cancel\" to discard it. You can also run it later: `mateway schedule test ")
	b.WriteString(task.ID)
	b.WriteString("`.")
	return b.String()
}

func diffSummary(diff string) string {
	var parts []string
	for _, line := range strings.Split(diff, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "@@") {
			continue
		}
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			parts = append(parts, line)
		}
		if len(parts) >= 4 {
			break
		}
	}
	return summarize(strings.Join(parts, " "))
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

func fallbackFinalReply(raw, locale, catalogDir string) string {
	catalog := i18n.New(i18n.Config{CatalogDir: catalogDir})
	if strings.Contains(strings.ToUpper(raw), "[TOOL_CALL]") {
		return catalog.T(locale, "runtime.invalid_tool_call", nil)
	}
	return catalog.T(locale, "runtime.empty_reply", nil)
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

func activeTaskSnapshot(state *session.State, taskID string) session.TaskNode {
	if state == nil {
		return session.TaskNode{}
	}
	for _, task := range state.Tasks {
		if task.ID == taskID {
			return task
		}
	}
	return session.TaskNode{}
}

func applyCompletionContract(task *session.TaskNode, text string) {
	if task == nil {
		return
	}
	if looksLikeActionTask(text) {
		task.CompletionContract = session.CompletionContract{
			TaskType:          "action",
			RequiresMutation:  true,
			AllowsBlocker:     true,
			RequiresLLMReview: true,
			SuccessCondition:  "At least one accepted mutation tool step exists, or the task records a concrete blocker that prevents execution.",
		}
		return
	}
	task.CompletionContract = session.CompletionContract{
		TaskType:          "informational",
		RequiresLLMReview: true,
		SuccessCondition:  "Final answer is grounded in accepted evidence or clearly states what remains unverified.",
	}
}

func looksLikeActionTask(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	if looksLikeInformationalMetaQuestion(lower) {
		return false
	}
	if strings.HasPrefix(lower, "/read") || strings.HasPrefix(lower, "/search") {
		return false
	}
	for _, cue := range runtimeCueList(nil, "router.action.info_cues") {
		if strings.Contains(lower, cue) {
			return false
		}
	}
	for _, cue := range runtimeCueList(nil, "router.action.action_cues") {
		if strings.Contains(lower, cue) {
			return true
		}
	}
	if containsAny(lower, runtimeCueList(nil, "router.action.generate_cues")) {
		for _, artifact := range runtimeCueList(nil, "router.action.generated_artifacts") {
			if strings.Contains(lower, artifact) {
				return true
			}
		}
	}
	return false
}

func looksLikeInformationalMetaQuestion(lower string) bool {
	infoCues := []string{"只回答", "不要执行", "不执行", "不用执行", "脚本名", "参数形式", "which script", "do not execute", "don't execute", "without executing"}
	for _, cue := range infoCues {
		if strings.Contains(lower, cue) {
			return true
		}
	}
	return false
}

func isActionAckFollowup(text string) bool {
	normalized := normalizeFollowupText(text)
	if normalized == "" {
		return false
	}
	return looksLikeNonSubstantiveActionAck(text) ||
		containsExactCue(normalized, runtimeCueList(nil, "router.action.ack_exact")) ||
		containsAny(normalized, runtimeCueList(nil, "router.action.ack_contains"))
}

func completionContractWarning(task session.TaskNode, finalText string) string {
	contract := task.CompletionContract
	if !contract.RequiresMutation {
		return ""
	}
	if hasAcceptedMutationStep(task) {
		return ""
	}
	if contract.AllowsBlocker && looksLikeConcreteBlocker(finalText) {
		return ""
	}
	return "missing_accepted_mutation_evidence"
}

type deterministicCompletionResult struct {
	Completed bool
	Reason    string
	Evidence  map[string]any
}

func deterministicCompletionDecision(task session.TaskNode, finalText string) deterministicCompletionResult {
	contract := task.CompletionContract
	text := strings.TrimSpace(finalText)
	if text == "" || looksLikeNonSubstantiveActionAck(text) {
		return deterministicCompletionResult{}
	}
	if stronglyLooksIncompleteFinalText(text) {
		return deterministicCompletionResult{}
	}
	if warning := finalTextWarning(text); warning != "" {
		if !(contract.TaskType == "informational" && hasAcceptedReadOnlyEvidenceStep(task)) {
			return deterministicCompletionResult{}
		}
	}
	if contract.RequiresMutation {
		return deterministicCompletionResult{}
	}
	if contract.TaskType == "informational" && hasAcceptedReadOnlyEvidenceStep(task) {
		return deterministicCompletionResult{
			Completed: true,
			Reason:    "accepted_informational_evidence",
			Evidence:  completionEvidenceSummary(task),
		}
	}
	if contract.TaskType == "informational" && canCompleteInformationalReplyWithoutReview(task) {
		return deterministicCompletionResult{
			Completed: true,
			Reason:    "informational_reply_no_execution_required",
			Evidence:  completionEvidenceSummary(task),
		}
	}
	return deterministicCompletionResult{}
}

func canCompleteInformationalReplyWithoutReview(task session.TaskNode) bool {
	if len(task.Steps) > 0 || hasAcceptedEvidenceStep(task) {
		return false
	}
	if !looksLikeInformationalMetaQuestion(strings.ToLower(task.Goal)) {
		return false
	}
	frame := task.Execution
	if frame.Status == "await_confirm" || frame.Status == "await_user_input" || frame.Status == "failed" || frame.Status == "cancelled" {
		return false
	}
	for _, event := range frame.Events {
		switch event.Status {
		case "pending", "failed", "blocked", "cancelled":
			return false
		}
		switch event.Type {
		case "tool_call", "tool_result", "confirmation_requested", "await_confirm", "await_user_input":
			return false
		}
	}
	return true
}

func completionEvidenceSummary(task session.TaskNode) map[string]any {
	out := map[string]any{}
	accepted := 0
	mutations := 0
	var tools []string
	seen := map[string]bool{}
	for _, step := range task.Steps {
		if !(step.Accepted || step.Status == "accepted") {
			continue
		}
		accepted++
		if step.Mutation {
			mutations++
		}
		if step.Tool != "" && !seen[step.Tool] {
			tools = append(tools, step.Tool)
			seen[step.Tool] = true
		}
	}
	out["accepted_steps"] = accepted
	out["accepted_mutations"] = mutations
	if len(tools) > 0 {
		out["tools"] = tools
	}
	if task.Execution.Status != "" {
		out["frame_status"] = task.Execution.Status
	}
	return out
}

func stronglyLooksIncompleteFinalText(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || looksLikeNonSubstantiveActionAck(trimmed) || strings.HasSuffix(trimmed, ":") || strings.HasSuffix(trimmed, "：") {
		return true
	}
	lower := strings.ToLower(trimmed)
	strong := []string{
		"环境摸清",
		"环境梳清",
		"继续计划",
		"继续处理",
		"接下来并行",
		"下一步会",
		"然后我会",
		"我将",
		"准备开始",
		"先摸清",
		"先生成",
		"next i will",
		"i will now",
		"will proceed",
		"will continue",
		"continue now",
		"start writing",
		"start creating",
		"start sending",
	}
	for _, cue := range strong {
		if strings.Contains(lower, cue) || strings.Contains(trimmed, cue) {
			return true
		}
	}
	return false
}

func hasAcceptedMutationStep(task session.TaskNode) bool {
	for _, step := range task.Steps {
		if step.Accepted && step.Mutation {
			return true
		}
		if step.Status == "accepted" {
			risk := strings.TrimSpace(step.Risk)
			if risk == string(agentcore.RiskGuardedMutation) || risk == string(agentcore.RiskDangerous) {
				return true
			}
			if accepted, _ := step.Evidence["acceptance"].(string); accepted == "accepted" {
				if mutation, _ := step.Evidence["mutation"].(bool); mutation {
					return true
				}
			}
		}
	}
	for _, event := range task.Execution.Events {
		if event.Status != "accepted" {
			continue
		}
		if mutation, _ := event.Evidence["mutation"].(bool); mutation {
			return true
		}
		risk, _ := event.Evidence["risk"].(string)
		if risk == string(agentcore.RiskGuardedMutation) || risk == string(agentcore.RiskDangerous) {
			return true
		}
	}
	return false
}

func completionReviewAllowsFinalAnswer(task session.TaskNode, review CompletionReviewResult) bool {
	return review.Completed && hasAcceptedEvidenceStep(task)
}

func hasAcceptedEvidenceStep(task session.TaskNode) bool {
	for _, step := range task.Steps {
		if step.Accepted || step.Status == "accepted" {
			return true
		}
	}
	for _, event := range task.Execution.Events {
		if event.Status == "accepted" {
			return true
		}
	}
	return false
}

func hasAcceptedReadOnlyEvidenceStep(task session.TaskNode) bool {
	for _, step := range task.Steps {
		if !(step.Accepted || step.Status == "accepted") {
			continue
		}
		if step.Mutation {
			continue
		}
		risk := strings.TrimSpace(step.Risk)
		if risk == "" || risk == string(agentcore.RiskSafeRead) {
			return true
		}
	}
	for _, event := range task.Execution.Events {
		if event.Status != "accepted" {
			continue
		}
		if mutation, _ := event.Evidence["mutation"].(bool); mutation {
			continue
		}
		risk, _ := event.Evidence["risk"].(string)
		if strings.TrimSpace(risk) == "" || risk == string(agentcore.RiskSafeRead) {
			return true
		}
	}
	return false
}

func latestExecutionEvent(frame session.ExecutionFrame) session.ExecutionEvent {
	if len(frame.Events) == 0 {
		return session.ExecutionEvent{}
	}
	return frame.Events[len(frame.Events)-1]
}

func looksLikeConcreteBlocker(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	for _, blocker := range runtimeCueList(nil, "router.blocker.cues") {
		if strings.Contains(lower, blocker) {
			return true
		}
	}
	return false
}

func (rt Runtime) hooksForState(state *session.State, taskID, userText, locale string, model agentcore.Model, trace *traceRecorder, steering []agentcore.Message) (agentcore.Hooks, func() string, func() CompletionReviewResult) {
	steeringSent := false
	var followUps []agentcore.Message
	noProgressTurns := 0
	maxNoProgressTurns := maxNoProgressTurns(rt.Config)
	maxRepeatedToolFailures := maxRepeatedToolFailures(rt.Config)
	lastFailureSignature := ""
	repeatedToolFailures := 0
	stopReason := ""
	var lastCompletionReview CompletionReviewResult
	runtimeStopReason := func() string {
		return stopReason
	}
	latestCompletionReview := func() CompletionReviewResult {
		return lastCompletionReview
	}
	hooks := agentcore.Hooks{
		Emit: trace.emit,
		GetSteeringMessages: func(context.Context) ([]agentcore.Message, error) {
			if steeringSent {
				return nil, nil
			}
			steeringSent = true
			return append([]agentcore.Message(nil), steering...), nil
		},
		ShouldStopAfterTurn: func(_ context.Context, turn agentcore.TurnContext) (bool, error) {
			if len(turn.Message.ToolCalls) != 0 {
				return false, nil
			}
			task := activeTaskSnapshot(state, taskID)
			if decision := deterministicCompletionDecision(task, turn.Message.Content); decision.Completed {
				lastCompletionReview = CompletionReviewResult{
					Completed:         true,
					Reason:            decision.Reason,
					MissingItems:      nil,
					SuggestedFollowUp: "",
				}
				_ = trace.write(map[string]any{
					"type":      "completion_deterministic",
					"task_id":   taskID,
					"completed": true,
					"reason":    decision.Reason,
					"evidence":  decision.Evidence,
				})
				noProgressTurns = 0
				return true, nil
			}
			review := rt.Hooks.completionReview(context.Background(), CompletionReviewInput{
				UserText:           userText,
				Task:               task,
				FinalText:          turn.Message.Content,
				TranscriptMessages: turn.Messages,
				Model:              model,
			}, trace)
			lastCompletionReview = review
			_ = trace.write(map[string]any{
				"type":               "completion_review",
				"task_id":            taskID,
				"completed":          review.Completed,
				"reason":             review.Reason,
				"missing_items":      review.MissingItems,
				"suggested_followup": review.SuggestedFollowUp,
			})
			if !review.Completed {
				noProgressTurns++
				if maxNoProgressTurns > 0 && noProgressTurns >= maxNoProgressTurns {
					frame := activeTaskSnapshot(state, taskID).Execution
					_ = trace.write(map[string]any{
						"type":                 "task_no_progress",
						"task_id":              taskID,
						"turns":                noProgressTurns,
						"reason":               review.Reason,
						"missing_items":        review.MissingItems,
						"frame_status":         frame.Status,
						"frame_current_step":   frame.CurrentStepID,
						"frame_recent_event":   latestExecutionEvent(frame),
						"frame_current_node":   frame.CurrentNodeID,
						"frame_execution_mode": frame.Mode,
					})
					stopReason = "task_no_progress"
					return true, nil
				}
				followUp := strings.TrimSpace(review.SuggestedFollowUp)
				if followUp == "" {
					followUp = "Continue now. Immediately execute the next required step with tools. If you cannot call a tool, state the concrete blocker and what user input or permission is needed. Do not only describe a plan."
				}
				followUps = append(followUps, agentcore.Message{Role: agentcore.RoleUser, Content: followUp})
			} else {
				noProgressTurns = 0
			}
			return false, nil
		},
		GetFollowUpMessages: func(context.Context) ([]agentcore.Message, error) {
			if len(followUps) == 0 {
				return nil, nil
			}
			out := followUps
			followUps = nil
			return out, nil
		},
		BeforeToolCall: func(_ context.Context, input agentcore.BeforeToolCallContext) (agentcore.BeforeToolCallResult, error) {
			approvalKey, approvalClass := taskApprovalKey(input.ToolCall, input.Tool, rt.Config)
			if state.HasTaskApproval(taskID, approvalKey) && taskApprovalCanReuse(input.ToolCall, rt.Config) {
				_ = trace.write(map[string]any{"type": "approval_reused", "task_id": taskID, "tool": input.ToolCall.Name, "approval_key": approvalKey, "class": approvalClass})
				return agentcore.BeforeToolCallResult{}, nil
			}
			policy := rt.Hooks.toolPolicy(context.Background(), ToolPolicyHookInput{ToolCall: input.ToolCall, Tool: input.Tool, Config: rt.Config, Locale: locale}, trace)
			if policy.Block {
				task := activeTaskSnapshot(state, taskID)
				resume := buildToolResumeContext(task, input.ToolCall, approvalClass, policy.Reason, policy.AuthorizationOnly)
				frameID := state.SetResumeContext(taskID, resume)
				state.SetExecutionStatus(taskID, "awaiting_confirmation")
				state.AddExecutionEvent(taskID, session.ExecutionEvent{
					Type:    "await_confirmation",
					Status:  "awaiting_confirmation",
					Tool:    input.ToolCall.Name,
					Summary: resume.ActionSummary,
					Evidence: map[string]any{
						"approval_key": approvalKey,
						"class":        approvalClass,
						"reason":       policy.Reason,
					},
				})
				_ = trace.write(map[string]any{"type": "approval_required", "task_id": taskID, "frame_id": frameID, "tool": input.ToolCall.Name, "approval_key": approvalKey, "class": approvalClass, "reason": policy.Reason, "resume_context": resume})
				state.Pending = &session.PendingAction{
					Kind:              "confirm_tool",
					TaskID:            taskID,
					Question:          renderToolApprovalQuestion(locale, input.ToolCall, approvalClass, policy.Reason),
					ToolCall:          input.ToolCall,
					ResumeText:        policy.ResumeText,
					FrameID:           frameID,
					ResumeContext:     resume,
					AuthorizationOnly: policy.AuthorizationOnly,
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
				state.AddExecutionEvent(taskID, session.ExecutionEvent{
					Type:    "tool_result",
					Status:  observe.TaskStep.Status,
					Tool:    input.ToolCall.Name,
					StepID:  observe.TaskStep.ID,
					Summary: observe.TaskStep.Summary,
					Evidence: map[string]any{
						"accepted": observe.TaskStep.Accepted,
						"mutation": observe.TaskStep.Mutation,
						"risk":     observe.TaskStep.Risk,
					},
				})
				switch observe.TaskStep.Status {
				case "accepted":
					noProgressTurns = 0
					lastFailureSignature = ""
					repeatedToolFailures = 0
				case "failed", "suspect":
					signature := toolFailureSignature(input.ToolCall)
					if signature == lastFailureSignature {
						repeatedToolFailures++
					} else {
						lastFailureSignature = signature
						repeatedToolFailures = 1
					}
					if maxRepeatedToolFailures > 0 && repeatedToolFailures >= maxRepeatedToolFailures {
						_ = trace.write(map[string]any{
							"type":      "tool_failure_loop",
							"task_id":   taskID,
							"tool":      input.ToolCall.Name,
							"signature": signature,
							"count":     repeatedToolFailures,
							"status":    observe.TaskStep.Status,
						})
						result := input.ToolResult
						result.IsError = true
						result.Content = strings.TrimSpace(result.Content)
						if result.Content == "" {
							result.Content = runtimeText(rt.Config, channel.InboundMessage{Text: userText}, "runtime.tool_failure_loop", nil)
						} else {
							result.Content += "\n\n" + runtimeText(rt.Config, channel.InboundMessage{Text: userText}, "runtime.tool_failure_loop", nil)
						}
						return agentcore.AfterToolCallResult{ToolResult: &result, Terminate: true, StopReason: "tool_failure_loop"}, nil
					}
				}
			}
			return agentcore.AfterToolCallResult{}, nil
		},
	}
	return hooks, runtimeStopReason, latestCompletionReview
}

func toolFailureSignature(call agentcore.ToolCall) string {
	data, err := json.Marshal(call.Args)
	if err != nil {
		data = []byte(fmt.Sprint(call.Args))
	}
	sum := sha256.Sum256([]byte(call.Name + "\x00" + string(data)))
	return call.Name + ":" + hex.EncodeToString(sum[:8])
}

func taskApprovalKey(call agentcore.ToolCall, def agentcore.Tool, cfg *config.Root) (string, string) {
	class := ""
	switch call.Name {
	case "terminal.run":
		class = "terminal_guarded"
	case "script.run":
		class = "script"
	case "secret.set":
		class = "secret"
	default:
		if def != nil {
			class = string(def.Risk())
		}
	}
	if strings.TrimSpace(class) == "" {
		class = "guarded_mutation"
	}
	return call.Name + ":" + class, class
}

func taskApprovalCanReuse(call agentcore.ToolCall, cfg *config.Root) bool {
	switch call.Name {
	case "secret.set":
		return false
	case "script.run":
		return false
	case "terminal.run":
		command := fmt.Sprint(call.Args["command"])
		if tool.IsDangerousCommand(command) {
			return false
		}
		decision := tool.CheckTerminalCommand(command, cfg)
		return decision.Allow
	default:
		return true
	}
}

func renderToolApprovalQuestion(locale string, call agentcore.ToolCall, class, reason string) string {
	action := toolApprovalActionSummary(call)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "Confirmation is required before continuing."
		if strings.HasPrefix(locale, "zh") {
			reason = "继续之前需要确认。"
		}
	}
	if strings.HasPrefix(locale, "zh") {
		return strings.TrimSpace(fmt.Sprintf("继续之前需要确认。\n\n工具：%s\n风险：%s\n将执行：%s\n原因：%s\n\n如果这是常用 CLI 或连接器流程，跑通后建议沉淀为 skill script，用 script.run 规范执行并留下稳定证据。\n\n回复“确认”或 confirm 继续；回复“取消”或 cancel 放弃。", call.Name, firstNonEmpty(class, "guarded_mutation"), action, reason))
	}
	return strings.TrimSpace(fmt.Sprintf("Confirmation is required before continuing.\n\nTool: %s\nRisk: %s\nAction: %s\nReason: %s\n\nIf this is a recurring CLI or connector workflow, consider turning the verified flow into a skill script and running it through script.run for stable evidence.\n\nReply \"confirm\" to continue, or \"cancel\" to stop.", call.Name, firstNonEmpty(class, "guarded_mutation"), action, reason))
}

func toolApprovalActionSummary(call agentcore.ToolCall) string {
	if call.Name == "terminal.run" {
		return "command: " + redactSecretString(fmt.Sprint(call.Args["command"]))
	}
	if len(call.Args) == 0 {
		return call.Name
	}
	keys := make([]string, 0, len(call.Args))
	for key := range call.Args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+": "+compactApprovalValue(call.Args[key]))
	}
	return call.Name + " args: " + strings.Join(parts, ", ")
}

func buildToolResumeContext(task session.TaskNode, call agentcore.ToolCall, class, reason string, authorizationOnly bool) session.ResumeContext {
	action := toolApprovalActionSummary(call)
	originalTask := strings.TrimSpace(task.Execution.OriginalTask)
	if originalTask == "" {
		originalTask = strings.TrimSpace(task.Goal)
	}
	return session.ResumeContext{
		OriginalTask:      originalTask,
		PendingTool:       call.Name,
		PendingArgs:       redactedMap(call.Args),
		PolicyClass:       strings.TrimSpace(class),
		Reason:            strings.TrimSpace(reason),
		ActionSummary:     action,
		AfterSuccess:      confirmedToolSuccessContinueText(originalTask, call),
		AfterFailure:      "Continue the original task without asking for the same confirmation again. Use a simpler allowed command or another available tool if needed.",
		AuthorizationOnly: authorizationOnly,
	}
}

func redactedMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	redacted, ok := redactSecrets(value).(map[string]any)
	if !ok {
		return map[string]any{"value": redactSecrets(value)}
	}
	return redacted
}

func compactApprovalValue(value any) string {
	text := redactSecretString(fmt.Sprint(redactSecrets(value)))
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 160 {
		return text[:157] + "..."
	}
	return text
}

func (rt Runtime) handlePending(ctx context.Context, state *session.State, msg channel.InboundMessage, trace *traceRecorder) (Response, bool, error) {
	if state.Pending == nil {
		return Response{}, false, nil
	}
	text := strings.TrimSpace(msg.Text)
	switch state.Pending.Kind {
	case "confirm_tool":
		control, hasControl := rt.pendingControl(msg, text)
		if hasControl {
			_ = trace.write(map[string]any{"type": "pending_control_normalized", "task_id": state.Pending.TaskID, "pending_kind": state.Pending.Kind, "text": text, "command": control})
		}
		if control == "cancel" || (!hasControl && rt.isCancel(msg, text)) {
			_ = trace.write(map[string]any{"type": "approval_denied", "task_id": state.Pending.TaskID, "tool": state.Pending.ToolCall.Name})
			state.AddExecutionEvent(state.Pending.TaskID, session.ExecutionEvent{Type: "confirmation_cancelled", Status: "cancelled", Tool: state.Pending.ToolCall.Name, Summary: "user cancelled pending confirmation"})
			state.SetExecutionStatus(state.Pending.TaskID, "cancelled")
			state.Pending = nil
			state.BlockActiveTask("cancelled")
			if err := rt.saveState(state, trace); err != nil {
				return Response{}, true, err
			}
			return rt.reply(msg, runtimeText(rt.Config, msg, "runtime.cancelled", nil), "cancelled"), true, nil
		}
		if !(control == "approve" || control == "continue" || (!hasControl && rt.isConfirm(msg, text))) {
			return rt.reply(msg, runtimeText(rt.Config, msg, "approval.confirm.generic", nil), "approval_pending"), true, nil
		}
		pending := *state.Pending
		pendingTaskID := pending.TaskID
		call := pending.ToolCall
		resume := pending.ResumeContext
		state.Pending = nil
		taskID := strings.TrimSpace(pendingTaskID)
		if taskID == "" {
			taskID = state.ActiveTask
		}
		if strings.TrimSpace(resume.OriginalTask) == "" {
			if task := state.TaskByID(taskID); task != nil {
				resume = task.Execution.ResumeContext
			}
		}
		state.SetExecutionStatus(taskID, "resuming")
		state.AddExecutionEvent(taskID, session.ExecutionEvent{Type: "confirmation_approved", Status: "resuming", Tool: call.Name, Summary: resume.ActionSummary})
		_ = trace.write(map[string]any{"type": "pending_confirmed", "task_id": taskID, "frame_id": pending.FrameID, "tool_call": call, "resume_context": resume})
		approvalKey, approvalClass := taskApprovalKey(call, nil, rt.Config)
		if taskApprovalCanReuse(call, rt.Config) {
			state.AddSessionApproval(session.TaskApproval{Key: approvalKey, Tool: call.Name, Class: approvalClass})
			_ = trace.write(map[string]any{"type": "approval_granted", "task_id": taskID, "tool": call.Name, "approval_key": approvalKey, "class": approvalClass})
		}
		_ = trace.write(map[string]any{"type": "pending_control_executed", "task_id": taskID, "pending_kind": "confirm_tool", "command": firstNonEmpty(control, "approve")})
		if call.Name == "script.run" {
			if record, err := script.Authorize(rt.Config, strings.TrimSpace(fmt.Sprint(call.Args["name"]))); err == nil {
				_ = trace.write(map[string]any{"type": "script_authorized", "script": record.Name, "path": record.Path, "source": record.Source, "hash": record.Hash})
			}
		}
		if pending.AuthorizationOnly {
			task := state.ActivateTask(taskID)
			if task == nil {
				task = state.EnsureTask("authorized script")
			}
			userText := firstNonEmpty(resume.AfterSuccess, mergeTaskAndInstruction(task.Goal, firstNonEmpty(pending.ResumeText, text)))
			_ = trace.write(map[string]any{"type": "pending_authorization_only_continue", "task_id": task.ID, "tool_call": call})
			resp, err := rt.runTask(ctx, msg, state, task, userText, trace)
			return resp, true, err
		}
		result := rt.Tools.Execute(ctx, call)
		toolDef, _ := rt.Tools.Get(call.Name)
		observe := rt.Hooks.observe(ctx, ObserveHookInput{
			Kind:       "tool_result",
			State:      *state,
			TaskID:     taskID,
			ToolCall:   call,
			Tool:       toolDef,
			ToolResult: result,
		}, trace)
		status := ""
		evidence := map[string]any{}
		if observe.TaskStep != nil {
			state.AddStep(taskID, *observe.TaskStep)
			status = observe.TaskStep.Status
			evidence = observe.TaskStep.Evidence
			state.AddExecutionEvent(taskID, session.ExecutionEvent{
				Type:     "confirmed_tool_result",
				Status:   status,
				Tool:     call.Name,
				StepID:   observe.TaskStep.ID,
				Summary:  observe.TaskStep.Summary,
				Evidence: redactedMap(evidence),
			})
		}
		_ = trace.write(map[string]any{"type": "tool_execution_end", "tool_call": call, "tool_result": redactToolResult(result), "acceptance": status, "evidence": redactSecrets(evidence)})
		state.Messages = append(state.Messages, agentcore.Message{Role: agentcore.RoleTool, ToolCallID: call.ID, Content: result.Content})
		if proposalID := proposalIDFromEvidence(evidence); proposalID != "" {
			question := renderAgentProfileProposalReview(rt.Config, msg, proposalID)
			state.Pending = &session.PendingAction{
				Kind:       "agent_profile_proposal_review",
				TaskID:     taskID,
				ProposalID: proposalID,
				Question:   question,
			}
			if err := rt.saveState(state, trace); err != nil {
				return Response{}, true, err
			}
			return rt.reply(msg, result.Content, "completed"), true, nil
		}
		if result.IsError {
			task := state.ActivateTask(taskID)
			if task == nil {
				task = state.EnsureTask("confirmed tool result")
			}
			state.SetExecutionStatus(task.ID, "resuming")
			continueText := pendingToolFailureContinueText(task.Goal, call, result, resume)
			_ = trace.write(map[string]any{
				"type":          "pending_tool_failed_continue",
				"task_id":       task.ID,
				"tool_call":     call,
				"reason":        result.Content,
				"continue_text": continueText,
			})
			resp, err := rt.runTask(ctx, msg, state, task, continueText, trace)
			return resp, true, err
		}
		task := state.ActivateTask(taskID)
		if task == nil {
			task = state.EnsureTask("confirmed tool result")
		}
		state.SetExecutionStatus(task.ID, "resuming")
		if call.Name == "schedule.create" && scheduleCreateRequiresTest(call) {
			scheduleID := scheduleIDFromToolResult(result)
			if scheduleID != "" {
				question := renderScheduleReview(rt.Config, msg, rt.home(), scheduleID)
				state.Pending = &session.PendingAction{
					Kind:       "schedule_review",
					TaskID:     task.ID,
					ScheduleID: scheduleID,
					Question:   question,
				}
				state.BlockActiveTask("await_schedule_test")
				state.AddExecutionEvent(task.ID, session.ExecutionEvent{Type: "await_schedule_review", Status: "awaiting_user_input", Tool: call.Name, Summary: "schedule review pending", Evidence: map[string]any{"schedule_id": scheduleID}})
				if err := rt.saveState(state, trace); err != nil {
					return Response{}, true, err
				}
				_ = trace.write(map[string]any{"type": "schedule_review_pending", "schedule_id": scheduleID, "source": "pending_confirmed_tool"})
				return rt.reply(msg, state.Pending.Question, "schedule_review_pending"), true, nil
			}
		}
		replyText := rt.summarizeConfirmedToolResult(ctx, msg, *task, call, result, trace)
		state.CompleteActiveTaskWithSummary(summarize(replyText), trace.id, trace.path)
		state.AddExecutionEvent(task.ID, session.ExecutionEvent{Type: "completed_after_confirmed_tool", Status: "completed", Tool: call.Name, Summary: summarize(replyText)})
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, true, err
		}
		return rt.reply(msg, replyText, "completed"), true, nil
	case "memory_proposal_review":
		control, hasControl := rt.pendingControl(msg, text)
		if hasControl {
			_ = trace.write(map[string]any{"type": "pending_control_normalized", "task_id": state.Pending.TaskID, "pending_kind": state.Pending.Kind, "text": text, "command": control})
		}
		action, ok := rt.parseMemoryProposalReviewAction(msg, text)
		if !ok && (control == "approve" || control == "continue") {
			action, ok = "commit", true
		}
		if !ok && (control == "ignore" || control == "cancel") {
			action, ok = "reject", true
		}
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
		taskID := state.Pending.TaskID
		proposalID := state.Pending.ProposalID
		state.Pending = nil
		_ = trace.write(map[string]any{"type": "pending_control_executed", "task_id": taskID, "pending_kind": "memory_proposal_review", "command": firstNonEmpty(control, action)})
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
		control, hasControl := rt.pendingControl(msg, text)
		if hasControl {
			_ = trace.write(map[string]any{"type": "pending_control_normalized", "task_id": state.Pending.TaskID, "pending_kind": state.Pending.Kind, "text": text, "command": control})
		}
		action, ok := rt.parseAgentProfileProposalReviewAction(msg, text)
		if !ok && (control == "approve" || control == "continue") {
			action, ok = "promote", true
		}
		if !ok && (control == "ignore" || control == "cancel") {
			action, ok = "reject", true
		}
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
		taskID := state.Pending.TaskID
		proposalID := state.Pending.ProposalID
		state.Pending = nil
		_ = trace.write(map[string]any{"type": "pending_control_executed", "task_id": taskID, "pending_kind": "agent_profile_proposal_review", "command": firstNonEmpty(control, action)})
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
		control, hasControl := rt.pendingControl(msg, text)
		if hasControl {
			_ = trace.write(map[string]any{"type": "pending_control_normalized", "task_id": state.Pending.TaskID, "pending_kind": state.Pending.Kind, "text": text, "command": control})
		}
		action, ok := rt.parseScheduleReviewAction(msg, text)
		if !ok && (control == "run" || control == "approve" || control == "continue") {
			action, ok = "test", true
		}
		if !ok && control == "cancel" {
			action, ok = "cancel", true
		}
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
		_ = trace.write(map[string]any{"type": "pending_control_executed", "task_id": taskID, "pending_kind": "schedule_review", "command": firstNonEmpty(control, action)})
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
		state.CompleteActiveTaskWithSummary(runtimeText(rt.Config, msg, "schedule.test.summary", textValues("task_id", task.ID)), trace.id, trace.path)
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, true, err
		}
		_ = trace.write(map[string]any{"type": "schedule_review_tested", "schedule_id": task.ID, "run_id": record.ID, "status": record.Status})
		return rt.reply(msg, runtimeText(rt.Config, msg, "schedule.test.done", textValues("task_id", task.ID, "run_at", task.RunAt)), "completed"), true, nil
	case "archive_task_recall":
		pending := *state.Pending
		if rt.isCancel(msg, text) {
			state.Pending = nil
			if err := rt.saveState(state, trace); err != nil {
				return Response{}, true, err
			}
			return rt.reply(msg, runtimeText(rt.Config, msg, "runtime.archive_recall.cancelled", nil), "cancelled"), true, nil
		}
		if !rt.isConfirm(msg, text) {
			return rt.reply(msg, pending.Question, "archive_recall_pending"), true, nil
		}
		archived, _, err := rt.Store.LoadArchive(state.Key, pending.ArchiveID)
		if err != nil {
			state.Pending = nil
			if saveErr := rt.saveState(state, trace); saveErr != nil {
				return Response{}, true, saveErr
			}
			return rt.reply(msg, runtimeText(rt.Config, msg, "runtime.archive_recall.load_error", textValues("error", err.Error())), "error"), true, nil
		}
		oldTask := taskByID(archived, pending.TaskID)
		if oldTask == nil {
			state.Pending = nil
			if err := rt.saveState(state, trace); err != nil {
				return Response{}, true, err
			}
			return rt.reply(msg, runtimeText(rt.Config, msg, "runtime.archive_recall.missing", nil), "error"), true, nil
		}
		state.Pending = nil
		userText := archivedTaskRecallText(*oldTask, pending.ArchiveID, text)
		goal := strings.TrimSpace(pending.ResumeText)
		if goal == "" {
			goal = text
		}
		task := state.StartTask(goal)
		applyCompletionContract(task, goal)
		_ = trace.write(map[string]any{"type": "archive_task_recall_confirmed", "archive_id": pending.ArchiveID, "old_task_id": pending.TaskID, "new_task_id": task.ID})
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, true, err
		}
		resp, err := rt.runTask(ctx, msg, state, task, userText, trace)
		return resp, true, err
	case "user_input":
		pending := *state.Pending
		if control, ok := rt.pendingControl(msg, text); ok {
			_ = trace.write(map[string]any{"type": "pending_control_normalized", "task_id": pending.TaskID, "pending_kind": pending.Kind, "text": text, "command": control})
			taskID := pending.TaskID
			switch control {
			case "cancel":
				state.Pending = nil
				blockTask(state, taskID, "cancelled")
				if err := rt.saveState(state, trace); err != nil {
					return Response{}, true, err
				}
				_ = trace.write(map[string]any{"type": "pending_control_executed", "task_id": taskID, "pending_kind": "user_input", "command": control})
				return rt.reply(msg, runtimeText(rt.Config, msg, "runtime.cancelled", nil), "cancelled"), true, nil
			case "approve", "continue", "run":
				state.Pending = nil
				state.Messages = append(state.Messages, agentcore.Message{Role: agentcore.RoleUser, Content: text})
				if task := state.ActivateTask(taskID); task != nil {
					userText := mergeTaskAndInstruction(task.Goal, text)
					if strings.TrimSpace(task.CompletionContract.SuccessCondition) == "" {
						applyCompletionContract(task, task.Goal)
					}
					_ = trace.write(map[string]any{"type": "pending_control_executed", "task_id": taskID, "pending_kind": "user_input", "command": control})
					resp, err := rt.runTask(ctx, msg, state, task, userText, trace)
					return resp, true, err
				}
			}
		}
		_ = trace.write(map[string]any{"type": "pending_control_fallback_to_llm", "task_id": pending.TaskID, "pending_kind": pending.Kind, "text": text})
		intentModel := rt.Pool.RoleModelForMessage(msg, "router", nil)
		if intentModel == nil {
			agent := rt.Pool.AgentForMessage(msg)
			var fallback agentcore.Model
			if agent != nil {
				fallback = agent.Model
			}
			intentModel = rt.Pool.RoleModelForMessage(msg, "followup", fallback)
		}
		catalogDir := ""
		if rt.Config != nil {
			catalogDir = rt.Config.App.MessageCatalogDir
		}
		intent := rt.Hooks.pendingIntent(ctx, PendingIntentInput{State: *state, Pending: pending, Text: text, Model: intentModel, Locale: runtimeLocale(rt.Config, msg), CatalogDir: catalogDir}, trace)
		switch intent.Kind {
		case "new_task":
			taskID := pending.TaskID
			state.Pending = nil
			blockTask(state, taskID, "interrupted")
			if state.ActiveTask == taskID {
				state.ActiveTask = ""
			}
			_ = trace.write(map[string]any{"type": "pending_user_input_bypassed", "task_id": taskID, "text": text, "reason": intent.Reason})
			if err := rt.saveState(state, trace); err != nil {
				return Response{}, true, err
			}
			return Response{}, false, nil
		}
		taskID := pending.TaskID
		state.Pending = nil
		state.Messages = append(state.Messages,
			agentcore.Message{Role: agentcore.RoleUser, Content: text},
		)
		if task := state.ActivateTask(taskID); task != nil && intent.Kind == "action_ack" {
			userText := mergeTaskAndInstruction(task.Goal, text)
			_ = trace.write(map[string]any{"type": "pending_user_input_bound", "task_id": taskID, "text": text, "reason": intent.Reason})
			if strings.TrimSpace(task.CompletionContract.SuccessCondition) == "" {
				applyCompletionContract(task, task.Goal)
			}
			resp, err := rt.runTask(ctx, msg, state, task, userText, trace)
			return resp, true, err
		}
		_ = trace.write(map[string]any{"type": "pending_user_input", "task_id": taskID, "text": text, "reason": intent.Reason})
		if err := rt.saveState(state, trace); err != nil {
			return Response{}, true, err
		}
		return Response{}, false, nil
	default:
		state.Pending = nil
		return Response{}, false, nil
	}
}

func (rt Runtime) summarizeConfirmedToolResult(ctx context.Context, msg channel.InboundMessage, task session.TaskNode, call agentcore.ToolCall, result agentcore.ToolResult, trace *traceRecorder) string {
	agent := rt.Pool.AgentForMessage(msg)
	if agent == nil {
		agent = agentcore.NewAgent(rt.Model, rt.Tools)
	}
	if agent.Model == nil {
		return result.Content
	}
	modelCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	summary, err := agent.Model.Next(modelCtx, agentcore.Context{
		SystemPrompt: "Summarize a confirmed tool execution for the original task. Do not call tools. Return a concise user-facing task-level update.",
		Messages: []agentcore.Message{{
			Role: agentcore.RoleUser,
			Content: "Original task:\n" + task.Goal +
				"\n\nConfirmed tool:\n" + call.Name +
				"\n\nTool output:\n" + result.Content +
				"\n\nExplain what was completed and what remains, if anything.",
		}},
		Tools: nil,
	})
	if err != nil {
		_ = trace.write(map[string]any{"type": "pending_confirm_summary_error", "error": err.Error()})
		return result.Content
	}
	text := strings.TrimSpace(summary.Content)
	if text == "" {
		return result.Content
	}
	return text
}

func confirmedToolSuccessContinueText(goal string, call agentcore.ToolCall) string {
	var b strings.Builder
	b.WriteString("The previously pending confirmation was approved and the tool call completed. Continue the original task to completion using this completed action as evidence.")
	b.WriteString("\n\nCompleted tool: ")
	b.WriteString(call.Name)
	if command := strings.TrimSpace(fmt.Sprint(call.Args["command"])); command != "" && command != "<nil>" {
		b.WriteString("\nCommand: ")
		b.WriteString(command)
	} else if len(call.Args) > 0 {
		b.WriteString("\nArgs: ")
		b.WriteString(compactApprovalValue(call.Args))
	}
	return mergeTaskAndInstruction(goal, b.String())
}

func pendingToolFailureContinueText(goal string, call agentcore.ToolCall, result agentcore.ToolResult, resume session.ResumeContext) string {
	if text := strings.TrimSpace(resume.AfterFailure); text != "" {
		var b strings.Builder
		b.WriteString(text)
		b.WriteString("\n\nFailed tool: ")
		b.WriteString(call.Name)
		if command := strings.TrimSpace(fmt.Sprint(call.Args["command"])); command != "" && command != "<nil>" {
			b.WriteString("\nCommand: ")
			b.WriteString(command)
		} else if len(call.Args) > 0 {
			b.WriteString("\nArgs: ")
			b.WriteString(compactApprovalValue(call.Args))
		}
		if reason := strings.TrimSpace(result.Content); reason != "" {
			b.WriteString("\nFailure: ")
			b.WriteString(reason)
		}
		return mergeTaskAndInstruction(firstNonEmpty(resume.OriginalTask, goal), b.String())
	}
	var b strings.Builder
	b.WriteString("The previously confirmed tool call failed. Continue the original task without asking for the same confirmation again. Use a simpler allowed command or another available tool if needed.")
	b.WriteString("\n\nFailed tool: ")
	b.WriteString(call.Name)
	if command := strings.TrimSpace(fmt.Sprint(call.Args["command"])); command != "" && command != "<nil>" {
		b.WriteString("\nCommand: ")
		b.WriteString(command)
	} else if len(call.Args) > 0 {
		b.WriteString("\nArgs: ")
		b.WriteString(compactApprovalValue(call.Args))
	}
	if reason := strings.TrimSpace(result.Content); reason != "" {
		b.WriteString("\nFailure: ")
		b.WriteString(reason)
	}
	return mergeTaskAndInstruction(goal, b.String())
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
	hasVerb := false
	for _, verb := range runtimeCueList(nil, "router.standalone_task.verbs") {
		if strings.Contains(text, verb) {
			hasVerb = true
			break
		}
	}
	if !hasVerb {
		return false
	}
	for _, marker := range runtimeCueList(nil, "router.standalone_task.markers") {
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

func (rt Runtime) pendingControl(msg channel.InboundMessage, text string) (string, bool) {
	action, ok := runtimeAlias(rt.Config, msg, text, "approve", "confirm", "continue", "run", "ignore", "cancel")
	if !ok {
		return "", false
	}
	switch action {
	case "confirm":
		return "approve", true
	case "approve", "continue", "run", "ignore", "cancel":
		return action, true
	default:
		return "", false
	}
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

func finalTextWarning(text string) string {
	lower := strings.ToLower(strings.TrimSpace(text))
	switch {
	case containsAny(lower, runtimeCueList(nil, "router.warning.malformed_cues")):
		return "tool_call_format_issue"
	case looksLikeNonSubstantiveActionAck(text):
		return "non_substantive_action_ack"
	case looksLikeUnexecutedNextStep(text):
		return "unexecuted_next_step"
	default:
		return ""
	}
}

func looksLikeUnexecutedNextStep(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return true
	}
	lower := strings.ToLower(trimmed)
	if strings.HasSuffix(trimmed, ":") || strings.HasSuffix(trimmed, "：") {
		return true
	}
	for _, phrase := range runtimeCueList(nil, "router.unexecuted.pending_phrases") {
		if strings.Contains(lower, phrase) || strings.Contains(trimmed, phrase) {
			return true
		}
	}
	hasAction := false
	for _, cue := range runtimeCueList(nil, "router.unexecuted.action_cues") {
		if strings.Contains(lower, cue) || strings.Contains(trimmed, cue) {
			hasAction = true
			break
		}
	}
	if !hasAction {
		return false
	}
	for _, cue := range runtimeCueList(nil, "router.unexecuted.work_cues") {
		if strings.Contains(lower, cue) || strings.Contains(trimmed, cue) {
			return true
		}
	}
	return false
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
	return agentcore.Message{Role: agentcore.RoleAssistant, Content: i18n.New(i18n.Config{}).T(i18n.LocaleZH, "runtime.heuristic.echo", textValues("text", text))}, nil
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
