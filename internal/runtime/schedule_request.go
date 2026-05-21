package runtime

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/schedule"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/textmatch"
)

const scheduleIntentPrefix = "schedule:create"
const scheduleProposalConfirm = "schedule_proposal_confirm"
const scheduleMutationConfirm = "schedule_mutation_confirm"
const scheduleProposalIDPrefix = "schedule_proposal_id="
const scheduleMutationPrefix = "schedule_mutation="

var scheduleTimeRE = regexp.MustCompile(`\b([01]?[0-9]|2[0-3]):([0-5][0-9])\b`)
var scheduleHourRE = regexp.MustCompile("([01]?[0-9]|2[0-3])\\s*(?:\u70b9|\u6642|\u65f6)")
var scheduleIntervalRE = regexp.MustCompile("(?:every|interval|per|each|\u6bcf\u9694|\u6bcf)\\s*([0-9]+)\\s*(hours?|hrs?|h|\u5c0f\u65f6|\u5c0f\u6642)")
var scheduleMonthlyDayRE = regexp.MustCompile("(?:monthly|every month|each month|\u6bcf\u6708)\\s*([0-9]{1,2})\\s*(?:st|nd|rd|th|\u53f7|\u65e5)?")
var scheduleFieldRE = regexp.MustCompile(`(?:^|[;\n])\s*(title|prompt|daily_at|weekly_at|weekday|weekdays|monthly_at|monthly_day|interval|agent_id|channel|thread_id|user_id|delivery_mode|delivery_path)\s*=([^;\n]*)`)

type scheduleMutationIntent struct {
	Op      string
	Task    schedule.Task
	Update  schedule.UpdateInput
	Summary string
}

func (l *AgentLoop) handleScheduleRequest() *Response {
	if !looksLikeScheduleCreate(l.state.resolvedRequest()) && !strings.HasPrefix(l.state.resolvedRequest(), scheduleIntentPrefix) {
		return nil
	}
	input := scheduleInputFromText(l.state.resolvedRequest(), l.state.message)
	if strings.HasPrefix(l.state.resolvedRequest(), scheduleIntentPrefix) {
		input = schedule.ApplyDraftFields(input, fieldsFromResolvedQuery(l.state.resolvedRequest()))
	}
	check := schedule.CheckDraft(input)
	if !check.Ready {
		return l.scheduleNeedsInput(input, check)
	}
	store := schedule.NewStore(l.runtime.Config.App.Home)
	proposal, path, err := store.Propose(schedule.ProposalInput{CreateInput: input})
	if err != nil {
		return l.scheduleFailure(err)
	}
	input.ID = proposal.Task.ID
	text := fmt.Sprintf("Schedule proposal written: %s\n\nid=%s\nschedule=%s\n\nReply yes to enable it, or no to reject it.", path, proposal.Task.ID, schedule.Summary(proposal.Task.Schedule))
	resp := Response{
		Reply: l.runtime.sanitizeReply(channel.OutboundMessage{
			Channel:  l.state.message.Channel,
			ThreadID: l.state.message.ThreadID,
			Text:     text,
			Style:    "approval_pending",
			Title:    "Mateway schedule proposal",
		}),
		TraceID:      l.state.traceID,
		AwaitConfirm: true,
	}
	l.runtime.Logger.Event("runtime.schedule_proposal_created", map[string]any{
		"trace_id": l.state.traceID,
		"id":       proposal.Task.ID,
		"path":     path,
	})
	l.saveScheduleSession(resp, input, nil)
	return &resp
}

