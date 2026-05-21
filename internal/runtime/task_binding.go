package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/followup"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/textmatch"
)

const (
	bindingApprovalReply          = "approval_reply"
	bindingPendingApprovalBlocked = "pending_approval_blocked"
	bindingReplacePendingApproval = "replace_pending_approval"
	bindingSlotFill               = "slot_fill"
	bindingActiveFollowup         = "active_followup"
	bindingOpenTaskFollowup       = "open_task_followup"
	bindingHistoricalContinuation = "historical_continuation"
	bindingNewTask                = "new_task"
	bindingAmbiguous              = "ambiguous"
)

const followupConfidenceThreshold = 0.72

type taskBindingDecision struct {
	Kind            string
	TargetTaskID    string
	SourceTaskID    string
	ResolvedQuery   string
	Reason          string
	Confidence      float64
	FilledFields    map[string]string
	ClarifyPrompt   string
	ApprovalGranted bool
}

func (l *AgentLoop) resolveTaskBinding(ctx context.Context) taskBindingDecision {
	l.runtime.Logger.Event("runtime.task_binding_started", map[string]any{
		"trace_id":    l.state.traceID,
		"session_key": l.state.message.SessionKey,
		"active_task": l.state.session.ActiveTaskID,
	})
	if decision, ok := l.resolveApprovalReply(); ok {
		return decision
	}
	if decision, ok := l.resolvePendingApprovalBlock(); ok {
		return decision
	}
	if decision, ok := l.resolveSlotFill(); ok {
		return decision
	}
	if decision, ok := l.resolveRuleFollowup(); ok {
		return decision
	}
	if decision, ok := l.resolveModelFollowup(ctx); ok {
		return decision
	}
	return taskBindingDecision{
		Kind:          bindingNewTask,
		TargetTaskID:  l.state.traceID,
		ResolvedQuery: strings.TrimSpace(l.state.message.Text),
		Reason:        "no existing task matched; treating input as a new task",
		Confidence:    0.9,
	}
}

