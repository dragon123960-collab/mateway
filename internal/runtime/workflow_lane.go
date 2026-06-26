package runtime

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/session"
)

const workflowLane = "workflow"

func findWorkflowSkillForTask(userText string, skills []discoveredSkill) (discoveredSkill, bool) {
	text := strings.ToLower(strings.TrimSpace(userText))
	if text == "" {
		return discoveredSkill{}, false
	}
	var best discoveredSkill
	bestScore := 0
	for _, skill := range skills {
		if !strings.EqualFold(strings.TrimSpace(skill.Granularity), "workflow") {
			continue
		}
		score := 0
		name := strings.ToLower(strings.TrimSpace(skill.Name))
		if name != "" && strings.Contains(text, name) {
			score += 100
		}
		for _, token := range workflowSkillTokens(skill) {
			if strings.Contains(text, token) {
				score += 5
			}
		}
		if score > bestScore {
			best = skill
			bestScore = score
		}
	}
	return best, bestScore >= 100
}

func workflowSkillTokens(skill discoveredSkill) []string {
	text := strings.ToLower(skill.Name + " " + skill.Description + " " + skill.Usage + " " + strings.Join(skill.Outputs, " ") + " " + strings.Join(skill.HumanGates, " "))
	return meaningfulTokens(text)
}