func (l *AgentLoop) handleScheduleProposalApproval() *Response {
	task := session.ActiveTask(l.state.session)
	if task == nil || task.PendingApproval == nil || task.PendingApproval.ApprovalType != scheduleProposalConfirm {
		return nil
	}
	approved, ok := parseApprovalDecision(normalizeFollowupText(l.state.message.Text), task.PendingApproval)
	if !ok {
		return nil
	}
	proposalID := scheduleProposalID(*task)
	if proposalID == "" {
		return l.scheduleFailure(fmt.Errorf("schedule proposal id is missing"))
	}
	store := schedule.NewStore(l.runtime.Config.App.Home)
	var text string
	status := session.TaskCompleted
	if approved {
		created, path, err := store.CommitProposal(proposalID)
		if err != nil {
			return l.scheduleFailure(err)
		}
		text = fmt.Sprintf("Schedule enabled: %s\n\nid=%s\nschedule=%s", path, created.ID, schedule.Summary(created.Schedule))
		l.runtime.Logger.Event("runtime.schedule_proposal_committed", map[string]any{
			"trace_id": l.state.traceID,
			"id":       created.ID,
			"path":     path,
		})
	} else {
		_, path, err := store.RejectProposal(proposalID, "Rejected from runtime confirmation.")
		if err != nil {
			return l.scheduleFailure(err)
		}
		text = "Schedule proposal rejected: " + path
		status = session.TaskAbandoned
		l.runtime.Logger.Event("runtime.schedule_proposal_rejected", map[string]any{
			"trace_id": l.state.traceID,
			"id":       proposalID,
			"path":     path,
		})
	}
	resp := Response{
		Reply: l.runtime.sanitizeReply(channel.OutboundMessage{
			Channel:  l.state.message.Channel,
			ThreadID: l.state.message.ThreadID,
			Text:     text,
			Style:    "reply",
			Title:    "Mateway schedule confirmation",
		}),
		TraceID: l.state.traceID,
	}
	l.saveScheduleApprovalSession(resp, *task, status)
	return &resp
}

func (l *AgentLoop) handleScheduleMutationApproval() *Response {
	task := session.ActiveTask(l.state.session)
	if task == nil || task.PendingApproval == nil || task.PendingApproval.ApprovalType != scheduleMutationConfirm {
		return nil
	}
	approved, ok := parseApprovalDecision(normalizeFollowupText(l.state.message.Text), task.PendingApproval)
	if !ok {
		return nil
	}
	op, id := scheduleMutationTarget(*task)
	if op == "" || id == "" {
		return l.scheduleFailure(fmt.Errorf("schedule mutation target is missing"))
	}
	store := schedule.NewStore(l.runtime.Config.App.Home)
	text := "Schedule change canceled."
	status := session.TaskAbandoned
	if approved {
		switch op {
		case "delete":
			path, err := store.Delete(id)
			if err != nil {
				return l.scheduleFailure(err)
			}
			text = "Schedule task deleted: " + path
		case "pause":
			updated, _, err := store.SetStatus(id, schedule.StatusPaused)
			if err != nil {
				return l.scheduleFailure(err)
			}
			text = "Schedule task paused: " + updated.ID
		case "resume":
			updated, _, err := store.SetStatus(id, schedule.StatusActive)
			if err != nil {
				return l.scheduleFailure(err)
			}
			text = "Schedule task resumed: " + updated.ID
		case "update":
			fields := fieldsFromResolvedQuery(task.ResolvedQuery)
			update := scheduleUpdateFromFields(fields)
			updated, path, err := store.Update(id, update)
			if err != nil {
				return l.scheduleFailure(err)
			}
			text = fmt.Sprintf("Schedule task updated: %s\n\nid=%s\nschedule=%s", path, updated.ID, schedule.Summary(updated.Schedule))
		default:
			return l.scheduleFailure(fmt.Errorf("unsupported schedule mutation: %s", op))
		}
		status = session.TaskCompleted
		l.runtime.Logger.Event("runtime.schedule_mutation_committed", map[string]any{
			"trace_id": l.state.traceID,
			"op":       op,
			"id":       id,
		})
	} else {
		l.runtime.Logger.Event("runtime.schedule_mutation_rejected", map[string]any{
			"trace_id": l.state.traceID,
			"op":       op,
			"id":       id,
		})
	}
	resp := Response{
		Reply: l.runtime.sanitizeReply(channel.OutboundMessage{
			Channel:  l.state.message.Channel,
			ThreadID: l.state.message.ThreadID,
			Text:     text,
			Style:    "reply",
			Title:    "Mateway schedule confirmation",
		}),
		TraceID: l.state.traceID,
	}
	l.saveScheduleApprovalSession(resp, *task, status)
	return &resp
}

