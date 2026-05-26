package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/session"
	"github.com/dongping/mateway/internal/skill"
	"github.com/dongping/mateway/internal/tool"
)

type agentPromptFiles struct {
	Soul   string
	Agent  string
	User   string
	Memory string
	Tools  string
}

type promptContextOptions struct {
	ShortMemory   string
	LongMemory    string
	Understanding taskUnderstanding
	CurrentTask   *session.TaskState
}

type promptStageProfile struct {
	IncludeDateTime      bool
	IncludeEnvironment   bool
	IncludeSoul          bool
	IncludeAgent         bool
	IncludeUser          bool
	IncludeMemoryGuide   bool
	IncludeToolsGuide    bool
	IncludeShortMemory   bool
	IncludeLongMemory    bool
	IncludeUnderstanding bool
	IncludeCurrentTask   bool
	IncludeToolRules     bool
}

const (
	promptStageStepAcceptance  = "step_acceptance"
	promptStageFinalAcceptance = "final_acceptance"
)

func buildModelContextPrompt(_ string, stage string, _ []skill.Match, _ []tool.Definition, toolCtx tool.Context, opts ...promptContextOptions) string {
	now := time.Now()
	files := loadAgentPromptFiles(toolCtx.Workspace, "main")
	var option promptContextOptions
	if len(opts) > 0 {
		option = opts[0]
	}
	profile := promptProfileForStage(stage)
	sections := []string{
		"You are Mateway, a practical personal work assistant agent.",
		"",
		"Core objective:",
		"Help the user complete work, organize information, call tools safely, and produce clear conclusions in the user's language.",
	}
	if profile.IncludeDateTime {
		sections = append(sections,
			"",
			"Current date:",
			now.Format("2006-01-02"),
			"",
			"User timezone:",
			firstNonEmpty(now.Location().String(), "Asia/Shanghai"),
		)
	}
	if profile.IncludeEnvironment {
		if env := renderEnvironmentContext(toolCtx); env != "" {
			sections = append(sections, "", "Current environment:", env)
		}
	}
	if extra := renderAgentPromptFilesForStage(files, profile); extra != "" {
		sections = append(sections, "", extra)
	}
	if profile.IncludeShortMemory {
		if memory := strings.TrimSpace(option.ShortMemory); memory != "" {
			sections = append(sections, "", "Short memory:", memory)
		}
	}
	if profile.IncludeLongMemory {
		if memory := strings.TrimSpace(option.LongMemory); memory != "" {
			sections = append(sections, "", "Relevant long memory:", memory)
		}
	}
	if profile.IncludeUnderstanding {
		understanding := renderUnderstandingForStage(stage, option.Understanding)
		if understanding != "" {
			sections = append(sections, "", "Task understanding:", understanding)
		}
	}
	if profile.IncludeCurrentTask {
		if progress := renderCurrentTaskProgress(option.CurrentTask); progress != "" {
			sections = append(sections, "", "Current execution progress:", progress)
		}
	}
	if profile.IncludeToolRules {
		sections = append(sections,
			"",
			"Tool-use rules:",
			"1. Do not expose raw tool calls or internal tool arguments to the user.",
			"2. Tool results will be supplied by the system.",
			"3. Final answers must be structured, readable, and written in the user's language unless the user requests otherwise.",
		)
	}
	sections = append(sections,
		"",
		"Current stage:",
		stage,
	)
	return strings.TrimSpace(strings.Join(sections, "\n"))
}

func buildStageModelPrompt(contextPrompt string, defs []skill.Definition) string {
	contextPrompt = strings.TrimSpace(contextPrompt)
	skillPrompt := strings.TrimSpace(skill.PromptBlock(defs))
	switch {
	case contextPrompt == "" && skillPrompt == "":
		return ""
	case skillPrompt == "":
		return contextPrompt
	case contextPrompt == "":
		return "Skills context:\n" + skillPrompt
	default:
		return contextPrompt + "\n\nSkills context:\n" + skillPrompt
	}
}

