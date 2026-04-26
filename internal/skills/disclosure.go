package skills

import (
	"sort"
	"strings"
	"unicode"
)

const defaultDisclosureLimit = 3

func FilterVisible(list []Skill, visibleNames []string) []Skill {
	if len(list) == 0 || len(visibleNames) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(visibleNames))
	for _, name := range visibleNames {
		name = strings.TrimSpace(name)
		if name != "" {
			allowed[name] = true
		}
	}
	out := make([]Skill, 0, len(list))
	for _, skill := range list {
		if allowed[skill.Manifest.Name] {
			out = append(out, skill)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Manifest.Name < out[j].Manifest.Name
	})
	return out
}

func ProgressiveDisclosure(goal string, list []Skill, limit int) []Skill {
	if len(list) == 0 {
		return nil
	}
	goal = normalizeSkillSelectionText(goal)
	if goal == "" {
		return nil
	}
	if limit <= 0 {
		limit = defaultDisclosureLimit
	}
	scored := make([]scoredSkill, 0, len(list))
	for _, skill := range list {
		scored = append(scored, scoredSkill{
			skill: skill,
			score: scoreSkillForGoal(goal, skill),
		})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].skill.Manifest.Name < scored[j].skill.Manifest.Name
		}
		return scored[i].score > scored[j].score
	})
	out := make([]Skill, 0, min(limit, len(scored)))
	for _, entry := range scored {
		if entry.score <= 0 {
			continue
		}
		out = append(out, entry.skill)
		if len(out) >= limit {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type scoredSkill struct {
	skill Skill
	score int
}

func scoreSkillForGoal(goal string, skill Skill) int {
	keywords := metadataKeywords(skill.Manifest.Metadata)
	searchBlob := normalizeSkillSelectionText(strings.Join(append([]string{
		skill.Manifest.Name,
		skill.Manifest.Description,
		skill.Manifest.Homepage,
		skill.Manifest.License,
		skill.Manifest.Compatibility,
		string(skill.Manifest.Type),
		strings.Join(skill.Manifest.Tags, " "),
		strings.Join(skill.Manifest.AllowedTools, " "),
		strings.Join(keywords, " "),
		bodySnippet(skill.Body, 2400),
	}, keywords...), " "))
	score := 0
	name := normalizeSkillSelectionText(skill.Manifest.Name)
	if name != "" && strings.Contains(goal, name) {
		score += 12
	}
	for _, token := range extractSkillSelectionTokens(goal) {
		switch {
		case strings.Contains(searchBlob, token):
			score += 6
		case fuzzySkillTokenMatch(searchBlob, token):
			score += 3
		}
	}
	for _, group := range semanticSkillGroups(goal) {
		if strings.Contains(searchBlob, group) {
			score += 5
		}
	}
	if skill.Executable && containsAny(goal, "run", "执行", "调用", "api", "cli", "script", "tool") {
		score += 2
	}
	if !skill.Executable && containsAny(goal, "规范", "说明", "文档", "guide", "instruction", "workflow", "design", "plan") {
		score += 1
	}
	return score
}

func normalizeSkillSelectionText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", " ")
	value = strings.ReplaceAll(value, "_", " ")
	return value
}

func extractSkillSelectionTokens(goal string) []string {
	fields := strings.FieldsFunc(goal, func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == '，' || r == '.' || r == '。' || r == ':' || r == '：' || r == ';' || r == '；' || r == '(' || r == ')' || r == '（' || r == '）'
	})
	out := make([]string, 0, len(fields))
	seen := map[string]bool{}
	for _, field := range fields {
		field = normalizeSkillSelectionText(field)
		if len(field) < 2 || seen[field] {
			continue
		}
		seen[field] = true
		out = append(out, field)
	}
	return out
}

func semanticSkillGroups(goal string) []string {
	var out []string
	add := func(groups ...string) {
		out = append(out, groups...)
	}
	switch {
	case containsAny(goal, "前端", "页面", "组件", "ui", "ux", "landing", "react", "css", "design"):
		add("frontend", "design", "react", "landing page", "ui")
	case containsAny(goal, "文档", "报告", "合同", "proposal", "docx", "word", "pdf"):
		add("document", "docx", "pdf", "proposal", "report")
	case containsAny(goal, "表格", "excel", "spreadsheet", "csv", "财务"):
		add("spreadsheet", "excel", "xlsx", "table")
	case containsAny(goal, "ppt", "演示", "slides", "presentation", "deck"):
		add("presentation", "slides", "pptx")
	case containsAny(goal, "图片", "海报", "图标", "贴纸", "gif", "image", "icon"):
		add("image", "icon", "gif", "sticker", "banner")
	case containsAny(goal, "数据库", "sql", "schema", "table", "migration"):
		add("database", "schema", "sql", "table")
	case containsAny(goal, "obsidian", "笔记", "canvas", "base", "markdown"):
		add("obsidian", "markdown", "canvas", "base")
	case containsAny(goal, "skill", "技能", "agent", "workflow", "规范"):
		add("skill", "agent", "workflow", "instruction")
	}
	return out
}

func metadataKeywords(metadata map[string]any) []string {
	if len(metadata) == 0 {
		return nil
	}
	out := make([]string, 0, len(metadata))
	for key, value := range metadata {
		key = strings.TrimSpace(strings.ToLower(key))
		if key != "" {
			out = append(out, key)
		}
		switch v := value.(type) {
		case string:
			v = strings.TrimSpace(v)
			if v != "" {
				out = append(out, v)
			}
		case []any:
			for _, item := range v {
				s, _ := item.(string)
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

func bodySnippet(body string, max int) string {
	body = strings.TrimSpace(body)
	if max > 0 && len(body) > max {
		return body[:max]
	}
	return body
}

func fuzzySkillTokenMatch(searchBlob, token string) bool {
	if len(token) < 3 {
		return false
	}
	return strings.Contains(searchBlob, token[:len(token)-1])
}

func containsAny(text string, items ...string) bool {
	for _, item := range items {
		if strings.Contains(text, normalizeSkillSelectionText(item)) {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