func (l *AgentLoop) applyTaskBinding(decision taskBindingDecision) *Response {
	l.state.binding = decision
	l.state.resolvedQuery = firstNonEmpty(decision.ResolvedQuery, l.state.message.Text)
	active := session.ActiveTask(l.state.session)
	switch decision.Kind {
	case bindingPendingApprovalBlocked:
		text := firstNonEmpty(decision.ClarifyPrompt, "当前还有一个操作等待确认。请先回复“确认”或“取消”；如果要开始新任务，请先取消当前待确认操作。")
		reply := l.runtime.sanitizeReply(channel.OutboundMessage{
			Channel:  l.state.message.Channel,
			ThreadID: l.state.message.ThreadID,
			Text:     text,
			Style:    "input_required",
			Title:    "Mateway 等待确认",
		})
		resp := Response{Reply: reply, TraceID: l.state.traceID, AwaitUserInput: true}
		l.runtime.Logger.Event("runtime.followup_resolved", l.bindingTraceFields(decision))
		l.saveConversationOnly(resp)
		return &resp
	case bindingReplacePendingApproval:
		if active != nil {
			active.Status = session.TaskAbandoned
			active.PendingApproval = nil
			active.PendingQuestions = nil
			active.PendingFields = nil
			active.UpdatedAt = l.state.startedAt
			l.state.session.Tasks[active.ID] = *active
		}
		task := session.TaskState{
			ID:            decision.TargetTaskID,
			TraceID:       l.state.traceID,
			Status:        session.TaskOpen,
			UserText:      l.state.message.Text,
			ResolvedQuery: l.state.resolvedQuery,
			StartedAt:     l.state.startedAt,
			UpdatedAt:     l.state.startedAt,
		}
		l.state.currentTask = &task
	case bindingAmbiguous:
		text := firstNonEmpty(decision.ClarifyPrompt, "I am not sure which task you want to continue. Please add a bit more context.")
		reply := l.runtime.sanitizeReply(channel.OutboundMessage{
			Channel:  l.state.message.Channel,
			ThreadID: l.state.message.ThreadID,
			Text:     text,
			Style:    "input_required",
			Title:    "Mateway needs context",
		})
		resp := Response{Reply: reply, TraceID: l.state.traceID, AwaitUserInput: true}
		l.runtime.Logger.Event("runtime.followup_resolved", l.bindingTraceFields(decision))
		l.saveConversationOnly(resp)
		return &resp
	case bindingHistoricalContinuation:
		source := l.state.session.Tasks[decision.SourceTaskID]
		task := source
		task.ID = l.state.traceID
		task.TraceID = l.state.traceID
		task.ParentTaskID = source.ID
		task.ContinuationOfTaskID = source.ID
		task.Status = session.TaskOpen
		task.UserText = l.state.message.Text
		task.ResolvedQuery = l.state.resolvedQuery
		task.PlanSummary = ""
		task.ToolNames = nil
		task.SelectedSkills = nil
		task.ResultCount = 0
		task.ReplyPreview = ""
		task.LastAnswer = ""
		task.PendingApproval = nil
		task.PendingQuestions = nil
		task.PendingFields = nil
		task.StartedAt = l.state.startedAt
		task.FinishedAt = time.Time{}
		task.UpdatedAt = l.state.startedAt
		l.state.currentTask = &task
		l.runtime.Logger.Event("runtime.task_continuation_created", map[string]any{
			"trace_id":       l.state.traceID,
			"source_task_id": source.ID,
			"target_task_id": task.ID,
		})
	case bindingNewTask:
		task := session.TaskState{
			ID:            decision.TargetTaskID,
			TraceID:       l.state.traceID,
			Status:        session.TaskOpen,
			UserText:      l.state.message.Text,
			ResolvedQuery: l.state.resolvedQuery,
			StartedAt:     l.state.startedAt,
			UpdatedAt:     l.state.startedAt,
		}
		l.state.currentTask = &task
	default:
		if decision.TargetTaskID != "" {
			task := l.state.session.Tasks[decision.TargetTaskID]
			task.TraceID = l.state.traceID
			task.UserText = l.state.message.Text
			task.ResolvedQuery = l.state.resolvedQuery
			task.UpdatedAt = l.state.startedAt
			if decision.Kind == bindingApprovalReply {
				task.Status = session.TaskOpen
				task.PendingApproval = nil
				task.PendingQuestions = nil
			}
			if decision.Kind == bindingSlotFill {
				task.PendingFields = applyFilledFields(task.PendingFields, decision.FilledFields)
				if len(task.PendingFields) == 0 {
					task.Status = session.TaskOpen
					task.PendingQuestions = nil
				} else {
					task.Status = session.TaskAwaitUserInput
					nextQuestion := "I still need these fields: "
					var missing []string
					for key, value := range task.PendingFields {
						if strings.TrimSpace(value) == "" {
							missing = append(missing, key)
						}
					}
					if len(missing) > 0 {
						nextQuestion += strings.Join(missing, ", ")
						task.PendingQuestions = []string{nextQuestion}
					}
				}
			}
			l.state.currentTask = &task
		}
	}
	if l.state.currentTask == nil && active != nil {
		l.state.currentTask = active
	}
	if l.state.currentTask != nil {
		l.state.topic = firstNonEmpty(l.state.currentTask.Topic, l.state.topic)
		l.runtime.Logger.Event("runtime.task_activated", map[string]any{
			"trace_id":    l.state.traceID,
			"task_id":     l.state.currentTask.ID,
			"kind":        decision.Kind,
			"task_status": l.state.currentTask.Status,
		})
		if decision.Kind == bindingSlotFill && len(l.state.currentTask.PendingFields) > 0 {
			text := firstNonEmpty(strings.Join(l.state.currentTask.PendingQuestions, "\n"), "我还需要你补充一个信息才能继续。")
			reply := l.runtime.sanitizeReply(channel.OutboundMessage{
				Channel:  l.state.message.Channel,
				ThreadID: l.state.message.ThreadID,
				Text:     text,
				Style:    "input_required",
				Title:    "Mateway needs more information",
			})
			resp := Response{Reply: reply, TraceID: l.state.traceID, AwaitUserInput: true}
			l.runtime.Logger.Event("runtime.task_pending_input", map[string]any{
				"trace_id": l.state.traceID,
				"task_id":  l.state.currentTask.ID,
				"fields":   pendingFieldNames(l.state.currentTask.PendingFields),
			})
			l.runtime.Logger.Event("runtime.followup_resolved", l.bindingTraceFields(decision))
			l.saveSession(resp)
			return &resp
		}
		if decision.Kind == bindingApprovalReply && !decision.ApprovalGranted {
			l.state.currentTask.Status = session.TaskAbandoned
			text := "Canceled the previous pending operation."
			reply := l.runtime.sanitizeReply(channel.OutboundMessage{
				Channel:  l.state.message.Channel,
				ThreadID: l.state.message.ThreadID,
				Text:     text,
				Style:    "reply",
				Title:    "Mateway canceled the operation",
			})
			resp := Response{Reply: reply, TraceID: l.state.traceID}
			l.runtime.Logger.Event("runtime.followup_resolved", l.bindingTraceFields(decision))
			l.saveSession(resp)
			return &resp
		}
	}
	l.runtime.Logger.Event("runtime.followup_resolved", l.bindingTraceFields(decision))
	return nil
}

