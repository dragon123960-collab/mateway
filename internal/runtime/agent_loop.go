package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skill"
)

type AgentLoop struct {
	runtime Runtime
	state   loopState
}

type loopState struct {
	message         channel.InboundMessage
	traceID         string
	plan            model.Plan
	results         []model.ToolResult
	control         string
	replyText       string
	awaitConfirm    bool
	awaitUserInput  bool
	failed          bool
	synthesisFailed bool
	startedAt       time.Time
	session         session.State
	resolvedQuery   string
	topic           string
	selectedSkills  []string
	binding         taskBindingDecision
	currentTask     *session.TaskState
}

func NewAgentLoop(rt Runtime, msg channel.InboundMessage) AgentLoop {
	if msg.SessionKey == "" {
		msg.SessionKey = fallbackSessionKey(msg)
	}
	return AgentLoop{
		runtime: rt,
		state: loopState{
			message:   msg,
			traceID:   traceIDForMessage(msg),
			startedAt: time.Now(),
		},
	}
}

func (l *AgentLoop) Run(ctx context.Context) (Response, error) {
	l.loadSession()
	if resp := l.resolveArtifactDirectAnswer(); resp != nil {
		return *resp, nil
	}
	binding := l.resolveTaskBinding(ctx)
	if resp := l.applyTaskBinding(binding); resp != nil {
		return *resp, nil
	}
	l.receive()
	if err := l.plan(ctx); err != nil {
		return l.fail(fmt.Errorf("plan failed: %w", err)), nil
	}
	l.act(ctx, l.state.plan)
	if l.state.control != "" {
		return l.controlReply(), nil
	}
	if l.shouldRepair() {
		l.repair(ctx)
		if l.state.control != "" {
			return l.controlReply(), nil
		}
	}
	l.synthesize(ctx)
	return l.finalReply(), nil
}

func (l *AgentLoop) receive() {
	msg := l.state.message
	l.runtime.Logger.Event("runtime.receive", map[string]any{
		"trace_id":       l.state.traceID,
		"session_key":    msg.SessionKey,
		"channel":        msg.Channel,
		"message_id":     msg.ID,
		"thread_id":      msg.ThreadID,
		"user_id":        msg.UserID,
		"text":           msg.Text,
		"resolved_query": firstNonEmpty(l.state.resolvedQuery, msg.Text),
	})
}

func (l *AgentLoop) loadSession() {
	if l.runtime.Sessions == nil {
		return
	}
	state, err := l.runtime.Sessions.Load(l.state.message.SessionKey)
	if err != nil {
		l.runtime.Logger.Event("runtime.session_load_failed", map[string]any{
			"trace_id":    l.state.traceID,
			"session_key": l.state.message.SessionKey,
			"error":       err.Error(),
		})
		return
	}
	l.state.session = state
	lastTaskID := ""
	lastStatus := ""
	if state.LastTask != nil {
		lastTaskID = state.LastTask.ID
		lastStatus = state.LastTask.Status
	}
	l.runtime.Logger.Event("runtime.session_loaded", map[string]any{
		"trace_id":     l.state.traceID,
		"session_key":  l.state.message.SessionKey,
		"exists":       !state.CreatedAt.IsZero() || state.TurnCount > 0 || state.LastTask != nil || len(state.RecentTurns) > 0,
		"turn_count":   state.TurnCount,
		"last_task_id": lastTaskID,
		"last_status":  lastStatus,
	})
}

func (l *AgentLoop) plan(ctx context.Context) error {
	planMatches := skill.SelectMatches(l.skillDefinitions(), skill.StagePlanning, l.skillContext())
	planSkills := make([]skill.Definition, 0, len(planMatches))
	for _, match := range planMatches {
		planSkills = append(planSkills, match.Definition)
	}
	l.runtime.Logger.Event("runtime.skills_selected", map[string]any{
		"trace_id": l.state.traceID,
		"stage":    skill.StagePlanning,
		"skills":   selectedSkillsTraceFields(planMatches),
	})
	l.recordSelectedSkills(planSkills)
	contextPrompt := buildModelContextPrompt(l.state.resolvedRequest(), skill.StagePlanning, planMatches, l.runtime.Tools.Definitions(), l.runtime.ToolCtx)
	plan, err := l.runtime.Model.PlanJSON(ctx, l.state.resolvedRequest(), l.runtime.Tools.Definitions(), strings.TrimSpace(contextPrompt+"\n\n"+skill.PromptBlock(planSkills)))
	if err != nil {
		return err
	}
	l.state.plan = plan
	l.runtime.Logger.Event("runtime.plan", map[string]any{
		"trace_id":   l.state.traceID,
		"summary":    plan.Summary,
		"steps":      len(plan.Steps),
		"tool_names": planToolNames(plan),
	})
	if l.runtime.Observer != nil {
		l.runtime.Observer.Plan(l.state.traceID, plan)
	}
	return nil
}