func promptProfileForStage(stage string) promptStageProfile {
	switch strings.TrimSpace(stage) {
	case promptStageFinalAcceptance:
		return promptStageProfile{
			IncludeDateTime:      true,
			IncludeSoul:          true,
			IncludeAgent:         true,
			IncludeUser:          true,
			IncludeUnderstanding: true,
			IncludeToolRules:     true,
		}
	case promptStageStepAcceptance:
		return promptStageProfile{
			IncludeDateTime:    true,
			IncludeSoul:        true,
			IncludeAgent:       true,
			IncludeUser:        true,
			IncludeToolRules:   true,
			IncludeEnvironment: false,
		}
	case skill.StageSynthesis:
		return promptStageProfile{
			IncludeSoul:        true,
			IncludeAgent:       true,
			IncludeUser:        true,
			IncludeToolRules:   true,
			IncludeDateTime:    false,
			IncludeEnvironment: false,
		}
	case skill.StagePlanningRepair:
		return promptStageProfile{
			IncludeDateTime:      true,
			IncludeEnvironment:   true,
			IncludeSoul:          true,
			IncludeAgent:         true,
			IncludeUser:          true,
			IncludeShortMemory:   true,
			IncludeUnderstanding: true,
			IncludeCurrentTask:   false,
			IncludeToolRules:     true,
		}
	case skill.StagePlanning:
		return promptStageProfile{
			IncludeDateTime:      true,
			IncludeEnvironment:   true,
			IncludeSoul:          true,
			IncludeAgent:         true,
			IncludeUser:          true,
			IncludeMemoryGuide:   true,
			IncludeShortMemory:   true,
			IncludeLongMemory:    true,
			IncludeUnderstanding: true,
			IncludeCurrentTask:   true,
			IncludeToolRules:     true,
		}
	default:
		return promptStageProfile{
			IncludeDateTime:      true,
			IncludeEnvironment:   true,
			IncludeSoul:          true,
			IncludeAgent:         true,
			IncludeUser:          true,
			IncludeShortMemory:   true,
			IncludeUnderstanding: true,
			IncludeToolRules:     true,
		}
	}
}

func renderAgentPromptFilesForStage(files agentPromptFiles, profile promptStageProfile) string {
	parts := make([]string, 0, 5)
	if profile.IncludeSoul && files.Soul != "" {
		parts = append(parts, "soul.md:\n"+files.Soul)
	}
	if profile.IncludeAgent && files.Agent != "" {
		parts = append(parts, "agent.md:\n"+files.Agent)
	}
	if profile.IncludeUser && files.User != "" {
		parts = append(parts, "user.md:\n"+files.User)
	}
	if profile.IncludeMemoryGuide && files.Memory != "" {
		parts = append(parts, "memory.md:\n"+files.Memory)
	}
	if profile.IncludeToolsGuide && files.Tools != "" {
		parts = append(parts, "tools.md:\n"+files.Tools)
	}
	return strings.Join(parts, "\n\n")
}

func renderAgentPromptFiles(files agentPromptFiles) string {
	return renderAgentPromptFilesForStage(files, promptStageProfile{
		IncludeSoul:        true,
		IncludeAgent:       true,
		IncludeUser:        true,
		IncludeMemoryGuide: true,
		IncludeToolsGuide:  true,
	})
}

func loadAgentPromptFiles(workspace, agentID string) agentPromptFiles {
	root := filepath.Join(workspace, "agents", agentID)
	return agentPromptFiles{
		Soul:   readOptionalFile(filepath.Join(root, "soul.md")),
		Agent:  readOptionalFile(filepath.Join(root, "agent.md")),
		User:   readOptionalFile(filepath.Join(root, "user.md")),
		Memory: readOptionalFile(filepath.Join(root, "memory.md")),
		Tools:  readOptionalFile(filepath.Join(root, "tools.md")),
	}
}

func readOptionalFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func renderEnvironmentContext(toolCtx tool.Context) string {
	lines := []string{
		"- operating_system: " + runtime.GOOS,
		"- architecture: " + runtime.GOARCH,
		"- shell: " + firstNonEmpty(strings.TrimSpace(os.Getenv("SHELL")), "unknown"),
		"- home: " + firstNonEmpty(toolCtx.Home, "unknown"),
		"- workspace: " + firstNonEmpty(toolCtx.Workspace, "unknown"),
		"- project_root: " + firstNonEmpty(toolCtx.ProjectRoot, "unknown"),
	}
	if cmds := availableCommandSummary(); cmds != "" {
		lines = append(lines, "- available_commands: "+cmds)
	}
	if pkg := packageManagerSummary(); pkg != "" {
		lines = append(lines, "- package_managers: "+pkg)
	}
	if toolchain := keyToolAvailabilitySummary(); toolchain != "" {
		lines = append(lines, "- key_tooling: "+toolchain)
	}
	return strings.Join(lines, "\n")
}

func availableCommandSummary() string {
	candidates := []string{"sh", "zsh", "bash", "git", "go", "rg", "sed", "awk", "find", "curl"}
	available := make([]string, 0, len(candidates))
	for _, name := range candidates {
		if _, err := exec.LookPath(name); err == nil {
			available = append(available, name)
		}
	}
	return strings.Join(available, ", ")
}

func packageManagerSummary() string {
	candidates := []string{"brew", "apt", "apt-get", "yum", "dnf", "pacman"}
	available := make([]string, 0, len(candidates))
	for _, name := range candidates {
		if _, err := exec.LookPath(name); err == nil {
			available = append(available, name)
		}
	}
	return strings.Join(available, ", ")
}

func keyToolAvailabilitySummary() string {
	candidates := []string{"git", "go", "node", "python3"}
	status := make([]string, 0, len(candidates))
	for _, name := range candidates {
		_, err := exec.LookPath(name)
		availability := "missing"
		if err == nil {
			availability = "available"
		}
		status = append(status, name+"="+availability)
	}
	return strings.Join(status, ", ")
}

func renderUnderstanding(understanding taskUnderstanding) string {
	if strings.TrimSpace(understanding.Goal) == "" {
		return ""
	}
	lines := []string{"- goal: " + understanding.Goal}
	if len(understanding.Capabilities) > 0 {
		lines = append(lines, "- capabilities: "+strings.Join(understanding.Capabilities, ", "))
	}
	if len(understanding.CompletionDraft) > 0 {
		lines = append(lines, "- completion_draft: "+strings.Join(understanding.CompletionDraft, " | "))
	}
	if len(understanding.EvidenceHints) > 0 {
		lines = append(lines, "- evidence_hints: "+strings.Join(understanding.EvidenceHints, " | "))
	}
	if len(understanding.Constraints) > 0 {
		lines = append(lines, "- constraints: "+strings.Join(understanding.Constraints, ", "))
	}
	if strings.TrimSpace(understanding.RiskLevel) != "" {
		lines = append(lines, "- risk_level: "+understanding.RiskLevel)
	}
	if understanding.NeedsGrounding {
		lines = append(lines, "- needs_grounding: true")
	}
	if understanding.NeedsMutation {
		lines = append(lines, "- needs_mutation: true")
	}
	return strings.Join(lines, "\n")
}

func renderUnderstandingForStage(stage string, understanding taskUnderstanding) string {
	switch strings.TrimSpace(stage) {
	case skill.StagePlanningRepair:
		return renderRepairUnderstanding(understanding)
	case promptStageFinalAcceptance:
		return renderFinalAcceptanceUnderstanding(understanding)
	default:
		return renderUnderstanding(understanding)
	}
}