func (l *AgentLoop) resolveApprovalReply() (taskBindingDecision, bool) {
	task := session.ActiveTask(l.state.session)
	if task == nil || task.PendingApproval == nil {
		return taskBindingDecision{}, false
	}
	if task.Status != session.TaskAwaitConfirm && task.Status != session.TaskAwaitUserInput {
		return taskBindingDecision{}, false
	}
	text := normalizeFollowupText(l.state.message.Text)
	if text == "" {
		return taskBindingDecision{}, false
	}
	if approved, ok := parseApprovalDecision(text, task.PendingApproval); ok {
		action := "Continue the previous task"
		if approved {
			action = "Continue and approve the previous task"
		}
		resolved := action + ":\n" + firstNonEmpty(task.ResolvedQuery, task.UserText)
		return taskBindingDecision{
			Kind:            bindingApprovalReply,
			TargetTaskID:    task.ID,
			ResolvedQuery:   resolved,
			Reason:          "matched approval reply for active task",
			Confidence:      0.98,
			ApprovalGranted: approved,
		}, true
	}
	return taskBindingDecision{}, false
}

func (l *AgentLoop) resolvePendingApprovalBlock() (taskBindingDecision, bool) {
	task := session.ActiveTask(l.state.session)
	if task == nil || task.PendingApproval == nil || task.Status != session.TaskAwaitConfirm {
		return taskBindingDecision{}, false
	}
	if isPendingApprovalReplacementRequest(l.state.message.Text) {
		return taskBindingDecision{
			Kind:          bindingReplacePendingApproval,
			TargetTaskID:  l.state.traceID,
			SourceTaskID:  task.ID,
			ResolvedQuery: strings.TrimSpace(l.state.message.Text),
			Reason:        "user asked to replace the pending approval with a different approach",
			Confidence:    0.94,
		}, true
	}
	prompt := strings.TrimSpace(task.PendingApproval.Prompt)
	if prompt == "" {
		prompt = firstNonEmpty(task.PendingApproval.RequestedAction, task.ResolvedQuery, task.UserText)
	}
	return taskBindingDecision{
		Kind:          bindingPendingApprovalBlocked,
		TargetTaskID:  task.ID,
		ResolvedQuery: strings.TrimSpace(l.state.message.Text),
		Reason:        "blocked non-approval message while a confirmation is pending",
		Confidence:    0.95,
		ClarifyPrompt: "当前还有一个操作等待确认：\n\n" + prompt + "\n\n请先回复“确认”继续，或回复“取消”放弃。处理完以后再发送新的任务。",
	}, true
}