func (l *AgentLoop) act(ctx context.Context, plan model.Plan) {
	results, control := l.runtime.executePlan(ctx, l.state.traceID, plan, l.state.binding.ApprovalGranted)
	l.state.plan = plan
	l.state.results = results
	l.state.control = control
}

func (l *AgentLoop) shouldRepair() bool {
	return l.state.control == "" && hasRepairableFailure(l.state.results)
}

func (l *AgentLoop) repair(ctx context.Context) {
	planMatches := skill.SelectMatches(l.skillDefinitions(), skill.StagePlanning, l.skillContext())
	planSkills := make([]skill.Definition, 0, len(planMatches))
	for _, match := range planMatches {
		planSkills = append(planSkills, match.Definition)
	}
	l.runtime.Logger.Event("runtime.skills_selected", map[string]any{
		"trace_id": l.state.traceID,
		"stage":    "planning_repair",
		"skills":   selectedSkillsTraceFields(planMatches),
	})
	l.recordSelectedSkills(planSkills)
	contextPrompt := buildModelContextPrompt(l.state.resolvedRequest(), "planning_repair", planMatches, l.runtime.Tools.Definitions(), l.runtime.ToolCtx)
	repaired, err := l.runtime.Model.RepairPlanJSON(ctx, l.state.resolvedRequest(), l.state.plan, l.state.results, l.runtime.Tools.Definitions(), strings.TrimSpace(contextPrompt+"\n\n"+skill.PromptBlock(planSkills)))
	if err != nil {
		l.runtime.Logger.Event("runtime.plan_repair_failed", map[string]any{
			"trace_id": l.state.traceID,
			"error":    err.Error(),
		})
		return
	}
	l.runtime.Logger.Event("runtime.plan_repair", map[string]any{
		"trace_id":   l.state.traceID,
		"summary":    repaired.Summary,
		"steps":      len(repaired.Steps),
		"tool_names": planToolNames(repaired),
	})
	if l.runtime.Observer != nil {
		l.runtime.Observer.Plan(l.state.traceID, repaired)
	}
	l.act(ctx, repaired)
}

func (l *AgentLoop) synthesize(ctx context.Context) {
	synthMatches := skill.SelectMatches(l.skillDefinitions(), skill.StageSynthesis, l.skillContext())
	synthSkills := make([]skill.Definition, 0, len(synthMatches))
	for _, match := range synthMatches {
		synthSkills = append(synthSkills, match.Definition)
	}
	l.runtime.Logger.Event("runtime.skills_selected", map[string]any{
		"trace_id": l.state.traceID,
		"stage":    skill.StageSynthesis,
		"skills":   selectedSkillsTraceFields(synthMatches),
	})
	l.recordSelectedSkills(synthSkills)
	contextPrompt := buildModelContextPrompt(l.state.resolvedRequest(), skill.StageSynthesis, synthMatches, l.runtime.Tools.Definitions(), l.runtime.ToolCtx)
	text, err := l.runtime.Model.Synthesize(ctx, l.state.resolvedRequest(), l.state.plan, l.state.results, strings.TrimSpace(contextPrompt+"\n\n"+skill.PromptBlock(synthSkills)))
	if err != nil {
		l.state.synthesisFailed = true
		l.state.replyText = fallbackSynthesis(l.state.results)
		l.runtime.Logger.Event("runtime.synthesize_failed", map[string]any{
			"trace_id": l.state.traceID,
			"error":    err.Error(),
		})
		return
	}
	l.state.replyText = text
}

func (l *AgentLoop) skillContext() skill.Context {
	results := make([]skill.ResultRef, 0, len(l.state.results))
	for _, result := range l.state.results {
		kind, _ := result.Evidence["kind"].(string)
		results = append(results, skill.ResultRef{Kind: kind})
	}
	return skill.Context{
		UserText: l.state.resolvedRequest(),
		Results:  results,
	}
}

