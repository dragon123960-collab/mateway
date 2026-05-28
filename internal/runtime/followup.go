package runtime

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

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
	current := strings.TrimSpace(text)
	if current == "" {
		return followupDecision{Kind: followupNewTask, ResolvedUserText: current, Reason: "empty input starts a new task"}
	}
	normalized := normalizeFollowupText(current)
	if isExplicitNewTask(normalized) {
		return followupDecision{Kind: followupNewTask, ResolvedUserText: current, Reason: "explicit new task cue"}
	}
	if task := latestOpenTask(state); task != nil {
		if isFollowupCue(normalized) || isShortContextDependent(normalized) {
			return continueTask(*task, current, "active task followup cue")
		}
	}
	if ref, ok := ordinalTaskReference(normalized); ok {
		task := taskByOrdinal(state, ref)
		if task == nil {
			return clarify(current, "ordinal task reference did not match an existing task")
		}
		return continueTask(*task, current, "historical ordinal task reference")
	}
	if isHistoricalCue(normalized) {
		candidates := historicalCandidates(state, current)
		switch len(candidates) {
		case 0:
			return clarify(current, "historical cue had no candidate task")
		case 1:
			return continueTask(candidates[0], current, "single historical task candidate")
		default:
			return clarify(current, "historical cue matched multiple candidate tasks")
		}
	}
	if isFollowupCue(normalized) {
		if task := latestTask(state); task != nil {
			return continueTask(*task, current, "recent task followup cue")
		}
		return clarify(current, "followup cue had no prior task")
	}
	if isRetryCue(normalized) {
		if task := latestTask(state); task != nil {
			return continueTask(*task, current, "retry recent task")
		}
		return clarify(current, "retry cue had no prior task")
	}
	if isShortContextDependent(normalized) {
		if task := latestTask(state); task != nil {
			return continueTask(*task, current, "short context-dependent followup")
		}
		return clarify(current, "short context-dependent input had no prior task")
	}
	return followupDecision{Kind: followupNewTask, ResolvedUserText: current, Reason: "standalone input"}
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
		ClarifyPrompt:    "我还不能稳定判断你要接哪一个历史任务。请补充更明确的线索，比如第几个任务、主题、文件名或关键词。",
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

func taskByOrdinal(state session.State, ordinal int) *session.TaskNode {
	if ordinal <= 0 || ordinal > len(state.Tasks) {
		return nil
	}
	return &state.Tasks[ordinal-1]
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
	cues := []string{"新任务", "另一个任务", "换个话题", "重新开始", "不用接上", "不要接上", "start a new task", "new task"}
	for _, cue := range cues {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

func isFollowupCue(text string) bool {
	cues := []string{"继续", "接着", "再", "补充", "扩展", "改成", "换成", "上一个", "上一条", "刚才", "那个", "继续上面", "continue", "expand", "same task"}
	for _, cue := range cues {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

func isHistoricalCue(text string) bool {
	cues := []string{"历史", "之前", "前面", "回到", "那个任务", "那件事", "刚才那个", "previous task", "earlier task"}
	for _, cue := range cues {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

func isRetryCue(text string) bool {
	cues := []string{"重试", "再试", "再来一次", "重新试", "retry", "try again"}
	for _, cue := range cues {
		if strings.Contains(text, cue) {
			return true
		}
	}
	return false
}

func isShortContextDependent(text string) bool {
	if utf8.RuneCountInString(text) > 10 {
		return false
	}
	if isFollowupCue(text) {
		return true
	}
	return strings.HasSuffix(text, "呢") ||
		strings.HasSuffix(text, "吗") ||
		strings.HasSuffix(text, "么") ||
		strings.HasSuffix(text, "如何") ||
		strings.HasSuffix(text, "怎么样")
}

var ordinalPatterns = []struct {
	re     *regexp.Regexp
	lookup map[string]int
}{
	{regexp.MustCompile(`第\s*([一二三四五六七八九十0-9]+)\s*(个|条|件)?\s*(任务|问题|请求)?`), nil},
}

func ordinalTaskReference(text string) (int, bool) {
	for _, pattern := range ordinalPatterns {
		matches := pattern.re.FindStringSubmatch(text)
		if len(matches) < 2 {
			continue
		}
		if n, ok := parseOrdinal(matches[1]); ok {
			return n, true
		}
	}
	return 0, false
}

func parseOrdinal(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if value[0] >= '0' && value[0] <= '9' {
		var n int
		if _, err := fmt.Sscanf(value, "%d", &n); err == nil && n > 0 {
			return n, true
		}
	}
	switch value {
	case "一":
		return 1, true
	case "二":
		return 2, true
	case "三":
		return 3, true
	case "四":
		return 4, true
	case "五":
		return 5, true
	case "六":
		return 6, true
	case "七":
		return 7, true
	case "八":
		return 8, true
	case "九":
		return 9, true
	case "十":
		return 10, true
	default:
		return 0, false
	}
}
