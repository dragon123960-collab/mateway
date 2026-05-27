package runtime

import (
	"sort"
	"strings"

	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/tool"
)

const (
	planningCandidateBudget = 5
	repairCandidateBudget   = 8
)

func composeCandidateTools(defs []tool.Definition, understanding taskUnderstanding, budget int, extraHints ...string) []tool.Definition {
	if budget <= 0 {
		budget = planningCandidateBudget
	}
	if len(defs) <= budget {
		return defs
	}
	type candidate struct {
		def   tool.Definition
		score int
	}
	user := normalizeComposeText(strings.TrimSpace(strings.Join(append([]string{understanding.Goal}, extraHints...), " ")))
	scored := make([]candidate, 0, len(defs))
	for _, def := range defs {
		score := toolMatchScore(def, user, understanding)
		if score > 0 {
			scored = append(scored, candidate{def: def, score: score})
		}
	}
	if len(scored) == 0 {
		return defs
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].def.Name < scored[j].def.Name
	})
	limit := budget
	if len(scored) < limit {
		limit = len(scored)
	}
	out := make([]tool.Definition, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, scored[i].def)
	}
	return out
}

func renderRecommendedTools(recommended []tool.Definition) string {
	if len(recommended) == 0 {
		return ""
	}
	names := toolNames(recommended)
	if len(names) == 0 {
		return ""
	}
	return "Candidate tools for this request:\n" +
		"- Prefer these tools when they fit the task: " + strings.Join(names, ", ")
}

func repairCandidateHints(results []model.ToolResult, repairReason string) []string {
	hints := []string{}
	if strings.TrimSpace(repairReason) != "" {
		hints = append(hints, repairReason)
	}
	for _, result := range results {
		if result.OK {
			continue
		}
		text := strings.TrimSpace(result.Tool + " " + result.Error + " " + result.Output)
		if text != "" {
			hints = append(hints, text)
		}
	}
	return hints
}

func toolMatchScore(def tool.Definition, user string, understanding taskUnderstanding) int {
	score := 0
	for _, text := range []string{def.Name, def.Description, def.Metadata.Purpose} {
		score += weightedTokenOverlap(user, text, 5)
	}
	for _, text := range def.Metadata.WhenToUse {
		score += weightedTokenOverlap(user, text, 4)
	}
	for _, text := range def.Metadata.OutputContract {
		score += weightedTokenOverlap(user, text, 2)
	}
	for _, text := range def.Metadata.WhenNotToUse {
		score -= weightedTokenOverlap(user, text, 3)
	}
	for _, capability := range understanding.Capabilities {
		score += capabilityToolScore(capability, def.Name)
	}
	if understanding.NeedsGrounding && supportsGrounding(def.Name) {
		score += 3
	}
	if understanding.NeedsMutation && def.Risk != tool.RiskSafeRead {
		score += 2
	}
	return score
}

func weightedTokenOverlap(user, text string, weight int) int {
	user = normalizeComposeText(user)
	text = normalizeComposeText(text)
	if user == "" || text == "" {
		return 0
	}
	score := 0
	for _, token := range strings.Fields(text) {
		if len([]rune(token)) < 2 {
			continue
		}
		if strings.Contains(user, token) {
			score += weight
		}
	}
	return score
}

func normalizeComposeText(text string) string {
	text = strings.ToLower(strings.TrimSpace(text))
	replacer := strings.NewReplacer("，", " ", "。", " ", "：", " ", "；", " ", ",", " ", ".", " ", "/", " ", "-", " ", "_", " ", "\n", " ")
	return strings.Join(strings.Fields(replacer.Replace(text)), " ")
}

func inferCapabilities(user string) []string {
	text := normalizeComposeText(user)
	var caps []string
	maybeAdd := func(cap string, cues ...string) {
		for _, cue := range cues {
			if strings.Contains(text, cue) {
				caps = appendCapability(caps, cap)
				return
			}
		}
	}
	maybeAdd("install_software", "安装", "install")
	maybeAdd("search_web", "搜索", "search", "最新", "today", "current")
	maybeAdd("fresh_fact_lookup", "天气", "weather", "汇率", "exchange rate", "股价", "stock price", "比分", "score", "航班", "flight", "空气质量", "aqi", "温度")
	maybeAdd("search_memory", "记忆", "memory")
	maybeAdd("inspect_project", "项目", "repo", "仓库", "结构", "目录")
	maybeAdd("inspect_file", "文件", "readme", "文档", "总结", "读取", "内容")
	maybeAdd("write_file", "写", "修改", "patch", "编辑")
	maybeAdd("run_terminal", "命令", "终端", "日志", "测试", "build", "状态")
	maybeAdd("run_terminal", "本机", "cli", "dry run", "dry-run", "auth", "profile", "发送消息", "command")
	maybeAdd("schedule_task", "定时", "每天", "每周", "schedule", "提醒")
	if len(caps) == 0 {
		caps = append(caps, "generic_lookup")
	}
	return caps
}

