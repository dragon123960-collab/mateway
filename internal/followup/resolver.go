package followup

import (
	"strings"
	"unicode/utf8"

	"github.com/dongping/mateway/internal/session"
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
			Reason:        "用户明确开启新话题",
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
			Reason:        "当前输入依赖上一轮上下文才能理解",
			Confidence:    0.73,
		}
	}

	return Decision{
		IsFollowup:    false,
		ResolvedQuery: current,
		Topic:         "",
		Reason:        "当前输入可独立理解",
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
			Reason:        "用户要求继续上一轮任务",
			Confidence:    0.95,
		}, true
	case isRetryIntent(normalized):
		return Decision{
			IsFollowup:    true,
			ResolvedQuery: "重新执行刚才的任务：\n" + base,
			Topic:         topic,
			Reason:        "用户要求重试上一轮任务",
			Confidence:    0.94,
		}, true
	case isExpandIntent(normalized):
		return Decision{
			IsFollowup:    true,
			ResolvedQuery: "继续上一轮任务，并展开说明以下方向：\n原任务：" + base + "\n补充要求：" + current,
			Topic:         topic,
			Reason:        "用户要求基于上一轮结果继续展开",
			Confidence:    0.91,
		}, true
	case looksReferenceEdit(normalized):
		return Decision{
			IsFollowup:    true,
			ResolvedQuery: mergeBaseAndInstruction(base, current),
			Topic:         topic,
			Reason:        "用户在引用上一轮对象追加修改要求",
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
	return "继续处理上一轮任务：\n原任务：" + base + "\n补充要求：" + current
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
	replacer := strings.NewReplacer("，", "", "。", "", "？", "", "！", "", "：", "", "；", "", "\n", " ", "\t", " ")
	return strings.ToLower(strings.TrimSpace(replacer.Replace(in)))
}

func isExplicitNewTopic(text string) bool {
	cues := []string{"换个话题", "新问题", "另一个问题", "另外一个问题", "重新问", "顺便问个新问题"}
	for _, cue := range cues {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

func isContinueIntent(text string) bool {
	exact := []string{"继续", "继续吧", "接着", "接着做", "继续刚才的", "继续刚才那个", "继续上一个", "继续上一轮", "继续处理"}
	for _, item := range exact {
		if text == item {
			return true
		}
	}
	return strings.Contains(text, "继续刚才") || strings.Contains(text, "继续上一轮") || strings.Contains(text, "接着刚才")
}

func isRetryIntent(text string) bool {
	return strings.Contains(text, "再试一次") || strings.Contains(text, "重试") || strings.Contains(text, "重新执行刚才")
}

func isExpandIntent(text string) bool {
	cues := []string{"展开", "详细", "细说", "具体一点", "展开讲", "再多说", "深入一点"}
	for _, cue := range cues {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

func looksReferenceEdit(text string) bool {
	cues := []string{"按刚才", "按上面", "基于刚才", "这个文件", "那个文件", "这个结果", "那个结果", "刚才那个文件", "前面那个"}
	for _, cue := range cues {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

func looksContextDependent(text string) bool {
	if utf8.RuneCountInString(text) <= 8 {
		shortCues := []string{"这个", "那个", "它", "继续", "接着", "展开", "重试", "再来", "按这个", "按那个"}
		for _, cue := range shortCues {
			if strings.Contains(text, cue) {
				return true
			}
		}
	}
	return strings.Contains(text, "刚才") || strings.Contains(text, "上一个") || strings.Contains(text, "前面")
}
