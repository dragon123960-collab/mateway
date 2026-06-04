package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/i18n"
	"github.com/dongping/mateway/internal/session"
)

type followupKind string

const (
	followupNewTask      followupKind = "new_task"
	followupContinuation followupKind = "continuation"
	followupClarify      followupKind = "clarify"
)

type followupDecision struct {
	Kind             followupKind
	TaskID           string
	ResolvedUserText string
	Reason           string
	ClarifyPrompt    string
}

func resolveFollowup(state session.State, text string) followupDecision {
	decision := protocolFollowupDecision(state, text)
	if decision.Kind != "" {
		return decision
	}
	return followupDecision{Kind: followupNewTask, ResolvedUserText: strings.TrimSpace(text), Reason: "standalone input"}
}

func fallbackFollowupDecision(state session.State, text, locale, catalogDir, reason string) followupDecision {
	current := strings.TrimSpace(text)
	normalized := normalizeFollowupText(current)
	cues := followupFallbackCues(locale, catalogDir)
	if isFollowupCueWithCues(normalized, cues.Followup) || containsCue(normalized, cues.Retry) || isShortContextDependentWithCues(normalized, cues) || isActionAckFollowup(current) {
		if task := latestOpenTask(state); task != nil {
			return continueTask(*task, current, followupDefaultString(reason, "safe fallback continuation"))
		}
		if task := latestTask(state); task != nil && task.Status != "completed" {
			return continueTask(*task, current, followupDefaultString(reason, "safe fallback continuation"))
		}
		if len(state.Tasks) > 0 {
			return clarify(current, followupDefaultString(reason, "context-dependent followup with no task candidate"))
		}
	}
	return followupDecision{Kind: followupNewTask, ResolvedUserText: current, Reason: followupDefaultString(reason, "standalone input")}
}

type followupCueSet struct {
	Followup    []string
	Retry       []string
	ShortSuffix []string
}

func followupFallbackCues(locale, catalogDir string) followupCueSet {
	catalog := i18n.New(i18n.Config{CatalogDir: catalogDir})
	return followupCueSet{
		Followup:    splitCatalogCSV(catalog.T(locale, "router.followup.cues.followup", nil)),
		Retry:       splitCatalogCSV(catalog.T(locale, "router.followup.cues.retry", nil)),
		ShortSuffix: splitCatalogCSV(catalog.T(locale, "router.followup.cues.short_suffix", nil)),
	}
}

