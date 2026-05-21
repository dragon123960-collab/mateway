package followup

import (
	"strings"
	"unicode/utf8"

	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/textmatch"
)

type Input struct {
	CurrentMessage string
	RecentTurns    []session.Turn
	LastTask       *session.TaskState
}

type Decision struct {
	IsFollowup    bool    `json:"is_followup"`
	ResolvedQuery string  `json:"resolved_query"`
	Topic         string  `json:"topic"`
	Reason        string  `json:"reason"`
	Confidence    float64 `json:"confidence"`
}

type Resolver struct{}

func (Resolver) Resolve(in Input) Decision {
	current := strings.TrimSpace(in.CurrentMessage)
	base := baseQuery(in.LastTask)
	topic := strings.TrimSpace(lastTaskTopic(in.LastTask))
	if current == "" {
		return Decision{}
	}
	if strings.TrimSpace(base) == "" {
		return Decision{ResolvedQuery: current, Topic: topic}
	}

	normalized := normalize(current)
	if isExplicitNewTopic(normalized) {
		return Decision{
			IsFollowup:    false,
			ResolvedQuery: current,
			Topic:         "",
			Reason:        "user explicitly started a new topic",
			Confidence:    0.94,
		}
	}

	if decision, ok := resolveByIntent(current, normalized, base, topic); ok {
		return decision
	}

	if looksContextDependent(normalized) {
		return Decision{
			IsFollowup:    true,
			ResolvedQuery: mergeBaseAndInstruction(base, current),
			Topic:         topic,
			Reason:        "current input depends on previous context",
			Confidence:    0.73,
		}
	}

	return Decision{
		IsFollowup:    false,
		ResolvedQuery: current,
		Topic:         "",
		Reason:        "current input is standalone",
		Confidence:    0.78,
	}
}

func resolveByIntent(current, normalized, base, topic string) (Decision, bool) {
	switch {
	case isContinueIntent(normalized):
		return Decision{
			IsFollowup:    true,
			ResolvedQuery: base,
			Topic:         topic,
			Reason:        "user asked to continue the previous task",
			Confidence:    0.95,
		}, true
	case isRetryIntent(normalized):
		return Decision{
			IsFollowup:    true,
			ResolvedQuery: "Retry the previous task:\n" + base,
			Topic:         topic,
			Reason:        "user asked to retry the previous task",
			Confidence:    0.94,
		}, true
	case isExpandIntent(normalized):
		return Decision{
			IsFollowup:    true,
			ResolvedQuery: "Continue the previous task and expand in this direction:\nOriginal task: " + base + "\nAdditional request: " + current,
			Topic:         topic,
			Reason:        "user asked to expand from the previous result",
			Confidence:    0.91,
		}, true
	case looksReferenceEdit(normalized):
		return Decision{
			IsFollowup:    true,
			ResolvedQuery: mergeBaseAndInstruction(base, current),
			Topic:         topic,
			Reason:        "user referenced a previous object and added an edit request",
			Confidence:    0.88,
		}, true
	}
	return Decision{}, false
}

func mergeBaseAndInstruction(base, current string) string {
	base = strings.TrimSpace(base)
	current = strings.TrimSpace(current)
	if base == "" {
		return current
	}
	if current == "" {
		return base
	}
	return "Continue the previous task:\nOriginal task: " + base + "\nAdditional request: " + current
}

func baseQuery(last *session.TaskState) string {
	if last == nil {
		return ""
	}
	for _, value := range []string{last.ResolvedQuery, last.UserText} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func lastTaskTopic(last *session.TaskState) string {
	if last == nil {
		return ""
	}
	for _, value := range []string{last.Topic, last.PlanSummary} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func normalize(in string) string {
	replacer := strings.NewReplacer("\uFF0C", "", "\u3002", "", "\uFF1F", "", "\uFF01", "", "\uFF1A", "", "\uFF1B", "", "\n", " ", "\t", " ")
	return strings.ToLower(strings.TrimSpace(replacer.Replace(in)))
}

func isExplicitNewTopic(text string) bool {
	return textmatch.ContainsGroup(text, "explicit_new_topic")
}

func isContinueIntent(text string) bool {
	return textmatch.ExactGroup(text, "continue_exact") || textmatch.ContainsGroup(text, "continue_contains")
}

func isRetryIntent(text string) bool {
	return textmatch.ContainsGroup(text, "retry_contains")
}

func isExpandIntent(text string) bool {
	return textmatch.ContainsGroup(text, "expand_cues")
}

func looksReferenceEdit(text string) bool {
	return textmatch.ContainsGroup(text, "reference_edit_cues")
}

func looksContextDependent(text string) bool {
	if utf8.RuneCountInString(text) <= 8 {
		if textmatch.ContainsGroup(text, "short_context_cues") {
			return true
		}
	}
	return textmatch.ContainsGroup(text, "context_contains")
}
