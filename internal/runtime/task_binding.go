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
)

const (
	bindingApprovalReply          = "approval_reply"
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
	if decision, ok := l.resolveSlotFill(); ok {
		return decision
	}
	if decision, ok := l.resolvePendingConfirmNewTask(); ok {
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
		Reason:        "未命中已有任务，作为新任务处理",
		Confidence:    0.9,
	}
}

func (l *AgentLoop) applyTaskBinding(decision taskBindingDecision) *Response {
	l.state.binding = decision
	l.state.resolvedQuery = firstNonEmpty(decision.ResolvedQuery, l.state.message.Text)
	active := session.ActiveTask(l.state.session)
	switch decision.Kind {
	case bindingAmbiguous:
		text := firstNonEmpty(decision.ClarifyPrompt, "我还不能确定你是在继续哪个任务。请再说清楚一点。")
		reply := l.runtime.sanitizeReply(channel.OutboundMessage{
			Channel:  l.state.message.Channel,
			ThreadID: l.state.message.ThreadID,
			Text:     text,
			Style:    "input_required",
			Title:    "Mateway 需要确认上下文",
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
					nextQuestion := "还需要补充这些信息："
					var missing []string
					for key, value := range task.PendingFields {
						if strings.TrimSpace(value) == "" {
							missing = append(missing, key)
						}
					}
					if len(missing) > 0 {
						nextQuestion += strings.Join(missing, "、")
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
			text := firstNonEmpty(strings.Join(l.state.currentTask.PendingQuestions, "\n"), "还需要你补充一点信息。")
			reply := l.runtime.sanitizeReply(channel.OutboundMessage{
				Channel:  l.state.message.Channel,
				ThreadID: l.state.message.ThreadID,
				Text:     text,
				Style:    "input_required",
				Title:    "Mateway 还需要信息",
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
			text := "已取消上一轮待确认操作。"
			reply := l.runtime.sanitizeReply(channel.OutboundMessage{
				Channel:  l.state.message.Channel,
				ThreadID: l.state.message.ThreadID,
				Text:     text,
				Style:    "reply",
				Title:    "Mateway 已取消",
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
		action := "继续执行上一轮任务"
		if approved {
			action = "继续并批准上一轮任务"
		}
		resolved := action + "：\n" + firstNonEmpty(task.ResolvedQuery, task.UserText)
		return taskBindingDecision{
			Kind:            bindingApprovalReply,
			TargetTaskID:    task.ID,
			ResolvedQuery:   resolved,
			Reason:          "命中当前活动任务的批准回复",
			Confidence:      0.98,
			ApprovalGranted: approved,
		}, true
	}
	return taskBindingDecision{}, false
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
		Reason:        "命中当前活动任务的参数补全",
		Confidence:    0.96,
		FilledFields:  filled,
	}, true
}

func (l *AgentLoop) resolvePendingConfirmNewTask() (taskBindingDecision, bool) {
	task := session.ActiveTask(l.state.session)
	if task == nil || task.PendingApproval == nil || task.Status != session.TaskAwaitConfirm {
		return taskBindingDecision{}, false
	}
	text := strings.TrimSpace(l.state.message.Text)
	if !looksLikeIndependentRequestDuringConfirm(text) {
		return taskBindingDecision{}, false
	}
	return taskBindingDecision{
		Kind:          bindingNewTask,
		TargetTaskID:  l.state.traceID,
		ResolvedQuery: text,
		Reason:        "待确认状态下识别到明显独立新请求，优先开启新任务",
		Confidence:    0.88,
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
		Reason:        firstNonEmpty(decision.Reason, "规则解析为继续当前活动任务"),
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
	return resolved + "\n\n重要：本轮补充要求优先级最高，必须明确执行，不要只复述上一轮主题。\n本轮补充要求：" + current
}

func containsStructuralFollowupIntent(text string) bool {
	normalized := normalizeFollowupText(text)
	cues := []string{
		"拆成", "拆分", "三条", "3条", "三个", "3个", "步骤", "检查项", "清单", "列表", "可执行", "验收标准", "压缩成", "改成",
	}
	for _, cue := range cues {
		if strings.Contains(normalized, cue) {
			return true
		}
	}
	return false
}

func shouldDeferFollowupToModel(text string) bool {
	normalized := normalizeFollowupText(text)
	if normalized == "" {
		return false
	}
	return strings.Contains(normalized, "昨天") ||
		strings.Contains(normalized, "前天") ||
		strings.Contains(normalized, "上次") ||
		strings.Contains(normalized, "之前") ||
		strings.Contains(normalized, "历史")
}

func looksLikeIndependentRequestDuringConfirm(text string) bool {
	trimmed := strings.TrimSpace(text)
	if len([]rune(trimmed)) < 8 {
		return false
	}
	normalized := normalizeFollowupText(trimmed)
	if normalized == "" {
		return false
	}
	if _, ok := parseApprovalDecision(normalized, &session.PendingApproval{ApprovalType: "boolean_confirm"}); ok {
		return false
	}
	cancelOnly := []string{"先不", "暂时不", "先算了", "不用了", "不要执行", "别执行", "先别执行"}
	for _, cue := range cancelOnly {
		if normalized == cue {
			return false
		}
	}
	newTopicCues := []string{
		"新问题", "另一个问题", "另外", "换个话题", "先不管", "先别管", "先跳过", "回到",
	}
	for _, cue := range newTopicCues {
		if strings.Contains(normalized, cue) {
			return true
		}
	}
	requestCues := []string{
		"请", "帮我", "帮忙", "麻烦", "总结", "搜索", "查一下", "查看", "阅读", "解释", "分析", "列出", "生成", "写一份", "整理", "评估", "对比", "修复", "处理",
	}
	for _, cue := range requestCues {
		if strings.Contains(normalized, cue) {
			return true
		}
	}
	return false
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
			Reason:        firstNonEmpty(decision.Reason, "无法高置信识别要继续的任务"),
			Confidence:    decision.Confidence,
			ClarifyPrompt: "我还不能确定你是在继续哪个任务。请补一句，例如“继续刚才的安装任务”或“接着昨天的 AI 趋势讨论”。",
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
			Reason:        firstNonEmpty(decision.Reason, "任务目标不明确"),
			Confidence:    decision.Confidence,
			ClarifyPrompt: "我还不能确定你要继续哪个任务。请带上“刚才的安装”“昨天的 AI 趋势文档”之类线索。",
		}, true
	}
	if kind == bindingActiveFollowup && strings.TrimSpace(decision.TargetTaskID) == "" && strings.TrimSpace(l.state.session.ActiveTaskID) != "" {
		decision.TargetTaskID = l.state.session.ActiveTaskID
	}
	if kind == bindingHistoricalContinuation && !l.taskExists(decision.SourceTaskID) {
		return taskBindingDecision{
			Kind:          bindingAmbiguous,
			ResolvedQuery: strings.TrimSpace(l.state.message.Text),
			Reason:        "模型返回的历史任务不存在",
			Confidence:    decision.Confidence,
			ClarifyPrompt: "我没找到你提到的历史任务。请带上更明确的线索，比如文档名、链接主题或任务内容。",
		}, true
	}
	if (kind == bindingActiveFollowup || kind == bindingOpenTaskFollowup) && !l.taskExists(decision.TargetTaskID) {
		return taskBindingDecision{
			Kind:          bindingAmbiguous,
			ResolvedQuery: strings.TrimSpace(l.state.message.Text),
			Reason:        "模型返回的目标任务不存在",
			Confidence:    decision.Confidence,
			ClarifyPrompt: "我没找到要继续的那个任务。请补一句更明确的上下文。",
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
	replacer := strings.NewReplacer("，", "", "。", "", "？", "", "！", "", "：", "", "\n", " ")
	return strings.ToLower(strings.TrimSpace(replacer.Replace(text)))
}

func parseApprovalDecision(text string, approval *session.PendingApproval) (bool, bool) {
	if approval == nil {
		return false, false
	}
	switch approval.ApprovalType {
	case "boolean_confirm", "":
		switch text {
		case "同意", "可以", "好", "好的", "行", "安装吧", "确认", "yes", "y", "ok":
			return true, true
		case "拒绝", "不要", "不行", "取消", "no", "n":
			return false, true
		}
	case "single_choice":
		if idx, err := parseChoiceIndex(text); err == nil && idx >= 0 && idx < len(approval.Options) {
			return true, true
		}
	}
	return false, false
}

func parseChoiceIndex(text string) (int, error) {
	text = strings.TrimSpace(strings.TrimPrefix(text, "选"))
	switch text {
	case "第一", "第一个", "1":
		return 0, nil
	case "第二", "第二个", "2":
		return 1, nil
	case "第三", "第三个", "3":
		return 2, nil
	default:
		return -1, fmt.Errorf("invalid choice")
	}
}

func fillPendingFields(pending map[string]string, input string) map[string]string {
	if len(pending) == 0 {
		return nil
	}
	filled := map[string]string{}
	parts := strings.FieldsFunc(strings.TrimSpace(input), func(r rune) bool {
		return r == ',' || r == '，' || r == '\n' || r == ';' || r == '；'
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
	return base + "\n已补充参数：" + strings.Join(parts, "；")
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