func (l *AgentLoop) skillDefinitions() []skill.Definition {
	if l.runtime.Skills == nil {
		return nil
	}
	return l.runtime.Skills.Definitions()
}

func (l *AgentLoop) controlReply() Response {
	style := "approval_pending"
	awaitInput := false
	for _, result := range l.state.results {
		if kind, _ := result.Evidence["kind"].(string); kind == "user_input_required" {
			style = "input_required"
			awaitInput = true
			break
		}
	}
	text := controlReplyText(l.state.results, style)
	l.state.replyText = text
	l.state.awaitConfirm = l.state.control == "await_confirm" && !awaitInput
	l.state.awaitUserInput = awaitInput
	resp := Response{
		Reply: l.runtime.sanitizeReply(channel.OutboundMessage{
			Channel:  l.state.message.Channel,
			ThreadID: l.state.message.ThreadID,
			Text:     text,
			Style:    style,
			Title:    "Mateway 待确认",
		}),
		TraceID:        l.state.traceID,
		Plan:           l.state.plan,
		Results:        l.state.results,
		AwaitConfirm:   l.state.awaitConfirm,
		AwaitUserInput: l.state.awaitUserInput,
	}
	l.runtime.Logger.Event("runtime.control", map[string]any{
		"trace_id": l.state.traceID,
		"control":  l.state.control,
		"style":    resp.Reply.Style,
	})
	if l.runtime.Observer != nil {
		l.runtime.Observer.Control(l.state.traceID, l.state.control, resp.Reply.Style)
	}
	l.saveSession(resp)
	return resp
}

func (l *AgentLoop) finalReply() Response {
	l.state.failed = anyFailed(l.state.results)
	resp := Response{
		Reply: l.runtime.sanitizeReply(channel.OutboundMessage{
			Channel:  l.state.message.Channel,
			ThreadID: l.state.message.ThreadID,
			Text:     l.state.replyText,
			Style:    styleForFailed(l.state.failed),
		}),
		TraceID: l.state.traceID,
		Plan:    l.state.plan,
		Results: l.state.results,
		Failed:  l.state.failed,
	}
	l.runtime.Logger.Event("runtime.reply", map[string]any{
		"trace_id":     l.state.traceID,
		"failed":       l.state.failed,
		"reply_chars":  len(l.state.replyText),
		"result_count": len(l.state.results),
	})
	if l.runtime.Observer != nil {
		l.runtime.Observer.Reply(l.state.traceID, l.state.replyText, l.state.failed)
	}
	l.saveSession(resp)
	return resp
}

func (l *AgentLoop) fail(err error) Response {
	resp := l.runtime.failure(l.state.message, nil, nil, err)
	l.runtime.Logger.Event("runtime.failed", map[string]any{
		"trace_id": l.state.traceID,
		"reason":   resp.Reply.Text,
	})
	if l.runtime.Observer != nil {
		l.runtime.Observer.Failed(l.state.traceID, resp.Reply.Text)
	}
	l.saveSession(resp)
	return resp
}