func (l *AgentLoop) handleScheduleMutationRequest() *Response {
	intent, ok := l.parseScheduleMutationIntent(l.state.resolvedRequest())
	if !ok {
		return nil
	}
	detail := firstNonEmpty(intent.Summary, intent.Task.Title)
	text := fmt.Sprintf("Confirm schedule %s: %s (%s)? Reply yes to continue, or no to cancel.", intent.Op, intent.Task.ID, detail)
	resp := Response{
		Reply: l.runtime.sanitizeReply(channel.OutboundMessage{
			Channel:  l.state.message.Channel,
			ThreadID: l.state.message.ThreadID,
			Text:     text,
			Style:    "approval_pending",
			Title:    "Mateway schedule confirmation",
		}),
		TraceID:      l.state.traceID,
		AwaitConfirm: true,
	}
	l.runtime.Logger.Event("runtime.schedule_mutation_needs_confirm", map[string]any{
		"trace_id": l.state.traceID,
		"op":       intent.Op,
		"id":       intent.Task.ID,
	})
	l.saveScheduleMutationSession(resp, intent)
	return &resp
}

func (l *AgentLoop) scheduleNeedsInput(input schedule.CreateInput, check schedule.DraftCheck) *Response {
	text := check.ClarifyMessage
	if strings.TrimSpace(text) == "" {
		text = "I need more information before creating this schedule."
	}
	resp := Response{
		Reply: l.runtime.sanitizeReply(channel.OutboundMessage{
			Channel:  l.state.message.Channel,
			ThreadID: l.state.message.ThreadID,
			Text:     text,
			Style:    "input_required",
			Title:    "Mateway needs schedule details",
		}),
		TraceID:        l.state.traceID,
		AwaitUserInput: true,
	}
	l.runtime.Logger.Event("runtime.schedule_proposal_needs_input", map[string]any{
		"trace_id": l.state.traceID,
		"fields":   pendingFieldNames(check.MissingFields),
	})
	l.saveScheduleSession(resp, input, blankPendingFields(check.MissingFields))
	return &resp
}

func (l *AgentLoop) scheduleFailure(err error) *Response {
	resp := l.runtime.failure(l.state.message, nil, nil, err)
	l.runtime.Logger.Event("runtime.schedule_proposal_failed", map[string]any{
		"trace_id": l.state.traceID,
		"error":    err.Error(),
	})
	l.saveSession(resp)
	return &resp
}