func (l *AgentLoop) resolveSlotFill() (taskBindingDecision, bool) {
	task := session.ActiveTask(l.state.session)
	if task == nil || len(task.PendingFields) == 0 {
		return taskBindingDecision{}, false
	}
	if task.Status != session.TaskAwaitUserInput {
		return taskBindingDecision{}, false
	}
	filled := fillPendingFields(task.PendingFields, l.state.message.Text)
	if len(filled) == 0 {
		return taskBindingDecision{}, false
	}
	resolved := buildResolvedQueryWithFields(firstNonEmpty(task.ResolvedQuery, task.UserText), mergeFieldMaps(task.PendingFields, filled))
	return taskBindingDecision{
		Kind:          bindingSlotFill,
		TargetTaskID:  task.ID,
		ResolvedQuery: resolved,
		Reason:        "matched slot fill for active task",
		Confidence:    0.96,
		FilledFields:  filled,
	}, true
}

func (l *AgentLoop) resolveRuleFollowup() (taskBindingDecision, bool) {
	task := session.ActiveTask(l.state.session)
	if task == nil {
		return taskBindingDecision{}, false
	}
	if shouldDeferFollowupToModel(l.state.message.Text) {
		return taskBindingDecision{}, false
	}
	decision := followup.Resolver{}.Resolve(followup.Input{
		CurrentMessage: l.state.message.Text,
		RecentTurns:    l.state.session.RecentTurns,
		LastTask:       task,
	})
	if !decision.IsFollowup {
		return taskBindingDecision{}, false
	}
	return taskBindingDecision{
		Kind:          bindingActiveFollowup,
		TargetTaskID:  task.ID,
		ResolvedQuery: strengthenFollowupInstruction(firstNonEmpty(decision.ResolvedQuery, task.ResolvedQuery, task.UserText, l.state.message.Text), l.state.message.Text),
		Reason:        firstNonEmpty(decision.Reason, "rule-based resolver matched active task followup"),
		Confidence:    decision.Confidence,
	}, true
}

func strengthenFollowupInstruction(resolved, current string) string {
	resolved = strings.TrimSpace(resolved)
	current = strings.TrimSpace(current)
	if current == "" {
		return resolved
	}
	if resolved == "" {
		return current
	}
	if !containsStructuralFollowupIntent(current) {
		return resolved
	}
	return resolved + "\n\nImportant: the current additional request has the highest priority. Execute it directly; do not only restate the previous topic.\nCurrent additional request: " + current
}

func containsStructuralFollowupIntent(text string) bool {
	normalized := normalizeFollowupText(text)
	return textmatch.ContainsGroup(normalized, "structural_followup")
}

func shouldDeferFollowupToModel(text string) bool {
	normalized := normalizeFollowupText(text)
	if normalized == "" {
		return false
	}
	return textmatch.ContainsGroup(normalized, "history_defer")
}

