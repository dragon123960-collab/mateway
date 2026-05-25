package skill

import (
	"sort"
	"strings"
)

const (
	StagePlanning  = "planning"
	StageSynthesis = "synthesis"
	StageUniversal = "universal"
)

var stageSelectionBudget = map[string]int{
	StagePlanning:  2,
	StageSynthesis: 3,
}

type Match struct {
	Definition Definition
	Reason     string
}

func Select(defs []Definition, stage string, ctx Context) []Definition {
	matches := SelectMatches(defs, stage, ctx)
	selected := make([]Definition, 0, len(matches))
	for _, match := range matches {
		selected = append(selected, match.Definition)
	}
	return selected
}

func SelectMatches(defs []Definition, stage string, ctx Context) []Match {
	selected := make([]Match, 0, len(defs))
	for _, def := range defs {
		if !matchesStage(def, stage) {
			continue
		}
		reason, ok := matchReason(def, ctx)
		if !ok {
			continue
		}
		selected = append(selected, Match{Definition: def, Reason: reason})
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].Definition.Priority != selected[j].Definition.Priority {
			return selected[i].Definition.Priority > selected[j].Definition.Priority
		}
		return selected[i].Definition.Name < selected[j].Definition.Name
	})
	selected = dedupeByScope(selected)
	budget := selectionBudgetForStage(stage)
	if len(selected) > budget {
		selected = selected[:budget]
	}
	return selected
}

func dedupeByScope(matches []Match) []Match {
	out := make([]Match, 0, len(matches))
	seen := map[string]struct{}{}
	for _, match := range matches {
		scope := normalizedScope(match.Definition)
		if scope != "" {
			if _, ok := seen[scope]; ok {
				continue
			}
			seen[scope] = struct{}{}
		}
		out = append(out, match)
	}
	return out
}

func normalizedScope(def Definition) string {
	scope := strings.TrimSpace(strings.ToLower(def.Scope))
	if scope == "" {
		return ""
	}
	return scope
}

func selectionBudgetForStage(stage string) int {
	stage = strings.TrimSpace(stage)
	if budget, ok := stageSelectionBudget[stage]; ok && budget > 0 {
		return budget
	}
	return 3
}

func matchesStage(def Definition, stage string) bool {
	switch strings.TrimSpace(def.Stage) {
	case "", StageUniversal:
		return true
	default:
		return strings.EqualFold(def.Stage, stage)
	}
}

func matchesContext(def Definition, ctx Context) bool {
	_, ok := matchReason(def, ctx)
	return ok
}

func matchReason(def Definition, ctx Context) (string, bool) {
	reasons := make([]string, 0, 3)
	if lang := strings.TrimSpace(def.WhenUserLanguage); lang != "" {
		if strings.EqualFold(lang, "zh-CN") && !looksChinese(ctx.UserText) {
			return "", false
		}
		reasons = append(reasons, "user_language="+lang)
	}
	if len(def.WhenContains) > 0 && !containsAnyForDefinition(ctx.UserText, def.WhenContains, def) {
		return "", false
	} else if len(def.WhenContains) > 0 {
		reasons = append(reasons, "when_contains")
	}
	if len(def.WhenResultKinds) > 0 && !hasResultKind(ctx.Results, def.WhenResultKinds) {
		return "", false
	} else if len(def.WhenResultKinds) > 0 {
		reasons = append(reasons, "when_result_kinds")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "stage="+def.Stage)
	}
	return strings.Join(reasons, ","), true
}

func containsAny(text string, patterns []string) bool {
	text = strings.ToLower(text)
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern != "" && strings.Contains(text, pattern) {
			return true
		}
	}
	return false
}

func containsAnyForDefinition(text string, patterns []string, def Definition) bool {
	for _, pattern := range patterns {
		if !containsAny(text, []string{pattern}) {
			continue
		}
		if suppressFreshSearchMatch(text, pattern, def) {
			continue
		}
		return true
	}
	return false
}

func suppressFreshSearchMatch(text, pattern string, def Definition) bool {
	if !isFreshSearchDefinition(def) {
		return false
	}
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	switch pattern {
	case "当前", "current", "now":
	default:
		return false
	}
	return mentionsLocalContext(text) && !mentionsFreshWebContext(text)
}

func isFreshSearchDefinition(def Definition) bool {
	name := strings.ToLower(strings.TrimSpace(def.Name))
	scope := strings.ToLower(strings.TrimSpace(def.Scope))
	return name == "fresh-search" || scope == "search-planning"
}

func mentionsLocalContext(text string) bool {
	text = strings.ToLower(text)
	phrases := []string{
		"当前项目", "当前仓库", "当前目录", "当前文件", "当前代码", "当前工程",
		"本项目", "本仓库", "这个项目", "这个仓库",
		"current project", "current repo", "current repository", "current directory", "current file", "current codebase",
	}
	for _, phrase := range phrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func mentionsFreshWebContext(text string) bool {
	text = strings.ToLower(text)
	phrases := []string{
		"最新", "最近", "今日", "今天", "实时", "官方", "版本", "发布", "更新", "趋势", "价格", "政策",
		"latest", "recent", "today", "official", "version", "release", "changelog", "pricing", "policy", "trend",
	}
	for _, phrase := range phrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

func hasResultKind(results []ResultRef, kinds []string) bool {
	if len(results) == 0 {
		return false
	}
	allowed := map[string]struct{}{}
	for _, kind := range kinds {
		kind = strings.TrimSpace(kind)
		if kind != "" {
			allowed[kind] = struct{}{}
		}
	}
	for _, result := range results {
		if _, ok := allowed[strings.TrimSpace(result.Kind)]; ok {
			return true
		}
	}
	return false
}

func PromptBlock(defs []Definition) string {
	if len(defs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Selected skills:\n")
	for _, def := range defs {
		b.WriteString("- ")
		b.WriteString(def.Name)
		if def.Description != "" {
			b.WriteString(": ")
			b.WriteString(def.Description)
		}
		b.WriteString("\n")
		if len(def.UseFor) > 0 {
			b.WriteString("  use_for: ")
			b.WriteString(strings.Join(def.UseFor, ", "))
			b.WriteString("\n")
		}
		if len(def.Produces) > 0 {
			b.WriteString("  produces: ")
			b.WriteString(strings.Join(def.Produces, ", "))
			b.WriteString("\n")
		}
		if strings.TrimSpace(def.AcceptanceMode) != "" {
			b.WriteString("  acceptance_mode: ")
			b.WriteString(def.AcceptanceMode)
			b.WriteString("\n")
		}
		if strings.TrimSpace(def.ParallelMode) != "" {
			b.WriteString("  parallel_mode: ")
			b.WriteString(def.ParallelMode)
			b.WriteString("\n")
		}
		if strings.TrimSpace(def.AcceptancePrompt) != "" {
			b.WriteString("  acceptance_prompt: ")
			b.WriteString(def.AcceptancePrompt)
			b.WriteString("\n")
		}
		if strings.TrimSpace(def.Instruction) != "" {
			b.WriteString(def.Instruction)
			b.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(b.String())
}