func (l *AgentLoop) saveScheduleSession(resp Response, input schedule.CreateInput, pending map[string]string) {
	if l.runtime.Sessions == nil {
		return
	}
	now := l.state.startedAt
	task := session.TaskState{
		ID:            l.state.traceID,
		TraceID:       l.state.traceID,
		Status:        session.TaskCompleted,
		Topic:         "Schedule proposal",
		UserText:      l.state.message.Text,
		ResolvedQuery: scheduleResolvedQuery(input),
		PlanSummary:   "Create a user scheduled task proposal",
		ReplyPreview:  shortenReply(resp.Reply.Text, 240),
		LastAnswer:    strings.TrimSpace(resp.Reply.Text),
		StartedAt:     now,
		UpdatedAt:     now,
		FinishedAt:    now,
	}
	if resp.AwaitUserInput {
		task.Status = session.TaskAwaitUserInput
		task.PendingFields = pending
		task.PendingQuestions = []string{strings.TrimSpace(resp.Reply.Text)}
		task.FinishedAt = time.Time{}
	} else if resp.AwaitConfirm {
		task.Status = session.TaskAwaitConfirm
		task.PendingApproval = &session.PendingApproval{
			ApprovalType:    scheduleProposalConfirm,
			Prompt:          strings.TrimSpace(resp.Reply.Text),
			RequestedAction: scheduleProposalIDPrefix + input.ID,
		}
		task.PendingQuestions = nil
		task.FinishedAt = time.Time{}
	}
	next := session.ApplyTask(l.state.session, session.StateMeta{
		SessionKey: l.state.message.SessionKey,
		Channel:    l.state.message.Channel,
		UserID:     l.state.message.UserID,
		ThreadID:   l.state.message.ThreadID,
	}, session.AppendTaskInput{
		Task:           task,
		AssistantReply: resp.Reply.Text,
		At:             now,
		Activate:       resp.AwaitUserInput || resp.AwaitConfirm,
	})
	if err := l.runtime.Sessions.Save(next); err != nil {
		l.runtime.Logger.Event("runtime.session_save_failed", map[string]any{
			"trace_id":    l.state.traceID,
			"session_key": l.state.message.SessionKey,
			"error":       err.Error(),
		})
	}
}

func (l *AgentLoop) saveScheduleApprovalSession(resp Response, previous session.TaskState, status string) {
	if l.runtime.Sessions == nil {
		return
	}
	now := l.state.startedAt
	task := previous
	task.TraceID = l.state.traceID
	task.UserText = l.state.message.Text
	task.Status = status
	task.PendingApproval = nil
	task.PendingFields = nil
	task.PendingQuestions = nil
	task.ReplyPreview = shortenReply(resp.Reply.Text, 240)
	task.LastAnswer = strings.TrimSpace(resp.Reply.Text)
	task.UpdatedAt = now
	task.FinishedAt = now
	next := session.ApplyTask(l.state.session, session.StateMeta{
		SessionKey: l.state.message.SessionKey,
		Channel:    l.state.message.Channel,
		UserID:     l.state.message.UserID,
		ThreadID:   l.state.message.ThreadID,
	}, session.AppendTaskInput{
		Task:           task,
		AssistantReply: resp.Reply.Text,
		At:             now,
		Activate:       false,
	})
	if err := l.runtime.Sessions.Save(next); err != nil {
		l.runtime.Logger.Event("runtime.session_save_failed", map[string]any{
			"trace_id":    l.state.traceID,
			"session_key": l.state.message.SessionKey,
			"error":       err.Error(),
		})
	}
}

func (l *AgentLoop) saveScheduleMutationSession(resp Response, intent scheduleMutationIntent) {
	if l.runtime.Sessions == nil {
		return
	}
	now := l.state.startedAt
	task := session.TaskState{
		ID:            l.state.traceID,
		TraceID:       l.state.traceID,
		Status:        session.TaskAwaitConfirm,
		Topic:         "Schedule mutation",
		UserText:      l.state.message.Text,
		ResolvedQuery: scheduleMutationResolvedQuery(intent),
		PlanSummary:   "Modify a user scheduled task",
		ReplyPreview:  shortenReply(resp.Reply.Text, 240),
		LastAnswer:    strings.TrimSpace(resp.Reply.Text),
		PendingApproval: &session.PendingApproval{
			ApprovalType:    scheduleMutationConfirm,
			Prompt:          strings.TrimSpace(resp.Reply.Text),
			RequestedAction: scheduleMutationPrefix + intent.Op + ":" + intent.Task.ID,
		},
		StartedAt:  now,
		UpdatedAt:  now,
		FinishedAt: time.Time{},
	}
	next := session.ApplyTask(l.state.session, session.StateMeta{
		SessionKey: l.state.message.SessionKey,
		Channel:    l.state.message.Channel,
		UserID:     l.state.message.UserID,
		ThreadID:   l.state.message.ThreadID,
	}, session.AppendTaskInput{
		Task:           task,
		AssistantReply: resp.Reply.Text,
		At:             now,
		Activate:       true,
	})
	if err := l.runtime.Sessions.Save(next); err != nil {
		l.runtime.Logger.Event("runtime.session_save_failed", map[string]any{
			"trace_id":    l.state.traceID,
			"session_key": l.state.message.SessionKey,
			"error":       err.Error(),
		})
	}
}

