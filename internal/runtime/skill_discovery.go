package runtime

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dongping/mateway/internal/config"
)

type discoveredSkill struct {
	Name        string
	Description string
	Stage       string
	Priority    string
	Path        string
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
	for _, root := range roots {
		for _, skill := range discoverSkillsInRoot(root) {
			key := strings.ToLower(skill.Name)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, skill)
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
		text := readPromptContextFile(path, 4096)
		if text == "" {
			continue
		}
		skill := parseSkillHeader(text)
		if skill.Name == "" {
			skill.Name = entry.Name()
		}
		skill.Path = path
		out = append(out, skill)
	}
	return out
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
		b.WriteString("\n")
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
	return strings.TrimSpace(b.String())
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
