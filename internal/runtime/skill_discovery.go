package runtime

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dongping/mateway/internal/agentprofile"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/skill"
)

type discoveredSkill struct {
	Name        string
	Description string
	Stage       string
	Priority    string
	Aliases     []string
	WhenToUse   []string
	State       string
	Path        string
	Redacted    bool
}

func skillScope(path string) string {
	clean := filepath.ToSlash(path)
	if strings.Contains(clean, "/workspace/agents/") && strings.Contains(clean, "/skills/") {
		return "agent"
	}
	if strings.Contains(clean, "/workspace/skills/") {
		return "shared"
	}
	return ""
}

func discoverSkills(cfg *config.Root, limit int) []discoveredSkill {
	return discoverSkillsForAgent(cfg, "main", limit)
}

func discoverSkillsForAgent(cfg *config.Root, agentID string, limit int) []discoveredSkill {
	if cfg == nil {
		return nil
	}
	if limit <= 0 {
		limit = 12
	}
	workspace := strings.TrimSpace(cfg.App.Workspace)
	if workspace == "" {
		workspace = filepath.Join(cfg.App.Home, "workspace")
	}
	roots := skillRoots(workspace, agentID)
	var out []discoveredSkill
	seen := map[string]bool{}
	states := cleanupStates(cfg, workspace)
	for _, root := range roots {
		for _, item := range discoverSkillsInRoot(root) {
			key := strings.ToLower(item.Name)
			if seen[key] {
				continue
			}
			item.State = states[filepath.ToSlash(item.Path)]
			if item.State == skill.StateHidden {
				continue
			}
			seen[key] = true
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		left := skillPriority(out[i])
		right := skillPriority(out[j])
		if left != right {
			return left > right
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func contextSkillsForTask(cfg *config.Root, agentID, userText string, limit int) []discoveredSkill {
	if limit <= 0 {
		limit = 8
	}
	candidates := discoverSkillsForAgent(cfg, agentID, 24)
	if len(candidates) == 0 {
		return nil
	}
	queryTokens := skillQueryTokens(userText)
	queryText := strings.ToLower(userText)
	var matched []discoveredSkill
	for _, item := range candidates {
		if skillMatchesTask(item, queryText, queryTokens) {
			matched = append(matched, item)
		}
	}
	if len(matched) == 0 {
		matched = fallbackContextSkills(candidates, minInt(limit, 3))
	}
	if len(matched) > limit {
		matched = matched[:limit]
	}
	return matched
}

func fallbackContextSkills(skills []discoveredSkill, limit int) []discoveredSkill {
	var out []discoveredSkill
	for _, item := range skills {
		if item.State == skill.StateCold {
			continue
		}
		if strings.TrimSpace(item.Stage) == "planning" || skillPriority(item) >= 80 {
			out = append(out, item)
		}
		if len(out) >= limit {
			return out
		}
	}
	for _, item := range skills {
		if item.State == skill.StateCold {
			continue
		}
		out = append(out, item)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func skillMatchesTask(item discoveredSkill, queryText string, queryTokens map[string]bool) bool {
	if len(queryTokens) == 0 {
		return false
	}
	for _, token := range skillMatchTokens(item) {
		if queryTokens[token] {
			return true
		}
		if len([]rune(token)) >= 2 && strings.Contains(queryText, token) {
			return true
		}
	}
	return false
}

func skillMatchTokens(item discoveredSkill) []string {
	values := []string{item.Name, item.Description, item.Stage, filepath.Base(filepath.Dir(item.Path))}
	values = append(values, item.Aliases...)
	values = append(values, item.WhenToUse...)
	return skillTokens(strings.Join(values, " "))
}

func skillQueryTokens(text string) map[string]bool {
	out := map[string]bool{}
	for _, token := range skillTokens(text) {
		out[token] = true
	}
	return out
}

func skillTokens(text string) []string {
	text = strings.ToLower(text)
	var tokens []string
	var b strings.Builder
	flush := func() {
		token := strings.TrimSpace(b.String())
		b.Reset()
		if len(token) >= 2 {
			tokens = append(tokens, token)
		}
	}
	for _, r := range text {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r > 127 {
			b.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return tokens
}

func cleanupStates(cfg *config.Root, workspace string) map[string]string {
	if cfg == nil || !cfg.Skills.Cleanup.EnabledValue() {
		return nil
	}
	report, err := skill.BuildCleanupReport(skill.CleanupInput{
		Home:      cfg.App.Home,
		Workspace: workspace,
		Config:    cfg.Skills.Cleanup,
	})
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, item := range report.Items {
		out[filepath.ToSlash(item.Path)] = item.State
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func skillRoots(workspace, agentID string) []string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		agentID = "main"
	}
	return []string{
		filepath.Join(workspace, "agents", agentID, "skills"),
		filepath.Join(workspace, "skills"),
	}
}

func discoverSkillsInRoot(root string) []discoveredSkill {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []discoveredSkill
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(root, entry.Name(), "SKILL.md")
		text, redacted := readSkillContextFile(path, 4096)
		if text == "" && !redacted {
			continue
		}
		skill := parseSkillHeader(text)
		if skill.Name == "" {
			skill.Name = entry.Name()
		}
		skill.Path = path
		skill.Redacted = redacted
		out = append(out, skill)
	}
	return out
}

func readSkillContextFile(path string, limit int64) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() <= 0 {
		return "", false
	}
	if limit <= 0 {
		limit = 2048
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	fullText := strings.TrimSpace(string(data))
	if fullText == "" {
		return "", false
	}
	if info.Size() <= limit && !looksSensitivePromptContext(path, fullText) && !agentprofile.UnsafePromptContext(fullText) {
		return fullText, false
	}
	header := skillHeaderOnly(fullText)
	if header == "" {
		return "", false
	}
	return header, true
}

func skillHeaderOnly(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	allowed := map[string]bool{
		"name":        true,
		"description": true,
		"stage":       true,
		"priority":    true,
		"aliases":     true,
		"when_to_use": true,
	}
	var out []string
	out = append(out, "---")
	for i := 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "---" {
			out = append(out, "---")
			return strings.Join(out, "\n")
		}
		key, _, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		if allowed[strings.ToLower(strings.TrimSpace(key))] {
			out = append(out, lines[i])
		}
	}
	return ""
}

func parseSkillHeader(text string) discoveredSkill {
	var skill discoveredSkill
	lines := strings.Split(text, "\n")
	inFrontMatter := len(lines) > 0 && strings.TrimSpace(lines[0]) == "---"
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if i == 0 && trimmed == "---" {
			continue
		}
		if inFrontMatter && trimmed == "---" {
			break
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			if !inFrontMatter && strings.HasPrefix(trimmed, "# ") && skill.Name == "" {
				skill.Name = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			}
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "name":
			skill.Name = value
		case "description":
			skill.Description = value
		case "stage":
			skill.Stage = value
		case "priority":
			skill.Priority = value
		case "aliases":
			skill.Aliases = splitSkillHeaderList(value)
		case "when_to_use":
			skill.WhenToUse = splitSkillHeaderList(value)
		}
		if !inFrontMatter && i > 20 {
			break
		}
	}
	return skill
}

func skillsPrompt(skills []discoveredSkill) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Discovered skills:\n")
	b.WriteString("- Skills are optional guidance, not tools. Use them to decide search strategy, source evaluation, installation workflow, or answer style.\n")
	b.WriteString("- Do not claim a skill was executed unless an actual tool was called.\n")
	b.WriteString("- A skill under workspace/skills is already installed as a shared skill. Agent-specific skills under workspace/agents/<agent>/skills only override shared skills; do not ask the user to copy or symlink a shared skill unless they want an override.\n")
	for _, skill := range skills {
		b.WriteString("- ")
		b.WriteString(skill.Name)
		if skill.State == "cold" {
			b.WriteString(" (state=cold)")
			if skill.Description != "" {
				b.WriteString(": ")
				b.WriteString(skill.Description)
			}
			if len(skill.WhenToUse) > 0 {
				b.WriteString(" when_to_use=")
				b.WriteString(strings.Join(skill.WhenToUse, "; "))
			}
			if len(skill.Aliases) > 0 {
				b.WriteString(" aliases=")
				b.WriteString(strings.Join(skill.Aliases, ", "))
			}
			b.WriteString("\n")
			continue
		}
		if skill.Stage != "" || skill.Priority != "" {
			b.WriteString(" (")
			var parts []string
			if skill.Stage != "" {
				parts = append(parts, "stage="+skill.Stage)
			}
			if skill.Priority != "" {
				parts = append(parts, "priority="+skill.Priority)
			}
			b.WriteString(strings.Join(parts, ", "))
			b.WriteString(")")
		}
		if skill.Description != "" {
			b.WriteString(": ")
			b.WriteString(skill.Description)
		}
		if skill.Redacted {
			b.WriteString(" (guidance redacted: SKILL.md contains sensitive-looking fields; inspect required secrets or scripts before execution)")
		}
		b.WriteString("\n")
		if !skill.Redacted {
			if guidance := skillGuidance(skill.Path, 1200); guidance != "" {
				b.WriteString("  Guidance:\n")
				for _, line := range strings.Split(guidance, "\n") {
					if strings.TrimSpace(line) == "" {
						continue
					}
					b.WriteString("  ")
					b.WriteString(line)
					b.WriteString("\n")
				}
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func splitSkillHeaderList(value string) []string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "[]")
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	var out []string
	for _, part := range parts {
		part = strings.Trim(strings.TrimSpace(part), `"'`)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func skillPriority(skill discoveredSkill) int {
	priority, err := strconv.Atoi(strings.TrimSpace(skill.Priority))
	if err != nil {
		return 0
	}
	return priority
}

func skillGuidance(path string, limit int) string {
	text := readPromptContextFile(path, int64(limit*4))
	if text == "" {
		return ""
	}
	text = stripSkillFrontMatter(text)
	lines := strings.Split(text, "\n")
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
		out = append(out, trimmed)
		if len(strings.Join(out, "\n")) >= limit {
			break
		}
	}
	return truncateString(strings.TrimSpace(strings.Join(out, "\n")), limit)
}

func stripSkillFrontMatter(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return text
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	return text
}

func truncateString(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return strings.TrimSpace(text[:limit]) + "\n..."
}