func looksLikeScheduleCreate(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return false
	}
	if textmatch.ContainsGroup(normalized, "schedule_delete_cues") || textmatch.ContainsGroup(normalized, "schedule_pause_cues") || textmatch.ContainsGroup(normalized, "schedule_resume_cues") || textmatch.ContainsGroup(normalized, "schedule_update_cues") {
		return false
	}
	return textmatch.ContainsGroup(normalized, "schedule_create_cues") && textmatch.ContainsGroup(normalized, "schedule_time_cues")
}

func (l *AgentLoop) parseScheduleMutationIntent(text string) (scheduleMutationIntent, bool) {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" || !textmatch.ContainsGroup(normalized, "schedule_reference_cues") {
		return scheduleMutationIntent{}, false
	}
	op := ""
	switch {
	case textmatch.ContainsGroup(normalized, "schedule_delete_cues"):
		op = "delete"
	case textmatch.ContainsGroup(normalized, "schedule_pause_cues"):
		op = "pause"
	case textmatch.ContainsGroup(normalized, "schedule_resume_cues"):
		op = "resume"
	case textmatch.ContainsGroup(normalized, "schedule_update_cues"):
		op = "update"
	default:
		return scheduleMutationIntent{}, false
	}
	store := schedule.NewStore(l.runtime.Config.App.Home)
	tasks, err := store.List()
	if err != nil || len(tasks) == 0 {
		return scheduleMutationIntent{}, false
	}
	var matches []schedule.Task
	for _, task := range tasks {
		if scheduleTaskMatchesText(task, normalized) {
			matches = append(matches, task)
		}
	}
	if len(matches) != 1 {
		return scheduleMutationIntent{}, false
	}
	intent := scheduleMutationIntent{Op: op, Task: matches[0]}
	if op == "update" {
		update, summary, ok := scheduleUpdateFromText(text)
		if !ok {
			return scheduleMutationIntent{}, false
		}
		intent.Update = update
		intent.Summary = summary
	}
	return intent, true
}

func scheduleInputFromText(text string, msg channel.InboundMessage) schedule.CreateInput {
	cleaned := strings.TrimSpace(strings.TrimPrefix(text, scheduleIntentPrefix))
	dailyAt := firstTime(cleaned)
	weekdays := firstWeekdays(cleaned)
	monthlyDay := firstMonthlyDay(cleaned)
	interval := firstInterval(cleaned)
	title := scheduleTitle(cleaned)
	input := schedule.CreateInput{
		Title:        title,
		Prompt:       schedulePrompt(cleaned),
		DailyAt:      dailyAt,
		WeeklyAt:     dailyAt,
		Weekdays:     weekdays,
		MonthlyDay:   monthlyDay,
		Interval:     interval,
		AgentID:      "main",
		Channel:      firstNonEmpty(msg.Channel, "cli"),
		ThreadID:     msg.ThreadID,
		UserID:       msg.UserID,
		DeliveryMode: "artifact",
	}
	if interval != "" {
		input.DailyAt = ""
		input.WeeklyAt = ""
		input.Weekdays = nil
		input.MonthlyAt = ""
		input.MonthlyDay = 0
	}
	if monthlyDay > 0 {
		input.DailyAt = ""
		input.WeeklyAt = ""
		input.Weekdays = nil
		input.MonthlyAt = dailyAt
	}
	if len(weekdays) == 0 {
		input.WeeklyAt = ""
	}
	return input
}