func (l *AgentLoop) saveSession(resp Response) {
	if l.runtime.Sessions == nil {
		return
	}
	finishedAt := time.Now()
	task := l.baseTaskForSave()
	task.TraceID = l.state.traceID
	task.UserText = l.state.message.Text
	task.ResolvedQuery = l.state.resolvedRequest()
	task.Topic = firstNonEmpty(l.state.topic, task.Topic, l.state.plan.Summary)
	task.PlanSummary = l.state.plan.Summary
	task.ToolNames = planToolNames(l.state.plan)
	task.SelectedSkills = append([]string(nil), l.state.selectedSkills...)
	if task.Status != session.TaskAbandoned {
		task.Status = taskStatusForResponse(resp)
	}
	task.Failed = resp.Failed
	task.ResultCount = len(resp.Results)
	task.ReplyPreview = shortenReply(resp.Reply.Text, 240)
	task.LastAnswer = strings.TrimSpace(resp.Reply.Text)
	task.Artifacts = collectArtifacts(resp.Results)
	task.UpdatedAt = finishedAt
	task.FinishedAt = finishedAt
	if task.StartedAt.IsZero() {
		task.StartedAt = l.state.startedAt
	}
	if resp.AwaitConfirm {
		task.PendingApproval = &session.PendingApproval{
			ApprovalType:    "boolean_confirm",
			Prompt:          strings.TrimSpace(resp.Reply.Text),
			RequestedAction: firstNonEmpty(task.PlanSummary, task.ResolvedQuery),
		}
		task.PendingQuestions = nil
		l.runtime.Logger.Event("runtime.task_pending_approval", map[string]any{
			"trace_id": l.state.traceID,
			"task_id":  task.ID,
		})
	} else if resp.AwaitUserInput {
		task.PendingQuestions = []string{strings.TrimSpace(resp.Reply.Text)}
		if len(task.PendingFields) == 0 {
			task.PendingFields = nil
		}
		task.PendingApproval = nil
		l.runtime.Logger.Event("runtime.task_pending_input", map[string]any{
			"trace_id": l.state.traceID,
			"task_id":  task.ID,
			"fields":   pendingFieldNames(task.PendingFields),
		})
	} else {
		task.PendingApproval = nil
		task.PendingQuestions = nil
		task.PendingFields = nil
	}
	next := session.ApplyTask(l.state.session, session.StateMeta{
		SessionKey: l.state.message.SessionKey,
		Channel:    l.state.message.Channel,
		UserID:     l.state.message.UserID,
		ThreadID:   l.state.message.ThreadID,
	}, session.AppendTaskInput{
		Task:           task,
		AssistantReply: resp.Reply.Text,
		At:             finishedAt,
		Activate:       true,
	})
	if err := l.runtime.Sessions.Save(next); err != nil {
		l.runtime.Logger.Event("runtime.session_save_failed", map[string]any{
			"trace_id":    l.state.traceID,
			"session_key": l.state.message.SessionKey,
			"error":       err.Error(),
		})
		return
	}
	l.state.session = next
	l.state.currentTask = session.ActiveTask(next)
	l.runtime.Logger.Event("runtime.session_saved", map[string]any{
		"trace_id":     l.state.traceID,
		"session_key":  next.SessionKey,
		"turn_count":   next.TurnCount,
		"task_id":      task.ID,
		"task_status":  task.Status,
		"result_count": task.ResultCount,
	})
}

func (l *AgentLoop) saveConversationOnly(resp Response) {
	if l.runtime.Sessions == nil {
		return
	}
	next := session.AppendConversation(l.state.session, session.StateMeta{
		SessionKey: l.state.message.SessionKey,
		Channel:    l.state.message.Channel,
		UserID:     l.state.message.UserID,
		ThreadID:   l.state.message.ThreadID,
	}, l.state.message.Text, resp.Reply.Text, time.Now())
	if err := l.runtime.Sessions.Save(next); err != nil {
		l.runtime.Logger.Event("runtime.session_save_failed", map[string]any{
			"trace_id":    l.state.traceID,
			"session_key": l.state.message.SessionKey,
			"error":       err.Error(),
		})
		return
	}
	l.state.session = next
	l.runtime.Logger.Event("runtime.session_saved", map[string]any{
		"trace_id":    l.state.traceID,
		"session_key": next.SessionKey,
		"turn_count":  next.TurnCount,
	})
}

func (l *AgentLoop) baseTaskForSave() session.TaskState {
	if l.state.currentTask != nil {
		return *l.state.currentTask
	}
	return session.TaskState{
		ID:        firstNonEmpty(l.state.binding.TargetTaskID, l.state.traceID),
		StartedAt: l.state.startedAt,
		Status:    session.TaskOpen,
	}
}

func (s *loopState) resolvedRequest() string {
	return firstNonEmpty(s.resolvedQuery, s.message.Text)
}

func (l *AgentLoop) recordSelectedSkills(defs []skill.Definition) {
	for _, def := range defs {
		if strings.TrimSpace(def.Name) == "" {
			continue
		}
		seen := false
		for _, existing := range l.state.selectedSkills {
			if existing == def.Name {
				seen = true
				break
			}
		}
		if !seen {
			l.state.selectedSkills = append(l.state.selectedSkills, def.Name)
		}
	}
}

func taskStatusForResponse(resp Response) string {
	switch {
	case resp.AwaitUserInput:
		return "await_user_input"
	case resp.AwaitConfirm:
		return "await_confirm"
	case resp.Failed:
		return "failed"
	default:
		return "completed"
	}
}

func shortenReply(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	if limit <= 3 {
		return text[:limit]
	}
	return text[:limit-3] + "..."
}
