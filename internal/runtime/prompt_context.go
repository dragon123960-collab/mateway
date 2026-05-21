package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/model"
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
	ShortMemory string
	LongMemory  string
}

func buildModelContextPrompt(msg string, stage string, matches []skill.Match, toolDefs []tool.Definition, toolCtx tool.Context, opts ...promptContextOptions) string {
	now := time.Now()
	files := loadAgentPromptFiles(toolCtx.Workspace, "main")
	var option promptContextOptions
	if len(opts) > 0 {
		option = opts[0]
	}
	sections := []string{
		"You are Mateway, a practical personal work assistant agent.",
		"",
		"Core objective:",
		"Help the user complete work, organize information, call tools safely, and produce clear conclusions in the user's language.",
		"",
		"Current date:",
		now.Format("2006-01-02"),
		"",
		"User timezone:",
		firstNonEmpty(now.Location().String(), "Asia/Shanghai"),
		"",
		"Current user request:",
		strings.TrimSpace(msg),
		"",
	}
	if env := renderEnvironmentContext(toolCtx); env != "" {
		sections = append(sections, "", "Current environment:", env)
	}
	if extra := renderAgentPromptFiles(files); extra != "" {
		sections = append(sections, "", extra)
	}
	if memory := strings.TrimSpace(option.ShortMemory); memory != "" {
		sections = append(sections, "", "Short memory:", memory)
	}
	if memory := strings.TrimSpace(option.LongMemory); memory != "" {
		sections = append(sections, "", "Relevant long memory:", memory)
	}
	if selected := renderSelectedSkills(matches); selected != "" {
		sections = append(sections, "", "Selected skills:", selected)
	}
	if toolsText := renderToolNames(toolDefs); toolsText != "" {
		sections = append(sections, "", "Available tools:", toolsText)
	}
	sections = append(sections,
		"",
		"Tool-use rules:",
		"1. Do not expose raw tool calls or internal tool arguments to the user.",
		"2. Tool results will be supplied by the system.",
		"3. Final answers must be structured, readable, and written in the user's language unless the user requests otherwise.",
		"",
		"Current stage:",
		stage,
	)
	return strings.TrimSpace(strings.Join(sections, "\n"))
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

func renderAgentPromptFiles(files agentPromptFiles) string {
	parts := make([]string, 0, 5)
	if files.Soul != "" {
		parts = append(parts, "soul.md:\n"+files.Soul)
	}
	if files.Agent != "" {
		parts = append(parts, "agent.md:\n"+files.Agent)
	}
	if files.User != "" {
		parts = append(parts, "user.md:\n"+files.User)
	}
	if files.Memory != "" {
		parts = append(parts, "memory.md:\n"+files.Memory)
	}
	if files.Tools != "" {
		parts = append(parts, "tools.md:\n"+files.Tools)
	}
	return strings.Join(parts, "\n\n")
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

func renderSelectedSkills(matches []skill.Match) string {
	if len(matches) == 0 {
		return "none"
	}
	lines := make([]string, 0, len(matches))
	for _, match := range matches {
		line := "- " + match.Definition.Name
		if match.Definition.Description != "" {
			line += ": " + match.Definition.Description
		}
		if strings.TrimSpace(match.Reason) != "" {
			line += " (" + match.Reason + ")"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func renderToolNames(defs []tool.Definition) string {
	if len(defs) == 0 {
		return ""
	}
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, fmt.Sprintf("- %s: %s", def.Name, def.Description))
	}
	return strings.Join(names, "\n")
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
		return "I need one more detail from you before I can continue."
	}
	if style == "approval_pending" {
		for i := len(results) - 1; i >= 0; i-- {
			if text := strings.TrimSpace(results[i].Output); text != "" {
				return text
			}
		}
		return "I need your confirmation before I can continue."
	}
	return fallbackSynthesis(results)
}