func (l *AgentLoop) resolveModelFollowup(ctx context.Context) (taskBindingDecision, bool) {
	prompt := buildFollowupPrompt(l.state.message.Text, l.state.session)
	decision, err := l.runtime.Model.ResolveFollowupJSON(ctx, prompt)
	if err != nil {
		return taskBindingDecision{}, false
	}
	if decision.Confidence < followupConfidenceThreshold || strings.TrimSpace(decision.Kind) == "" {
		return taskBindingDecision{
			Kind:          bindingAmbiguous,
			ResolvedQuery: strings.TrimSpace(l.state.message.Text),
			Reason:        firstNonEmpty(decision.Reason, "could not confidently identify the target task"),
			Confidence:    decision.Confidence,
			ClarifyPrompt: "I am not sure which task you want to continue. Please mention a clue such as the task topic, document name, or link.",
		}, true
	}
	kind := strings.TrimSpace(decision.Kind)
	switch kind {
	case bindingActiveFollowup, bindingOpenTaskFollowup, bindingHistoricalContinuation, bindingNewTask, bindingAmbiguous:
	default:
		kind = bindingAmbiguous
	}
	if kind == bindingAmbiguous {
		return taskBindingDecision{
			Kind:          bindingAmbiguous,
			ResolvedQuery: strings.TrimSpace(l.state.message.Text),
			Reason:        firstNonEmpty(decision.Reason, "task target is ambiguous"),
			Confidence:    decision.Confidence,
			ClarifyPrompt: "I am not sure which task you want to continue. Please include a clearer clue such as the topic, document, or link.",
		}, true
	}
	if kind == bindingActiveFollowup && strings.TrimSpace(decision.TargetTaskID) == "" && strings.TrimSpace(l.state.session.ActiveTaskID) != "" {
		decision.TargetTaskID = l.state.session.ActiveTaskID
	}
	if kind == bindingHistoricalContinuation && !l.taskExists(decision.SourceTaskID) {
		return taskBindingDecision{
			Kind:          bindingAmbiguous,
			ResolvedQuery: strings.TrimSpace(l.state.message.Text),
			Reason:        "model returned a missing historical task",
			Confidence:    decision.Confidence,
			ClarifyPrompt: "我没找到你提到的历史任务。可以补一个更明确的线索，比如文档名、链接主题或任务内容。",
		}, true
	}
	if (kind == bindingActiveFollowup || kind == bindingOpenTaskFollowup) && !l.taskExists(decision.TargetTaskID) {
		return taskBindingDecision{
			Kind:          bindingAmbiguous,
			ResolvedQuery: strings.TrimSpace(l.state.message.Text),
			Reason:        "model returned a missing target task",
			Confidence:    decision.Confidence,
			ClarifyPrompt: "我没找到要继续的任务。请补充更明确的上下文。",
		}, true
	}
	if kind == bindingNewTask {
		decision.TargetTaskID = l.state.traceID
	}
	return taskBindingDecision{
		Kind:          kind,
		TargetTaskID:  decision.TargetTaskID,
		SourceTaskID:  decision.SourceTaskID,
		ResolvedQuery: firstNonEmpty(decision.ResolvedQuery, l.state.message.Text),
		Reason:        decision.Reason,
		Confidence:    decision.Confidence,
	}, true
}

func (l *AgentLoop) taskExists(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	_, ok := l.state.session.Tasks[id]
	return ok
}

func buildFollowupPrompt(current string, st session.State) string {
	payload := map[string]any{
		"current_date":   time.Now().Format("2006-01-02"),
		"timezone":       time.Now().Location().String(),
		"current_input":  strings.TrimSpace(current),
		"active_task_id": st.ActiveTaskID,
		"active_task":    summarizeTaskForFollowup(session.ActiveTask(st)),
		"open_tasks":     summarizeTasksForFollowup(current, session.OpenTasks(st), st.ActiveTaskID, 6),
		"history_tasks":  summarizeTasksForFollowup(current, session.HistoricalTasks(st, 12), "", 8),
		"recent_turns":   st.RecentTurns,
	}
	data, _ := json.MarshalIndent(payload, "", "  ")
	return string(data)
}

func summarizeTaskForFollowup(task *session.TaskState) map[string]any {
	if task == nil {
		return nil
	}
	return map[string]any{
		"id":                task.ID,
		"status":            task.Status,
		"topic":             task.Topic,
		"user_text":         shortenReply(task.UserText, 120),
		"resolved_query":    shortenReply(task.ResolvedQuery, 160),
		"reply_preview":     shortenReply(task.ReplyPreview, 120),
		"pending_fields":    task.PendingFields,
		"pending_questions": task.PendingQuestions,
		"pending_approval":  task.PendingApproval,
		"artifacts":         summarizeArtifacts(task.Artifacts, 4),
		"artifact_keywords": artifactKeywords(task.Artifacts),
		"updated_at":        task.UpdatedAt,
	}
}