func scheduleResolvedQuery(input schedule.CreateInput) string {
	return scheduleIntentPrefix + "\nFilled fields: title=" + input.Title + "; prompt=" + input.Prompt + "; daily_at=" + input.DailyAt + "; weekly_at=" + input.WeeklyAt + "; weekday=" + input.Weekday + "; weekdays=" + strings.Join(input.Weekdays, ",") + "; monthly_at=" + input.MonthlyAt + "; monthly_day=" + fmt.Sprint(input.MonthlyDay) + "; interval=" + input.Interval
}

func scheduleMutationResolvedQuery(intent scheduleMutationIntent) string {
	base := scheduleMutationPrefix + intent.Op + ":" + intent.Task.ID
	if intent.Op != "update" {
		return base
	}
	return base + "\n" + scheduleUpdateFieldText(intent.Update)
}

func scheduleProposalID(task session.TaskState) string {
	if task.PendingApproval != nil {
		action := strings.TrimSpace(task.PendingApproval.RequestedAction)
		if strings.HasPrefix(action, scheduleProposalIDPrefix) {
			return strings.TrimSpace(strings.TrimPrefix(action, scheduleProposalIDPrefix))
		}
	}
	fields := fieldsFromResolvedQuery(task.ResolvedQuery)
	return strings.TrimSpace(fields["id"])
}

func scheduleMutationTarget(task session.TaskState) (string, string) {
	if task.PendingApproval != nil {
		action := strings.TrimSpace(task.PendingApproval.RequestedAction)
		if strings.HasPrefix(action, scheduleMutationPrefix) {
			raw := strings.TrimSpace(strings.TrimPrefix(action, scheduleMutationPrefix))
			op, id, ok := strings.Cut(raw, ":")
			if ok {
				return strings.TrimSpace(op), strings.TrimSpace(id)
			}
		}
	}
	raw := strings.TrimSpace(strings.TrimPrefix(task.ResolvedQuery, scheduleMutationPrefix))
	op, id, ok := strings.Cut(raw, ":")
	if ok {
		return strings.TrimSpace(op), strings.TrimSpace(id)
	}
	return "", ""
}

func scheduleTaskMatchesText(task schedule.Task, text string) bool {
	id := strings.ToLower(strings.TrimSpace(task.ID))
	title := strings.ToLower(strings.TrimSpace(task.Title))
	if id != "" && strings.Contains(text, id) {
		return true
	}
	return title != "" && strings.Contains(text, title)
}

func scheduleUpdateFromText(text string) (schedule.UpdateInput, string, bool) {
	fields := fieldsFromResolvedQuery(text)
	update := scheduleUpdateFromFields(fields)
	if spec, ok := scheduleSpecFromMutationText(text); ok {
		update.Schedule = &spec
	}
	summary := scheduleUpdateSummary(update)
	return update, summary, summary != ""
}

func scheduleUpdateFromFields(fields map[string]string) schedule.UpdateInput {
	var update schedule.UpdateInput
	if len(fields) == 0 {
		return update
	}
	update.Title = fields["title"]
	update.Prompt = fields["prompt"]
	update.AgentID = fields["agent_id"]
	update.DeliveryMode = fields["delivery_mode"]
	update.DeliveryPath = fields["delivery_path"]
	input := schedule.ApplyDraftFields(schedule.CreateInput{}, fields)
	if hasScheduleFields(fields) {
		spec := scheduleSpecFromCreateInput(input)
		update.Schedule = &spec
	}
	return update
}