func buildWorkflowLaneGraph(taskID, userText, workspace string, skill discoveredSkill) (session.TaskGraph, session.TaskContract) {
	now := time.Now()
	skillName := strings.TrimSpace(skill.Name)
	outputRoot := filepath.Join(workspace, "outputs", slugFromTask(userText, skillName))
	tools := workflowAllowedTools(skill)
	outputs := cleanStringSlice(skill.Outputs)

	nodes := []session.TaskGraphNode{
		{
			ID:     "load-workflow-skill",
			Type:   session.NodeTypeSubtask,
			Mode:   session.NodeModeReact,
			Goal:   fmt.Sprintf("Read workflow skill %s from its discovered workspace path and required references before producing artifacts.", skillName),
			Status: session.NodeStatusPending,
			Input:  map[string]any{"skill_name": skillName, "skill_path": skill.Path, "artifact_root": outputRoot, "workflow_lane": true},
			Output: map[string]any{"skill_context": true},
			AllowedTools: intersectAllowedTools(tools, []string{
				"file.read",
				"terminal.run",
			}),
			Acceptance: session.Acceptance{Criteria: "skill instructions and relevant references have been read from the discovered workspace skill path"},
			CreatedAt:  now,
			UpdatedAt:  now,
		},
		{
			ID:      "draft-workflow-artifacts",
			Type:    session.NodeTypeSubtask,
			Mode:    session.NodeModeReact,
			Goal:    workflowDraftGoal(skillName, userText, outputRoot),
			Status:  session.NodeStatusPending,
			Depends: []string{"load-workflow-skill"},
			Input: map[string]any{
				"skill_name":       skillName,
				"skill_path":       skill.Path,
				"artifact_root":    outputRoot,
				"workflow_lane":    true,
				"workflow_outputs": outputs,
				"attempt_feedback": "Use file.read for local files, toolresult.read for raw_ref/tool-result references, and web.fetch only for http(s) URLs. If file.edit old_string does not match, re-read the file or use file.write for full artifact replacement.",
			},
			Output:       workflowOutputMap(outputs),
			AllowedTools: tools,
			Acceptance:   session.Acceptance{Criteria: workflowDraftAcceptance(skillName, outputRoot)},
			CreatedAt:    now,
			UpdatedAt:    now,
		},
		{
			ID:      "review-workflow-gate",
			Type:    session.NodeTypeHumanReview,
			Mode:    session.NodeModeHuman,
			Goal:    workflowReviewQuestion(skill, outputRoot),
			Status:  session.NodeStatusPending,
			Depends: []string{"draft-workflow-artifacts"},
			Input: map[string]any{
				"workflow_lane": true,
				"human_gates":   append([]string(nil), skill.HumanGates...),
				"artifact_root": outputRoot,
			},
			Acceptance: session.Acceptance{Criteria: workflowReviewQuestion(skill, outputRoot)},
			CreatedAt:  now,
			UpdatedAt:  now,
		},
		{
			ID:      "finalize-workflow-artifacts",
			Type:    session.NodeTypeSubtask,
			Mode:    session.NodeModeReact,
			Goal:    workflowFinalizeGoal(skillName, outputRoot),
			Status:  session.NodeStatusPending,
			Depends: []string{"review-workflow-gate"},
			Input: map[string]any{
				"skill_name":       skillName,
				"skill_path":       skill.Path,
				"artifact_root":    outputRoot,
				"workflow_lane":    true,
				"workflow_outputs": outputs,
				"attempt_feedback": "Continue from the reviewed draft artifacts and user feedback. Use exact absolute paths under artifact_root; do not write into the skill directory.",
			},
			Output:       workflowOutputMap(outputs),
			AllowedTools: tools,
			Acceptance:   session.Acceptance{Criteria: workflowFinalizeAcceptance(skillName, outputRoot, outputs)},
			CreatedAt:    now,
			UpdatedAt:    now,
		},
		{
			ID:         "summarize-workflow-delivery",
			Type:       session.NodeTypeSubtask,
			Mode:       session.NodeModeDirect,
			Goal:       "Summarize the delivered workflow artifacts with exact absolute paths and any remaining blockers.",
			Status:     session.NodeStatusPending,
			Depends:    []string{"finalize-workflow-artifacts"},
			Input:      map[string]any{"artifact_root": outputRoot, "workflow_lane": true},
			Output:     map[string]any{"final_answer": true},
			Acceptance: session.Acceptance{Criteria: "final answer lists concrete artifact paths and does not claim missing files exist"},
			CreatedAt:  now,
			UpdatedAt:  now,
		},
	}

	g := session.TaskGraph{
		ID:        "graph-" + taskID,
		TaskID:    taskID,
		Status:    session.GraphStatusPlanned,
		Lane:      workflowLane,
		Nodes:     nodes,
		CreatedAt: now,
		UpdatedAt: now,
	}

	contract := session.TaskContract{
		Summary:       strings.TrimSpace(userText),
		RequiresTools: len(tools) > 0,
		RequiredTools: append([]string(nil), tools...),
		RequiredEvidence: []session.TaskEvidenceContract{{
			Kind:        "skill_instruction",
			Tool:        "file.read",
			Description: "read workflow skill SKILL.md at " + skill.Path,
		}},
		ExpectedOutcome: workflowFinalizeAcceptance(skillName, outputRoot, outputs),
		TaskAcceptance:  workflowFinalizeAcceptance(skillName, outputRoot, outputs),
		FinalOutput:     outputs,
		RequiredSkills: []session.RequiredSkill{{
			Name:   skillName,
			Path:   skill.Path,
			Reason: "workflow lane matched registered workflow skill",
		}},
		PlanItems: []session.TaskPlanItem{
			{ID: "plan-1", Title: "Read workflow skill", Status: "pending", Tool: "file.read", Criteria: skill.Path},
			{ID: "plan-2", Title: "Draft workflow artifacts", Status: "pending", Tool: firstTool(tools), Criteria: workflowDraftAcceptance(skillName, outputRoot)},
			{ID: "plan-3", Title: "Human review gate", Status: "pending", Criteria: workflowReviewQuestion(skill, outputRoot)},
			{ID: "plan-4", Title: "Finalize workflow artifacts", Status: "pending", Tool: firstTool(tools), Criteria: workflowFinalizeAcceptance(skillName, outputRoot, outputs)},
		},
		CreatedAt: now,
	}
	return g, contract
}

func workflowAllowedTools(skill discoveredSkill) []string {
	tools := cleanStringSlice(skill.AllowedTools)
	if len(tools) == 0 {
		tools = []string{"file.read", "file.write", "file.edit", "terminal.run", "web.search", "web.fetch", "toolresult.read"}
	}
	if !containsWorkflowTool(tools, "toolresult.read") {
		tools = append(tools, "toolresult.read")
	}
	return tools
}

func workflowOutputMap(outputs []string) map[string]any {
	if len(outputs) == 0 {
		return map[string]any{"artifact_paths": true}
	}
	out := make(map[string]any, len(outputs)+1)
	out["artifact_paths"] = true
	for _, key := range outputs {
		if strings.TrimSpace(key) != "" {
			out[key] = true
		}
	}
	return out
}

