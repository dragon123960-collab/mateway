package harness

import (
	"fmt"
	"regexp"
	"strings"
)

var cliCommandPattern = regexp.MustCompile(`\b[a-z0-9][a-z0-9._-]{1,64}\b`)

func buildCLIExplorationHint(goal string, visibleToolNames []string) string {
	if !looksLikeCLILearningIntent(goal) {
		return ""
	}
	command := detectCLICommand(goal)
	if command == "" {
		return ""
	}

	lines := []string{
		"## CLI_INSPECTION_POLICY",
		fmt.Sprintf("The user is asking you to learn or inspect the CLI command `%s`.", command),
		"Do not ask the user for docs or links before you try local inspection.",
	}

	if providerTool := matchingCLIProviderTool(command, visibleToolNames); providerTool != "" {
		lines = append(lines, fmt.Sprintf("A matching CLI provider appears to exist. Prefer `%s` first for discovery if it is visible.", providerTool))
	}
	if hasVisibleTool(visibleToolNames, "sandbox_exec") {
		lines = append(lines,
			fmt.Sprintf("If `%s` is not already exposed as a provider tool, use `sandbox_exec` to inspect it locally.", command),
			fmt.Sprintf("Preferred local inspection order for `%s`: `--help`, then `-h`, then `help`, then `version`.", command),
		)
	}
	if hasVisibleTool(visibleToolNames, "web_search") || hasVisibleTool(visibleToolNames, "browser_fetch") {
		lines = append(lines, "Only if local inspection cannot resolve the command should you fall back to web_search/browser_fetch for external docs.")
	}
	lines = append(lines, "If the command is missing or shell-only, explain that clearly and continue with the best fallback instead of stopping early.")
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func looksLikeCLILearningIntent(goal string) bool {
	goal = strings.ToLower(strings.TrimSpace(goal))
	if goal == "" {
		return false
	}
	return containsAny(goal,
		"学习", "研究", "怎么用", "用法", "使用", "help", "usage", "inspect", "cli", "命令行", "command",
	)
}

func detectCLICommand(goal string) string {
	goal = strings.ToLower(strings.TrimSpace(goal))
	matches := cliCommandPattern.FindAllString(goal, -1)
	for _, match := range matches {
		switch {
		case strings.Contains(match, "-"):
			return match
		case strings.HasSuffix(match, "cli"), strings.HasSuffix(match, "ctl"), strings.HasSuffix(match, "cmd"):
			return match
		}
	}
	return ""
}

func matchingCLIProviderTool(command string, visibleToolNames []string) string {
	command = strings.TrimSpace(strings.ToLower(command))
	if command == "" {
		return ""
	}
	provider := normalizeCLIProviderToken(command)
	candidates := []string{
		provider + "_list",
		provider + "_run",
	}
	for _, candidate := range candidates {
		if hasVisibleTool(visibleToolNames, candidate) {
			return candidate
		}
	}
	return ""
}

func normalizeCLIProviderToken(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}