func scheduleSpecFromCreateInput(input schedule.CreateInput) schedule.ScheduleSpec {
	if strings.TrimSpace(input.Interval) != "" {
		return schedule.ScheduleSpec{Kind: "interval", Interval: strings.TrimSpace(input.Interval)}
	}
	if strings.TrimSpace(input.MonthlyAt) != "" || input.MonthlyDay > 0 {
		return schedule.ScheduleSpec{Kind: "monthly", MonthlyAt: firstNonEmpty(input.MonthlyAt, input.DailyAt, "09:00"), MonthlyDay: input.MonthlyDay}
	}
	if strings.TrimSpace(input.WeeklyAt) != "" || strings.TrimSpace(input.Weekday) != "" || len(input.Weekdays) > 0 {
		weekdays := input.Weekdays
		if len(weekdays) == 0 && strings.TrimSpace(input.Weekday) != "" {
			weekdays = []string{strings.TrimSpace(input.Weekday)}
		}
		return schedule.ScheduleSpec{Kind: "weekly", WeeklyAt: firstNonEmpty(input.WeeklyAt, input.DailyAt, "09:00"), Weekday: strings.TrimSpace(input.Weekday), Weekdays: weekdays}
	}
	return schedule.ScheduleSpec{Kind: "daily", DailyAt: firstNonEmpty(input.DailyAt, "09:00")}
}

func scheduleSpecFromMutationText(text string) (schedule.ScheduleSpec, bool) {
	dailyAt := firstTime(text)
	weekdays := firstWeekdays(text)
	monthlyDay := firstMonthlyDay(text)
	interval := firstInterval(text)
	if interval != "" {
		return schedule.ScheduleSpec{Kind: "interval", Interval: interval}, true
	}
	if monthlyDay > 0 {
		return schedule.ScheduleSpec{Kind: "monthly", MonthlyAt: firstNonEmpty(dailyAt, "09:00"), MonthlyDay: monthlyDay}, true
	}
	if len(weekdays) > 0 {
		return schedule.ScheduleSpec{Kind: "weekly", WeeklyAt: firstNonEmpty(dailyAt, "09:00"), Weekdays: weekdays}, true
	}
	if dailyAt != "" {
		return schedule.ScheduleSpec{Kind: "daily", DailyAt: dailyAt}, true
	}
	return schedule.ScheduleSpec{}, false
}

func hasScheduleFields(fields map[string]string) bool {
	for _, key := range []string{"daily_at", "weekly_at", "weekday", "weekdays", "monthly_at", "monthly_day", "interval"} {
		if strings.TrimSpace(fields[key]) != "" {
			return true
		}
	}
	return false
}

func scheduleUpdateFieldText(update schedule.UpdateInput) string {
	var fields []string
	if update.Title != "" {
		fields = append(fields, "title="+update.Title)
	}
	if update.Prompt != "" {
		fields = append(fields, "prompt="+update.Prompt)
	}
	if update.AgentID != "" {
		fields = append(fields, "agent_id="+update.AgentID)
	}
	if update.Schedule != nil {
		fields = append(fields, scheduleSpecFieldText(*update.Schedule))
	}
	if update.DeliveryMode != "" {
		fields = append(fields, "delivery_mode="+update.DeliveryMode)
	}
	if update.DeliveryPath != "" {
		fields = append(fields, "delivery_path="+update.DeliveryPath)
	}
	return strings.Join(fields, "; ")
}

func scheduleSpecFieldText(spec schedule.ScheduleSpec) string {
	switch spec.Kind {
	case "weekly":
		return "weekly_at=" + spec.WeeklyAt + "; weekdays=" + strings.Join(spec.Weekdays, ",")
	case "monthly":
		return "monthly_at=" + spec.MonthlyAt + "; monthly_day=" + fmt.Sprint(spec.MonthlyDay)
	case "interval":
		return "interval=" + spec.Interval
	default:
		return "daily_at=" + spec.DailyAt
	}
}

