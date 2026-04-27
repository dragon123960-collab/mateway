package harness

import (
	"sort"
	"strings"
	"unicode"

	"github.com/dongping/mateway/internal/tools"
)

func progressiveToolDisclosure(goal string, list []tools.Tool) map[string]bool {
	if len(list) <= 8 {
		return nil
	}
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return nil
	}
	query := normalizeToolSelectionText(goal)
	allowed := map[string]bool{}
	alwaysAllowTools(allowed)
	scored := make([]scoredTool, 0, len(list))
	for _, item := range list {
		spec := item.Spec()
		score := scoreToolForGoal(query, spec)
		scored = append(scored, scoredTool{spec: spec, score: score})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].spec.Name < scored[j].spec.Name
		}
		return scored[i].score > scored[j].score
	})
	kindCount := map[tools.Kind]int{}
	for _, entry := range scored {
		if entry.score <= 0 {
			continue
		}
		if shouldSkipByKindQuota(entry.spec, kindCount) {
			continue
		}
		allowed[entry.spec.Name] = true
		kindCount[entry.spec.Kind]++
		if len(allowed) >= 10 {
			break
		}
	}
	if len(allowed) == 0 {
		return nil
	}
	return allowed
}

type scoredTool struct {
	spec  tools.Spec
	score int
}

func alwaysAllowTools(allowed map[string]bool) {
	for _, name := range []string{
		"read_memory",
		"read_skill_resource",
		"read_session_summary",
		"recall_last_task",
		"search_history",
		"search_scoped_memory",
		"wiki_query",
	} {
		allowed[name] = true
	}
}

func shouldSkipByKindQuota(spec tools.Spec, kindCount map[tools.Kind]int) bool {
	switch spec.Kind {
	case tools.KindCLI:
		return kindCount[spec.Kind] >= 2
	case tools.KindSkill:
		return kindCount[spec.Kind] >= 3
	default:
		return kindCount[spec.Kind] >= 5
	}
}

func scoreToolForGoal(goal string, spec tools.Spec) int {
	searchBlob := normalizeToolSelectionText(strings.Join(append([]string{
		spec.Name,
		spec.Description,
		string(spec.Kind),
		spec.RiskLevel,
	}, spec.Tags...), " "))
	score := 0
	executionGoal := goalLooksLikeExecution(goal)
	envBoundCLIGoal := goalSuggestsEnvironmentBoundCLI(goal)
	if strings.Contains(searchBlob, "memory") || strings.Contains(searchBlob, "wiki") || strings.Contains(searchBlob, "history") {
		score += 2
	}
	for _, token := range extractToolSelectionTokens(goal) {
		switch {
		case strings.Contains(searchBlob, token):
			score += 6
		case fuzzyTokenMatch(searchBlob, token):
			score += 3
		}
	}
	for _, group := range semanticGoalGroups(goal) {
		if strings.Contains(searchBlob, group) {
			score += 5
		}
	}
	switch spec.Kind {
	case tools.KindBuiltin:
		score += 1
	case tools.KindSkill:
		score += 2
	case tools.KindCLI:
		if strings.Contains(goal, "cli") || strings.Contains(goal, "命令") {
			score += 3
		}
		if envBoundCLIGoal {
			score += 5
		}
	}
	if strings.Contains(searchBlob, "web_search") || strings.Contains(searchBlob, "browser_fetch") {
		if containsAny(goal, "调研", "研究", "趋势", "分析", "news", "research", "trend", "market", "fund", "基金") {
			score += 8
		}
	}
	if strings.Contains(searchBlob, "exec") && executionGoal {
		score += 4
	}
	if spec.Name == "exec" && envBoundCLIGoal {
		score += 10
	}
	if strings.Contains(searchBlob, "sandbox_exec") && executionGoal {
		if envBoundCLIGoal {
			score += 2
		} else {
			score += 8
		}
	}
	if strings.Contains(searchBlob, "create_agent") || strings.Contains(searchBlob, "create_workspace") {
		if containsAny(goal, "agent", "workspace", "工作区", "创建", "新建", "channel") {
			score += 8
		}
	}
	if strings.Contains(searchBlob, "schedule") || strings.Contains(searchBlob, "cron") || strings.Contains(searchBlob, "automation") {
		if containsAny(goal, "定时", "提醒", "每天", "每日", "每周", "cron", "schedule", "timer", "remind", "follow up", "自动执行", "自动运行") {
			score += 10
		}
	}
	return score
}

func normalizeToolSelectionText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", " ")
	value = strings.ReplaceAll(value, "_", " ")
	return value
}

func extractToolSelectionTokens(goal string) []string {
	fields := strings.FieldsFunc(goal, func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == '，' || r == '.' || r == '。' || r == ':' || r == '：' || r == ';' || r == '；' || r == '(' || r == ')' || r == '（' || r == '）'
	})
	out := make([]string, 0, len(fields))
	seen := map[string]bool{}
	for _, field := range fields {
		field = normalizeToolSelectionText(field)
		if len(field) < 2 || seen[field] {
			continue
		}
		seen[field] = true
		out = append(out, field)
	}
	return out
}

func semanticGoalGroups(goal string) []string {
	var out []string
	add := func(groups ...string) {
		out = append(out, groups...)
	}
	switch {
	case containsAny(goal, "调研", "研究", "趋势", "分析", "总结", "对比", "research", "analy", "compare", "trend", "fund", "基金", "市场"):
		add("search", "web", "browser", "wiki")
	case containsAny(goal, "文件", "代码", "项目", "workspace", "repo", "仓库"):
		add("filesystem", "file", "workspace")
	case goalLooksLikeExecution(goal):
		add("exec", "sandbox", "cli")
	case containsAny(goal, "定时", "提醒", "每天", "每日", "每周", "cron", "schedule", "timer", "remind", "自动执行", "自动运行"):
		add("schedule", "cron", "automation", "reminder")
	case containsAny(goal, "创建agent", "新建agent", "agent", "workspace", "channel", "创建工作区"):
		add("agent", "workspace")
	}
	return out
}

func fuzzyTokenMatch(searchBlob, token string) bool {
	if len(token) < 3 {
		return false
	}
	return strings.Contains(searchBlob, token[:len(token)-1])
}

func containsAny(text string, items ...string) bool {
	for _, item := range items {
		if strings.Contains(text, strings.ToLower(strings.TrimSpace(item))) {
			return true
		}
	}
	return false
}

func goalLooksLikeExecution(goal string) bool {
	return containsAny(goal, "执行", "验证", "测试", "命令", "shell", "cli", "run", "test")
}

func goalSuggestsEnvironmentBoundCLI(goal string) bool {
	if !containsAny(goal, "cli", "命令", "shell", "终端", "terminal", "zsh", "bash", "opencli", "gh", "obsidian") {
		return false
	}
	return containsAny(goal,
		"opencli",
		"gh",
		"obsidian",
		"cookie",
		"cookies",
		"浏览器",
		"browser",
		"登录",
		"login",
		"session",
		"会话",
		"daemon",
		"桌面",
		"desktop",
		"终端",
		"terminal",
		"zsh",
		"bash",
		"本地",
		"本机",
		"用户目录",
		"home",
		"~/.",
	)
}