func splitCatalogCSV(text string) []string {
	var out []string
	for _, item := range strings.Split(text, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func followupCueList(key string) []string {
	return splitCatalogCSV(i18n.New(i18n.Config{}).T(i18n.LocaleZH, key, nil))
}

func protocolFollowupDecision(_ session.State, text string) followupDecision {
	current := strings.TrimSpace(text)
	if current == "" {
		return followupDecision{Kind: followupNewTask, ResolvedUserText: current, Reason: "empty input starts a new task"}
	}
	if strings.HasPrefix(current, "/") {
		return followupDecision{Kind: followupNewTask, ResolvedUserText: current, Reason: "slash command starts a new task"}
	}
	return followupDecision{}
}

func isStaleWeakFollowup(text string, task session.TaskNode) bool {
	if time.Since(task.UpdatedAt) <= 6*time.Hour {
		return false
	}
	goal := normalizeFollowupText(task.Goal)
	if tokenOverlap(text, goal) > 0 {
		return false
	}
	for _, cue := range followupCueList("router.followup.cues.weak") {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

func continueTask(task session.TaskNode, current, reason string) followupDecision {
	return followupDecision{
		Kind:             followupContinuation,
		TaskID:           task.ID,
		ResolvedUserText: mergeTaskAndInstruction(task.Goal, current),
		Reason:           reason,
	}
}

func clarify(current, reason string) followupDecision {
	return followupDecision{
		Kind:             followupClarify,
		ResolvedUserText: current,
		Reason:           reason,
		ClarifyPrompt:    i18n.New(i18n.Config{}).T(i18n.LocaleZH, "router.followup.clarify", nil),
	}
}

func mergeTaskAndInstruction(goal, current string) string {
	goal = strings.TrimSpace(goal)
	current = strings.TrimSpace(current)
	if goal == "" {
		return current
	}
	return "Continue the existing task:\nOriginal task: " + goal + "\nAdditional request: " + current
}

func latestOpenTask(state session.State) *session.TaskNode {
	if state.ActiveTask != "" {
		for i := range state.Tasks {
			if state.Tasks[i].ID == state.ActiveTask && session.IsOpenTaskStatus(state.Tasks[i].Status) {
				return &state.Tasks[i]
			}
		}
	}
	for i := len(state.Tasks) - 1; i >= 0; i-- {
		if session.IsOpenTaskStatus(state.Tasks[i].Status) {
			return &state.Tasks[i]
		}
	}
	return nil
}

func latestTask(state session.State) *session.TaskNode {
	if len(state.Tasks) == 0 {
		return nil
	}
	return &state.Tasks[len(state.Tasks)-1]
}

func historicalCandidates(state session.State, text string) []session.TaskNode {
	normalized := normalizeFollowupText(text)
	var out []session.TaskNode
	for i := len(state.Tasks) - 1; i >= 0; i-- {
		task := state.Tasks[i]
		goal := normalizeFollowupText(task.Goal)
		if goal == "" {
			continue
		}
		if tokenOverlap(normalized, goal) >= 1 || strings.Contains(normalized, goal) || strings.Contains(goal, normalized) {
			out = append(out, task)
		}
	}
	return out
}

func tokenOverlap(a, b string) int {
	seen := map[string]bool{}
	for _, token := range strings.Fields(a) {
		if utf8.RuneCountInString(token) >= 2 {
			seen[token] = true
		}
	}
	count := 0
	for _, token := range strings.Fields(b) {
		if seen[token] {
			count++
		}
	}
	return count
}

func normalizeFollowupText(text string) string {
	replacer := strings.NewReplacer("，", " ", "。", " ", "？", " ", "！", " ", "：", " ", "；", " ", ",", " ", ".", " ", "?", " ", "!", " ", "\n", " ", "\t", " ")
	return strings.Join(strings.Fields(strings.ToLower(replacer.Replace(strings.TrimSpace(text)))), " ")
}

func isExplicitNewTask(text string) bool {
	for _, cue := range followupCueList("router.followup.cues.new_task") {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

func isFollowupCue(text string) bool {
	return isFollowupCueWithCues(text, followupCueList("router.followup.cues.followup"))
}

func isFollowupCueWithCues(text string, cues []string) bool {
	for _, cue := range cues {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

func isHistoricalCue(text string) bool {
	for _, cue := range followupCueList("router.followup.cues.historical") {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

func isRetryCue(text string) bool {
	return containsCue(text, followupCueList("router.followup.cues.retry"))
}

func containsCue(text string, cues []string) bool {
	for _, cue := range cues {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

func isShortContextDependent(text string) bool {
	return isShortContextDependentWithCues(text, followupCueSet{
		Followup:    followupCueList("router.followup.cues.followup"),
		ShortSuffix: followupCueList("router.followup.cues.short_suffix"),
	})
}

func isShortContextDependentWithCues(text string, cues followupCueSet) bool {
	if utf8.RuneCountInString(text) > 10 {
		return false
	}
	if isFollowupCueWithCues(text, cues.Followup) {
		return true
	}
	for _, suffix := range cues.ShortSuffix {
		if strings.HasSuffix(text, suffix) {
			return true
		}
	}
	return false
}

func modelFollowupPrompt(state session.State, text string) string {
	var b strings.Builder
	b.WriteString("Decide whether the current user message continues one of the recent tasks.\n")
	b.WriteString("Return JSON only with this schema: {\"kind\":\"new_task|continuation|clarify\",\"task_id\":\"\",\"reason\":\"\"}.\n")
	b.WriteString("Use continuation when the user asks to continue, finish remaining work, execute remaining steps, test, retry, or verify the immediately previous task.\n")
	b.WriteString("Use new_task for clearly unrelated standalone work. Use clarify only when multiple tasks match.\n\n")
	b.WriteString("Current user message:\n")
	b.WriteString(strings.TrimSpace(text))
	b.WriteString("\n\nRecent tasks, newest last:\n")
	start := len(state.Tasks) - 5
	if start < 0 {
		start = 0
	}
	for _, task := range state.Tasks[start:] {
		b.WriteString("- id: ")
		b.WriteString(task.ID)
		b.WriteString("\n  status: ")
		b.WriteString(task.Status)
		b.WriteString("\n  goal: ")
		b.WriteString(task.Goal)
		if task.Summary != "" {
			b.WriteString("\n  summary: ")
			b.WriteString(summarize(task.Summary))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func parseModelFollowupDecision(text string, state session.State, current string) (followupDecision, error) {
	raw := strings.TrimSpace(text)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	var payload struct {
		Kind   string `json:"kind"`
		TaskID string `json:"task_id"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return followupDecision{}, err
	}
	kind := strings.TrimSpace(payload.Kind)
	reason := strings.TrimSpace(payload.Reason)
	switch kind {
	case string(followupContinuation):
		task := taskByID(state, payload.TaskID)
		if task == nil {
			return followupDecision{}, fmt.Errorf("model followup task_id %q not found", payload.TaskID)
		}
		return continueTask(*task, current, followupDefaultString(reason, "model followup route")), nil
	case string(followupClarify):
		return clarify(current, followupDefaultString(reason, "model followup ambiguous")), nil
	case string(followupNewTask):
		return followupDecision{Kind: followupNewTask, ResolvedUserText: strings.TrimSpace(current), Reason: followupDefaultString(reason, "model followup new task")}, nil
	default:
		return followupDecision{}, fmt.Errorf("unsupported model followup kind %q", kind)
	}
}

type modelPendingIntentHookProvider struct{}

func (modelPendingIntentHookProvider) Name() string { return "model_pending_intent" }

func (modelPendingIntentHookProvider) PendingIntentHook(ctx context.Context, input PendingIntentInput) (pendingIntentDecision, error) {
	if input.Model == nil {
		return pendingIntentDecision{}, nil
	}
	if strings.TrimSpace(input.Text) == "" || input.Pending.Kind != "user_input" {
		return pendingIntentDecision{}, nil
	}
	msg, err := input.Model.Next(ctx, agentcore.Context{
		SystemPrompt: "You classify how a user message relates to a pending question. Return JSON only. Do not call tools.",
		Messages:     []agentcore.Message{{Role: agentcore.RoleUser, Content: modelPendingIntentPrompt(input)}},
		Tools:        nil,
	})
	if err != nil {
		return pendingIntentDecision{}, err
	}
	return parseModelPendingIntentDecision(msg.Content)
}

func modelPendingIntentPrompt(input PendingIntentInput) string {
	task := taskByID(input.State, input.Pending.TaskID)
	var b strings.Builder
	b.WriteString("Classify the current user message while a task is awaiting user input.\n")
	b.WriteString("Return JSON only: {\"kind\":\"answer_pending|action_ack|new_task|unclear\",\"reason\":\"\"}.\n")
	b.WriteString("Use answer_pending when the message supplies missing information or answers the pending question.\n")
	b.WriteString("Use action_ack when the user gives a short approval/continue signal for the pending task.\n")
	b.WriteString("Use new_task when the message is a standalone unrelated task.\n")
	b.WriteString("Use unclear only when it is ambiguous and should remain pending.\n\n")
	examples := strings.TrimSpace(i18n.New(i18n.Config{CatalogDir: input.CatalogDir}).T(input.Locale, "router.pending_intent.examples", nil))
	if examples != "" && examples != "router.pending_intent.examples" {
		b.WriteString("Examples:\n")
		b.WriteString(examples)
		b.WriteString("\n\n")
	}
	b.WriteString("Pending question:\n")
	b.WriteString(strings.TrimSpace(input.Pending.Question))
	b.WriteString("\n\nCurrent user message:\n")
	b.WriteString(strings.TrimSpace(input.Text))
	if task != nil {
		b.WriteString("\n\nPending task:\n- id: ")
		b.WriteString(task.ID)
		b.WriteString("\n- status: ")
		b.WriteString(task.Status)
		b.WriteString("\n- goal: ")
		b.WriteString(task.Goal)
		if task.Summary != "" {
			b.WriteString("\n- summary: ")
			b.WriteString(summarize(task.Summary))
		}
	}
	return b.String()
}

func parseModelPendingIntentDecision(text string) (pendingIntentDecision, error) {
	raw := strings.TrimSpace(text)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	var payload struct {
		Kind   string `json:"kind"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return pendingIntentDecision{}, err
	}
	kind := strings.TrimSpace(payload.Kind)
	switch kind {
	case "answer_pending", "action_ack", "new_task", "unclear":
		return pendingIntentDecision{Kind: kind, Reason: strings.TrimSpace(payload.Reason)}, nil
	default:
		return pendingIntentDecision{}, fmt.Errorf("unsupported pending intent kind %q", kind)
	}
}

func fallbackPendingIntentDecision(pending session.PendingAction, text, reason string) pendingIntentDecision {
	if shouldBypassUserInputPending(&pending, text) {
		return pendingIntentDecision{Kind: "new_task", Reason: followupDefaultString(reason, "standalone task request")}
	}
	if isActionAckFollowup(text) {
		return pendingIntentDecision{Kind: "action_ack", Reason: followupDefaultString(reason, "action acknowledgement")}
	}
	return pendingIntentDecision{Kind: "answer_pending", Reason: followupDefaultString(reason, "answer pending question")}
}

func taskByID(state session.State, id string) *session.TaskNode {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	for i := range state.Tasks {
		if state.Tasks[i].ID == id {
			return &state.Tasks[i]
		}
	}
	return nil
}

func followupDefaultString(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