func workflowDraftGoal(skillName, userText, outputRoot string) string {
	if strings.Contains(strings.ToLower(skillName), "ppt") {
		return "Create the research notes, Xiaohongshu script draft, and slide outline for the PPT workflow under " + outputRoot + " using absolute paths and the skill's standard hyphenated filenames. Do not generate final decks before the human review gate."
	}
	return "Create the draft artifacts for workflow skill " + skillName + " under " + outputRoot + " using absolute paths for task: " + strings.TrimSpace(userText)
}

func workflowDraftAcceptance(skillName, outputRoot string) string {
	if strings.Contains(strings.ToLower(skillName), "ppt") {
		return "research-notes.md, xiaohongshu-script.md, and slide-outline.md exist under " + outputRoot + "; final decks are deferred until human review"
	}
	return "draft artifacts exist under " + outputRoot
}

func workflowFinalizeGoal(skillName, outputRoot string) string {
	if strings.Contains(strings.ToLower(skillName), "ppt") {
		return "After user review, generate the final HTML PPT decks, recording guide, and production metadata under " + outputRoot + " using absolute paths."
	}
	return "After user review, generate the final workflow artifacts under " + outputRoot + " using absolute paths."
}

func workflowFinalizeAcceptance(skillName, outputRoot string, outputs []string) string {
	if strings.Contains(strings.ToLower(skillName), "ppt") {
		return "final HTML PPT package is complete under " + outputRoot + " with script, slide outline, horizontal deck, vertical deck, recording guide, and production metadata"
	}
	if len(outputs) > 0 {
		return "all declared workflow outputs exist under " + outputRoot + ": " + strings.Join(outputs, ", ")
	}
	return "final workflow artifacts exist under " + outputRoot
}

func workflowReviewQuestion(skill discoveredSkill, outputRoot string) string {
	if strings.Contains(strings.ToLower(skill.Name), "ppt") {
		skillDir := filepath.Dir(strings.TrimSpace(skill.Path))
		styleCatalog := filepath.Join(skillDir, "assets", "style-catalog", "index.html")
		return "请审核口播稿和 slide outline，并选择或确认 PPT 风格。\n\n" +
			"口播稿：" + filepath.Join(outputRoot, "xiaohongshu-script.md") + "\n" +
			"Slide outline：" + filepath.Join(outputRoot, "slide-outline.md") + "\n" +
			"风格选择 HTML：" + styleCatalog + "\n\n" +
			"如果口播稿不用修改，请直接说明“口播稿不用修改”，并选择一个风格继续生成 HTML PPT。"
	}
	if len(skill.HumanGates) > 0 {
		return "Please review the workflow artifacts under " + outputRoot + " and respond with approval or requested changes. Gates: " + strings.Join(skill.HumanGates, "; ")
	}
	return "Please review the workflow artifacts under " + outputRoot + " and respond with approval or requested changes."
}

func slugFromTask(userText, fallback string) string {
	lower := strings.ToLower(strings.TrimSpace(userText))
	replacements := map[string]string{
		"「": "", "」": "", "\"": "", "'": "", "：": " ", ":": " ", "，": " ", ",": " ", "。": " ", ".": " ",
		"使用": "", "选题是": "", "选题": "", "时长": "", "分钟": "", "生成": "", "小红书": "xiaohongshu", "口播稿": "script", "html": "html", "ppt": "ppt",
	}
	for old, repl := range replacements {
		lower = strings.ReplaceAll(lower, old, repl)
	}
	var parts []string
	for _, token := range meaningfulTokens(lower) {
		token = strings.Trim(token, "-_/")
		if token != "" && len([]rune(token)) <= 32 {
			parts = append(parts, token)
		}
		if len(parts) >= 8 {
			break
		}
	}
	if len(parts) == 0 {
		parts = meaningfulTokens(strings.ToLower(fallback))
	}
	if len(parts) == 0 {
		return "workflow-output"
	}
	return strings.Join(parts, "-")
}

func intersectAllowedTools(allowed, want []string) []string {
	set := stringSet(allowed)
	var out []string
	for _, tool := range want {
		if set[tool] {
			out = append(out, tool)
		}
	}
	if len(out) == 0 {
		return want
	}
	return out
}

func containsWorkflowTool(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}

func firstTool(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func isWorkflowLaneGraph(g *session.TaskGraph) bool {
	return g != nil && strings.EqualFold(strings.TrimSpace(g.Lane), workflowLane)
}
