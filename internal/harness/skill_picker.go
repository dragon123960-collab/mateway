package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/dongping/mateway/internal/skills"
)

const defaultSkillPickerLimit = 3

type skillPickerResponse struct {
	Skills []string `json:"skills"`
	Reason string   `json:"reason,omitempty"`
}

func (h *Harness) selectSkillsForRun(ctx context.Context, run Run) ([]string, string, string) {
	visible := h.visibleSkillCatalog(run.Capabilities.VisibleSkills)
	if len(visible) == 0 || strings.TrimSpace(run.Goal) == "" {
		return nil, "", ""
	}

	heuristic := skillNames(skills.ProgressiveDisclosure(run.Goal, visible, defaultSkillPickerLimit))
	if len(heuristic) > 0 && !shouldUseModelSkillPicker(run.Goal, visible, heuristic) {
		note := fmt.Sprintf("source=heuristic selected=%s", formatSelectedSkills(heuristic))
		return heuristic, "heuristic", note
	}

	modelNames, reason, err := h.pickSkillsWithModel(ctx, run.ID, run.Goal, visible, defaultSkillPickerLimit)
	if err == nil {
		note := fmt.Sprintf("source=model selected=%s", formatSelectedSkills(modelNames))
		if strings.TrimSpace(reason) != "" {
			note += " reason=" + trimInline(reason, 180)
		}
		return modelNames, "model", note
	}

	if len(heuristic) == 0 {
		note := "source=none"
		if err != nil {
			note += " error=" + trimInline(err.Error(), 180)
		}
		return nil, "fallback_none", note
	}
	note := fmt.Sprintf("source=heuristic selected=%s", formatSelectedSkills(heuristic))
	if err != nil {
		note += " fallback_reason=" + trimInline(err.Error(), 180)
	}
	return heuristic, "heuristic", note
}

func (h *Harness) pickSkillsWithModel(ctx context.Context, runID, goal string, visible []skills.Skill, limit int) ([]string, string, error) {
	if !h.EnableEino {
		return nil, "", fmt.Errorf("skill picker model is not enabled")
	}
	model, modelOpts, err := h.newEinoModel(ctx, runID)
	if err != nil {
		return nil, "", err
	}

	messages := []*schema.Message{
		schema.SystemMessage(buildSkillPickerSystemPrompt(limit)),
		schema.UserMessage(buildSkillPickerUserPrompt(goal, visible, limit)),
	}
	msg, err := model.Generate(withModelPurpose(ctx, "skill_picker"), messages, modelOpts...)
	if err != nil {
		return nil, "", err
	}
	names, reason, err := parseSkillPickerContent(msg.Content, visible, limit)
	if err != nil {
		return nil, "", err
	}
	return names, reason, nil
}

func buildSkillPickerSystemPrompt(limit int) string {
	return strings.TrimSpace(fmt.Sprintf(`
You are the skill-picker for the Mateway agent runtime.

Choose at most %d skills from the visible catalog for the current user task.
Your job is to decide which SKILL.md bodies should be loaded into prompt context now.

Rules:
- Only choose from the visible catalog.
- Prefer 0 to %d skills.
- Use discovery metadata only; do not invent missing details.
- Do not choose broad or redundant skills just because they sound related.
- If no skill needs to be activated now, return an empty list.
- Return strict JSON only, with no markdown fences and no extra prose.

JSON shape:
{"skills":["skill-name"],"reason":"short explanation"}
`, limit, limit))
}

func buildSkillPickerUserPrompt(goal string, visible []skills.Skill, limit int) string {
	lines := []string{
		"Goal:",
		strings.TrimSpace(goal),
		"",
		fmt.Sprintf("Visible skill catalog (choose at most %d):", limit),
	}
	for _, skill := range visible {
		lines = append(lines, "- "+formatSkillPickerEntry(skill))
	}
	return strings.Join(lines, "\n")
}

func formatSkillPickerEntry(skill skills.Skill) string {
	parts := []string{fmt.Sprintf("name=%s", skill.Manifest.Name)}
	if desc := strings.TrimSpace(skill.Manifest.Description); desc != "" {
		parts = append(parts, "description="+desc)
	}
	if compat := strings.TrimSpace(skill.Manifest.Compatibility); compat != "" {
		parts = append(parts, "compatibility="+compat)
	}
	if len(skill.Manifest.Tags) > 0 {
		parts = append(parts, "tags="+strings.Join(skill.Manifest.Tags, ","))
	}
	if len(skill.Manifest.AllowedTools) > 0 {
		parts = append(parts, "allowed-tools="+strings.Join(skill.Manifest.AllowedTools, ","))
	}
	if count := len(skill.Resources.Scripts) + len(skill.Resources.References) + len(skill.Resources.Assets); count > 0 {
		parts = append(parts, fmt.Sprintf("resources=%d", count))
	}
	if skill.Executable && skill.Manifest.Type != "" {
		parts = append(parts, "runtime="+string(skill.Manifest.Type))
	} else {
		parts = append(parts, "runtime=doc")
	}
	return strings.Join(parts, " | ")
}

func parseSkillPickerContent(content string, visible []skills.Skill, limit int) ([]string, string, error) {
	raw := extractJSONObject(content)
	if raw == "" {
		return nil, "", fmt.Errorf("skill picker returned empty content")
	}
	var payload skillPickerResponse
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, "", fmt.Errorf("decode skill picker response: %w", err)
	}
	allowed := make(map[string]bool, len(visible))
	for _, skill := range visible {
		allowed[skill.Manifest.Name] = true
	}
	out := make([]string, 0, min(limit, len(payload.Skills)))
	seen := map[string]bool{}
	for _, name := range payload.Skills {
		name = strings.TrimSpace(name)
		if name == "" || !allowed[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
		if len(out) >= limit {
			break
		}
	}
	return out, strings.TrimSpace(payload.Reason), nil
}

func extractJSONObject(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```JSON")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end >= start {
		return strings.TrimSpace(content[start : end+1])
	}
	return content
}

func formatSelectedSkills(list []string) string {
	if len(list) == 0 {
		return "(none)"
	}
	return strings.Join(list, ", ")
}

func skillNames(list []skills.Skill) []string {
	if len(list) == 0 {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, skill := range list {
		name := strings.TrimSpace(skill.Manifest.Name)
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func shouldUseModelSkillPicker(goal string, visible []skills.Skill, heuristic []string) bool {
	goal = strings.ToLower(strings.TrimSpace(goal))
	if goal == "" {
		return false
	}
	if skillPickerContainsAny(goal,
		"skill", "skills", "技能", "选择技能", "挑选技能", "which skill", "what skill",
	) {
		return true
	}
	if len(heuristic) == 0 && len(visible) >= 6 {
		return true
	}
	return false
}

func skillPickerContainsAny(text string, items ...string) bool {
	for _, item := range items {
		if strings.Contains(text, strings.ToLower(strings.TrimSpace(item))) {
			return true
		}
	}
	return false
}