func summarizeTasksForFollowup(current string, tasks []session.TaskState, excludeID string, limit int) []map[string]any {
	type scored struct {
		task  session.TaskState
		score int
	}
	scoredTasks := make([]scored, 0, len(tasks))
	for _, task := range tasks {
		if task.ID == excludeID {
			continue
		}
		scoredTasks = append(scoredTasks, scored{task: task, score: scoreTaskForFollowup(current, task)})
	}
	sort.SliceStable(scoredTasks, func(i, j int) bool {
		if scoredTasks[i].score != scoredTasks[j].score {
			return scoredTasks[i].score > scoredTasks[j].score
		}
		return scoredTasks[i].task.UpdatedAt.After(scoredTasks[j].task.UpdatedAt)
	})
	out := make([]map[string]any, 0, len(scoredTasks))
	for _, item := range scoredTasks {
		if limit > 0 && len(out) >= limit {
			break
		}
		summary := summarizeTaskForFollowup(&item.task)
		summary["relevance_score"] = item.score
		out = append(out, summary)
	}
	return out
}

func scoreTaskForFollowup(current string, task session.TaskState) int {
	tokens := tokenizeForMatch(current)
	if len(tokens) == 0 {
		return 0
	}
	score := 0
	haystacks := []string{
		task.Topic,
		task.UserText,
		task.ResolvedQuery,
		task.ReplyPreview,
	}
	for _, artifact := range task.Artifacts {
		haystacks = append(haystacks, artifact.Label, artifact.Path, artifact.SourceURL, artifact.Summary)
	}
	for _, token := range tokens {
		for _, hay := range haystacks {
			if strings.Contains(strings.ToLower(hay), token) {
				score++
				break
			}
		}
	}
	if task.IsOpenLike() {
		score += 2
	}
	return score
}

func tokenizeForMatch(text string) []string {
	text = strings.ToLower(strings.TrimSpace(text))
	replacer := strings.NewReplacer("，", " ", "。", " ", "？", " ", "！", " ", "：", " ", "；", " ", ",", " ", ".", " ", "\n", " ", "\t", " ")
	text = replacer.Replace(text)
	parts := strings.Fields(text)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if len([]rune(part)) < 2 {
			continue
		}
		out = append(out, part)
	}
	return out
}

func summarizeArtifacts(artifacts []session.Artifact, limit int) []map[string]any {
	if len(artifacts) == 0 {
		return nil
	}
	if limit > 0 && len(artifacts) > limit {
		artifacts = artifacts[:limit]
	}
	out := make([]map[string]any, 0, len(artifacts))
	for _, artifact := range artifacts {
		out = append(out, map[string]any{
			"kind":       artifact.Kind,
			"path":       artifact.Path,
			"label":      artifact.Label,
			"source_url": artifact.SourceURL,
			"summary":    shortenReply(artifact.Summary, 100),
		})
	}
	return out
}

func artifactKeywords(artifacts []session.Artifact) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, artifact := range artifacts {
		for _, raw := range []string{artifact.Label, filepathBase(artifact.Path), filepathBase(artifact.SourceURL), artifact.Summary} {
			for _, token := range tokenizeForMatch(raw) {
				if _, ok := seen[token]; ok {
					continue
				}
				seen[token] = struct{}{}
				out = append(out, token)
				if len(out) >= 8 {
					return out
				}
			}
		}
	}
	return out
}

func filepathBase(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	parts := strings.FieldsFunc(text, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return text
	}
	return parts[len(parts)-1]
}

func normalizeFollowupText(text string) string {
	replacer := strings.NewReplacer("\uFF0C", "", "\u3002", "", "\uFF1F", "", "\uFF01", "", "\uFF1A", "", "\n", " ")
	return strings.ToLower(strings.TrimSpace(replacer.Replace(text)))
}

func parseApprovalDecision(text string, approval *session.PendingApproval) (bool, bool) {
	if approval == nil {
		return false, false
	}
	switch approval.ApprovalType {
	case "boolean_confirm", "schedule_proposal_confirm", "schedule_mutation_confirm", "":
		switch {
		case textmatch.ExactGroup(text, "approval_yes") || text == "yes" || text == "y" || text == "ok":
			return true, true
		case textmatch.ExactGroup(text, "approval_no") || text == "no" || text == "n":
			return false, true
		}
	case "single_choice":
		if idx, err := parseChoiceIndex(text); err == nil && idx >= 0 && idx < len(approval.Options) {
			return true, true
		}
	}
	return false, false
}

