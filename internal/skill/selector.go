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
	if len(def.WhenContains) > 0 && !containsAny(ctx.UserText, def.WhenContains) {
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
		if strings.TrimSpace(def.Instruction) != "" {
			b.WriteString(def.Instruction)
			b.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(b.String())
}