func inferCompletionDraft(user string, capabilities []string) []string {
	out := []string{"complete the user's main request"}
	if requiresGroundingEvidence(user) {
		out = append(out, "support the answer with grounded evidence")
	}
	for _, capability := range capabilities {
		switch capability {
		case "install_software":
			out = append(out, "identify the install method and verify the install result")
		case "search_web":
			out = append(out, "return concrete search evidence or clearly say no results")
		case "fresh_fact_lookup":
			out = append(out, "return current fact values with time context, or clearly say the facts could not be obtained")
		case "inspect_project":
			out = append(out, "summarize the relevant project structure")
		case "inspect_file":
			out = append(out, "summarize the relevant file content")
		case "write_file":
			out = append(out, "produce or update the requested file artifact")
		case "schedule_task":
			out = append(out, "create or update the requested schedule with visible task evidence")
		}
	}
	return dedupeStrings(out)
}

func inferEvidenceHints(capabilities []string, user string) []string {
	var out []string
	if requiresGroundingEvidence(user) {
		out = append(out, "prefer explicit file path, line range, URL, query, or verification evidence")
	}
	for _, capability := range capabilities {
		switch capability {
		case "install_software":
			out = append(out, "install command and verify command output")
		case "search_web":
			out = append(out, "search query, provider, and result count")
		case "fresh_fact_lookup":
			out = append(out, "fresh/current query wording, provider used, result count, and concrete current fact fields")
		case "inspect_project":
			out = append(out, "project path, file count, and sample tree")
		case "inspect_file":
			out = append(out, "file path, headings, and preview lines")
		case "write_file":
			out = append(out, "target file path and bytes written or patch evidence")
		case "schedule_task":
			out = append(out, "schedule task id, status, and saved path")
		case "run_terminal":
			out = append(out, "exit code, stdout, stderr, and timed_out flag")
		}
	}
	return dedupeStrings(out)
}

func inferRiskLevel(capabilities []string, user string) string {
	if requiresMutationCapability(capabilities) {
		if strings.Contains(normalizeComposeText(user), "删除") || strings.Contains(normalizeComposeText(user), "delete") {
			return "dangerous_execute"
		}
		return "guarded_mutation"
	}
	return "safe_read"
}

func appendCapability(caps []string, cap string) []string {
	for _, existing := range caps {
		if existing == cap {
			return caps
		}
	}
	return append(caps, cap)
}

func dedupeStrings(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func capabilityToolScore(capability, toolName string) int {
	switch capability {
	case "install_software":
		switch toolName {
		case "software.search":
			return 8
		case "software.install":
			return 10
		}
	case "search_web":
		switch toolName {
		case "web.search", "web.fetch", "software.search", "skill.search":
			return 8
		}
	case "fresh_fact_lookup":
		switch toolName {
		case "web.search", "web.fetch":
			return 10
		}
	case "search_memory":
		switch toolName {
		case "memory.search", "memory.index":
			return 8
		}
	case "inspect_project":
		switch toolName {
		case "project.index", "file.summary", "file.read":
			return 8
		}
	case "inspect_file":
		switch toolName {
		case "file.summary", "file.read", "project.index":
			return 8
		}
	case "write_file":
		switch toolName {
		case "file.write", "file.patch":
			return 10
		}
	case "run_terminal":
		switch toolName {
		case "terminal.run", "config.summary":
			return 8
		}
	case "schedule_task":
		if strings.HasPrefix(toolName, "schedule.") {
			return 10
		}
	case "generic_lookup":
		switch toolName {
		case "web.search", "web.fetch", "file.summary", "project.index":
			return 4
		}
	}
	return 0
}

func requiresMutationCapability(capabilities []string) bool {
	for _, capability := range capabilities {
		switch capability {
		case "install_software", "write_file", "schedule_task":
			return true
		}
	}
	return false
}

func supportsGrounding(toolName string) bool {
	switch toolName {
	case "file.read", "file.summary", "project.index", "web.search", "web.fetch", "software.search", "skill.search", "software.install", "skill.install", "terminal.run", "memory.search", "memory.index":
		return true
	default:
		return false
	}
}

func toolNames(defs []tool.Definition) []string {
	out := make([]string, 0, len(defs))
	for _, def := range defs {
		out = append(out, def.Name)
	}
	return out
}

func collectConstraints(user string) []string {
	user = strings.ToLower(user)
	var out []string
	for _, cue := range []string{"today", "latest", "current", "不要", "only", "just"} {
		if strings.Contains(user, cue) {
			out = append(out, cue)
		}
	}
	return out
}
