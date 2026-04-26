package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dongping/mateway/internal/skills"
)

type Assembler struct {
	Workspace          string
	SystemPrompt       string
	Goal               string
	Skills             []skills.Skill
	SelectedSkills     []skills.Skill
	UseSelectedSkills  bool
	UseSkillActivation bool
	SkillToolName      string
	MaxSelected        int
}

func (a Assembler) Build() string {
	parts := make([]string, 0, 4)
	if text := strings.TrimSpace(a.SystemPrompt); text != "" {
		parts = append(parts, text)
	}
	for _, name := range []string{"SOUL.md", "AGENT.md", "USER.md"} {
		path := filepath.Join(a.Workspace, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			continue
		}
		parts = append(parts, "## "+strings.TrimSuffix(name, ".md")+"\n"+text)
	}
	if block := a.buildSkillDisclosure(); block != "" {
		parts = append(parts, block)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func (a Assembler) buildSkillDisclosure() string {
	if len(a.Skills) == 0 {
		return ""
	}
	visible := append([]skills.Skill(nil), a.Skills...)
	sort.Slice(visible, func(i, j int) bool {
		return visible[i].Manifest.Name < visible[j].Manifest.Name
	})
	lines := []string{
		"## AVAILABLE_SKILLS",
		"The following skills are visible for this run. Use this registry to recognize relevant skill guidance, and rely on the selected SKILL.md bodies below when present.",
	}
	for _, skill := range visible {
		lines = append(lines, "- "+renderSkillSummary(skill))
	}
	selected := a.SelectedSkills
	if !a.UseSelectedSkills && len(selected) == 0 {
		selected = skills.ProgressiveDisclosure(a.Goal, visible, a.MaxSelected)
	}
	if len(selected) == 0 {
		return strings.TrimSpace(strings.Join(lines, "\n"))
	}
	lines = append(lines, "", "## SELECTED_SKILLS")
	if a.UseSkillActivation {
		toolName := firstNonEmpty(a.SkillToolName, "skill")
		lines = append(lines, "The following skills were preselected for this request. Use the `"+toolName+"` tool to activate a skill and load its full SKILL.md only when needed.")
	} else {
		lines = append(lines, "The following SKILL.md instructions were selected for the current request. Treat them as task-specific guidance. If a selected skill references relative paths like `scripts/`, `references/`, or `assets/`, resolve them from that skill directory.")
	}
	for _, skill := range selected {
		lines = append(lines, "", renderSelectedSkill(skill, a.UseSkillActivation))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func renderSkillSummary(skill skills.Skill) string {
	parts := []string{fmt.Sprintf("`%s` [%s]", skill.Manifest.Name, skillKind(skill))}
	if desc := strings.TrimSpace(skill.Manifest.Description); desc != "" {
		parts = append(parts, desc)
	}
	if compat := strings.TrimSpace(skill.Manifest.Compatibility); compat != "" {
		parts = append(parts, "compat: "+compat)
	}
	if len(skill.Manifest.AllowedTools) > 0 {
		parts = append(parts, "allowed-tools: "+strings.Join(skill.Manifest.AllowedTools, ", "))
	}
	return strings.Join(parts, " - ")
}

func renderSelectedSkill(skill skills.Skill, activationMode bool) string {
	body := strings.TrimSpace(skill.Body)
	if body == "" {
		body = strings.TrimSpace(skill.Manifest.Description)
	}
	lines := []string{
		"### " + skill.Manifest.Name,
		"source: " + skill.SkillPath,
		"kind: " + skillKind(skill),
	}
	if resources := renderSkillResources(skill.Resources); resources != "" {
		lines = append(lines, resources, "Use `read_skill_resource` to inspect these files on demand when needed.")
	}
	if activationMode {
		lines = append(lines, "Activate this skill through the skill tool to load its full instructions.")
		return strings.TrimSpace(strings.Join(lines, "\n"))
	}
	lines = append(lines, "", body)
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func skillKind(skill skills.Skill) string {
	if skill.Executable && skill.Manifest.Type != "" {
		return string(skill.Manifest.Type)
	}
	if skill.Executable {
		return "runnable"
	}
	return "doc"
}

func renderSkillResources(resources skills.ResourceSet) string {
	lines := []string{}
	for _, name := range resources.AllowedDirs() {
		if part := renderResourceCategory(name, resourcesForCategory(resources, name)); part != "" {
			lines = append(lines, part)
		}
	}
	return strings.Join(lines, "\n")
}

func renderResourceCategory(label string, items []string) string {
	if len(items) == 0 {
		return ""
	}
	const maxItems = 6
	visible := items
	if len(visible) > maxItems {
		visible = visible[:maxItems]
	}
	line := label + ": " + strings.Join(visible, ", ")
	if len(items) > len(visible) {
		line += fmt.Sprintf(" (+%d more)", len(items)-len(visible))
	}
	return line
}

func resourcesForCategory(resources skills.ResourceSet, name string) []string {
	switch name {
	case "scripts":
		return resources.Scripts
	case "references":
		return resources.References
	case "assets":
		return resources.Assets
	default:
		return resources.Extra[name]
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