func isPendingApprovalReplacementRequest(text string) bool {
	normalized := normalizeFollowupText(text)
	if normalized == "" {
		return false
	}
	replacementCues := []string{
		"换", "改用", "换成", "换为", "不要这个", "别用这个", "重新", "另一种", "其他方式", "别装这个",
		"use instead", "switch", "change to", "replace", "different way", "another way", "not this",
	}
	methodCues := []string{
		"homebrew", "brew", "go install", "npm", "pnpm", "pip", "uv", "docker", "源码", "source", "binary", "release",
	}
	hasReplacementCue := false
	for _, cue := range replacementCues {
		if strings.Contains(normalized, cue) {
			hasReplacementCue = true
			break
		}
	}
	if !hasReplacementCue {
		return false
	}
	for _, cue := range methodCues {
		if strings.Contains(normalized, cue) {
			return true
		}
	}
	return strings.Contains(normalized, "安装") || strings.Contains(normalized, "install")
}

func parseChoiceIndex(text string) (int, error) {
	for _, prefix := range textmatch.Terms("choice_prefix") {
		text = strings.TrimSpace(strings.TrimPrefix(text, prefix))
	}
	switch text {
	case "1":
		return 0, nil
	case "2":
		return 1, nil
	case "3":
		return 2, nil
	default:
		switch {
		case textmatch.ExactGroup(text, "choice_first"):
			return 0, nil
		case textmatch.ExactGroup(text, "choice_second"):
			return 1, nil
		case textmatch.ExactGroup(text, "choice_third"):
			return 2, nil
		default:
			return -1, fmt.Errorf("invalid choice")
		}
	}
}

func fillPendingFields(pending map[string]string, input string) map[string]string {
	if len(pending) == 0 {
		return nil
	}
	filled := map[string]string{}
	parts := strings.FieldsFunc(strings.TrimSpace(input), func(r rune) bool {
		return r == ',' || r == '\uFF0C' || r == '\n' || r == ';' || r == '\uFF1B'
	})
	for _, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			key, value, ok = strings.Cut(part, ":")
		}
		if ok {
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			if _, exists := pending[key]; exists && value != "" {
				filled[key] = value
			}
		}
	}
	if len(filled) > 0 {
		return filled
	}
	var missing []string
	for key, value := range pending {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) == 1 && strings.TrimSpace(input) != "" {
		return map[string]string{missing[0]: strings.TrimSpace(input)}
	}
	return nil
}

func applyFilledFields(pending, filled map[string]string) map[string]string {
	if len(pending) == 0 {
		return nil
	}
	out := map[string]string{}
	for key, value := range pending {
		if next, ok := filled[key]; ok {
			value = next
		}
		if strings.TrimSpace(value) == "" {
			out[key] = ""
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mergeFieldMaps(base, filled map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range base {
		out[key] = value
	}
	for key, value := range filled {
		out[key] = value
	}
	return out
}

func buildResolvedQueryWithFields(base string, fields map[string]string) string {
	base = strings.TrimSpace(base)
	if len(fields) == 0 {
		return base
	}
	var parts []string
	for key, value := range fields {
		if strings.TrimSpace(value) != "" {
			parts = append(parts, fmt.Sprintf("%s=%s", key, value))
		}
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		return base
	}
	return base + "\nFilled fields: " + strings.Join(parts, "; ")
}

func pendingFieldNames(fields map[string]string) []string {
	var out []string
	for key, value := range fields {
		if strings.TrimSpace(value) == "" {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func (l *AgentLoop) bindingTraceFields(decision taskBindingDecision) map[string]any {
	return map[string]any{
		"trace_id":       l.state.traceID,
		"session_key":    l.state.message.SessionKey,
		"kind":           decision.Kind,
		"target_task_id": decision.TargetTaskID,
		"source_task_id": decision.SourceTaskID,
		"resolved_query": decision.ResolvedQuery,
		"reason":         decision.Reason,
		"confidence":     decision.Confidence,
	}
}