func renderRepairUnderstanding(understanding taskUnderstanding) string {
	lines := []string{}
	if goal := strings.TrimSpace(understanding.Goal); goal != "" {
		lines = append(lines, "- remaining_goal: "+goal)
	}
	if reason := strings.TrimSpace(repairFailureReason(understanding)); reason != "" {
		lines = append(lines, "- failure_reason: "+reason)
	}
	if delta := strings.TrimSpace(repairCompletionDelta(understanding)); delta != "" {
		lines = append(lines, "- completion_delta: "+delta)
	}
	if risk := strings.TrimSpace(understanding.RiskLevel); risk != "" {
		lines = append(lines, "- risk_level: "+risk)
	}
	return strings.Join(lines, "\n")
}

func renderFinalAcceptanceUnderstanding(understanding taskUnderstanding) string {
	lines := []string{}
	if goal := strings.TrimSpace(understanding.Goal); goal != "" {
		lines = append(lines, "- goal: "+goal)
	}
	if len(understanding.CompletionDraft) > 0 {
		lines = append(lines, "- completion_draft: "+strings.Join(understanding.CompletionDraft, " | "))
	}
	if strings.TrimSpace(understanding.RiskLevel) != "" {
		lines = append(lines, "- risk_level: "+understanding.RiskLevel)
	}
	if understanding.IsScheduledRun {
		lines = append(lines, "- scheduled_run: true")
	}
	return strings.Join(lines, "\n")
}

func repairFailureReason(understanding taskUnderstanding) string {
	if len(understanding.Constraints) == 0 {
		return ""
	}
	return strings.Join(understanding.Constraints, " | ")
}

func repairCompletionDelta(understanding taskUnderstanding) string {
	if len(understanding.CompletionDraft) == 0 {
		return ""
	}
	return strings.Join(understanding.CompletionDraft, " | ")
}

func renderCurrentTaskProgress(task *session.TaskState) string {
	if task == nil || len(task.StepOrder) == 0 || len(task.StepStates) == 0 {
		return ""
	}
	lines := []string{}
	if strings.TrimSpace(task.ExecutionStatus) != "" {
		lines = append(lines, "- execution_status: "+task.ExecutionStatus)
	}
	lines = append(lines, "- completed_steps should not be repeated unless the plan has materially changed.")
	lines = append(lines, "Step progress:")
	for _, id := range task.StepOrder {
		step, ok := task.StepStates[id]
		if !ok {
			continue
		}
		line := "- " + id + " status=" + firstNonEmpty(step.Status, "unknown") + " tool=" + firstNonEmpty(step.Tool, "unknown")
		if strings.TrimSpace(step.AcceptanceStatus) != "" {
			line += " acceptance=" + step.AcceptanceStatus
		}
		if strings.TrimSpace(step.ResultSummary) != "" {
			line += " result=" + compactText(step.ResultSummary, 120)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func selectedSkillsTraceFields(matches []skill.Match) []map[string]any {
	out := make([]map[string]any, 0, len(matches))
	for _, match := range matches {
		out = append(out, map[string]any{
			"name":        match.Definition.Name,
			"description": match.Definition.Description,
			"stage":       match.Definition.Stage,
			"dir":         match.Definition.Dir,
			"reason":      match.Reason,
			"priority":    match.Definition.Priority,
		})
	}
	return out
}

func controlReplyText(results []model.ToolResult, style string) string {
	if style == "input_required" {
		for i := len(results) - 1; i >= 0; i-- {
			if kind, _ := results[i].Evidence["kind"].(string); kind == "user_input_required" {
				if text := strings.TrimSpace(results[i].Output); text != "" {
					return text
				}
			}
		}
		return "我还需要你补充一个信息才能继续。"
	}
	if style == "approval_pending" {
		for i := len(results) - 1; i >= 0; i-- {
			if text := strings.TrimSpace(results[i].Output); text != "" {
				return text
			}
		}
		return "继续之前需要你确认。"
	}
	return fallbackSynthesis(results)
}