func scheduleUpdateSummary(update schedule.UpdateInput) string {
	var parts []string
	if update.Title != "" {
		parts = append(parts, "title")
	}
	if update.Prompt != "" {
		parts = append(parts, "prompt")
	}
	if update.AgentID != "" {
		parts = append(parts, "agent")
	}
	if update.Schedule != nil {
		parts = append(parts, "schedule="+schedule.Summary(*update.Schedule))
	}
	if update.DeliveryMode != "" || update.DeliveryPath != "" {
		parts = append(parts, "delivery")
	}
	return strings.Join(parts, ", ")
}

func fieldsFromResolvedQuery(text string) map[string]string {
	matches := scheduleFieldRE.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, match := range matches {
		key := strings.TrimSpace(match[1])
		value := strings.TrimSpace(match[2])
		if value != "" {
			out[key] = value
		} else if _, ok := out[key]; !ok {
			out[key] = ""
		}
	}
	return out
}

func firstTime(text string) string {
	match := scheduleTimeRE.FindStringSubmatch(text)
	if len(match) == 3 {
		return fmt.Sprintf("%02s:%s", match[1], match[2])
	}
	match = scheduleHourRE.FindStringSubmatch(text)
	if len(match) == 2 {
		return fmt.Sprintf("%02s:00", match[1])
	}
	return ""
}

func firstInterval(text string) string {
	match := scheduleIntervalRE.FindStringSubmatch(strings.ToLower(text))
	if len(match) < 3 {
		return ""
	}
	return match[1] + "h"
}

func firstWeekdays(text string) []string {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if textmatch.ContainsGroup(normalized, "schedule_workday_cues") {
		return []string{"monday", "tuesday", "wednesday", "thursday", "friday"}
	}
	days := []struct {
		name  string
		terms []string
	}{
		{"monday", []string{"monday", "mon", "\u5468\u4e00", "\u661f\u671f\u4e00"}},
		{"tuesday", []string{"tuesday", "tue", "\u5468\u4e8c", "\u661f\u671f\u4e8c"}},
		{"wednesday", []string{"wednesday", "wed", "\u5468\u4e09", "\u661f\u671f\u4e09"}},
		{"thursday", []string{"thursday", "thu", "\u5468\u56db", "\u661f\u671f\u56db"}},
		{"friday", []string{"friday", "fri", "\u5468\u4e94", "\u661f\u671f\u4e94"}},
		{"saturday", []string{"saturday", "sat", "\u5468\u516d", "\u661f\u671f\u516d"}},
		{"sunday", []string{"sunday", "sun", "\u5468\u65e5", "\u5468\u5929", "\u661f\u671f\u65e5", "\u661f\u671f\u5929"}},
	}
	var out []string
	for _, day := range days {
		for _, term := range day.terms {
			if strings.Contains(normalized, term) {
				out = append(out, day.name)
				break
			}
		}
	}
	return out
}

func firstMonthlyDay(text string) int {
	match := scheduleMonthlyDayRE.FindStringSubmatch(strings.ToLower(text))
	if len(match) != 2 {
		return 0
	}
	day := 0
	for _, ch := range match[1] {
		day = day*10 + int(ch-'0')
	}
	if day < 1 || day > 31 {
		return 0
	}
	return day
}

func blankPendingFields(fields map[string]string) map[string]string {
	if len(fields) == 0 {
		return nil
	}
	out := map[string]string{}
	for key := range fields {
		out[key] = ""
	}
	return out
}

func scheduleTitle(text string) string {
	text = strings.TrimSpace(text)
	for _, cue := range textmatch.Terms("schedule_strip_cues") {
		text = strings.ReplaceAll(text, cue, " ")
	}
	text = scheduleTimeRE.ReplaceAllString(text, " ")
	text = strings.Join(strings.Fields(text), " ")
	if len([]rune(text)) > 60 {
		return string([]rune(text)[:60])
	}
	return firstNonEmpty(text, "Scheduled task")
}

func schedulePrompt(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return "Scheduled task request: " + text
}
