package runtime

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/memory"
	"github.com/dongping/mateway/internal/session"
)

type cliUsageContext struct {
	Executable  string
	MemoryFound bool
}

type cliUsageCandidate struct {
	Executable string
}

func cliUsageCandidateFromText(text string) cliUsageCandidate {
	for _, quoted := range backtickTokens(text) {
		name := normalizeCLIExecutableName(quoted)
		if name != "" && !commonCLIExecutable(name) && cliNameLooksExternal(name) {
			return cliUsageCandidate{Executable: name}
		}
	}
	fields := strings.Fields(strings.TrimSpace(text))
	for i, field := range fields {
		name := normalizeCLIExecutableName(field)
		if name == "" || commonCLIExecutable(name) {
			continue
		}
		if i > 0 && strings.TrimSpace(fields[i-1]) == "mateway" {
			continue
		}
		if cliNameLooksExternal(name) {
			return cliUsageCandidate{Executable: name}
		}
	}
	return cliUsageCandidate{}
}

func backtickTokens(text string) []string {
	var out []string
	parts := strings.Split(text, "`")
	for i := 1; i < len(parts); i += 2 {
		if token := strings.TrimSpace(parts[i]); token != "" {
			out = append(out, token)
		}
	}
	return out
}

func buildCLIUsageContext(user string, results []memory.SearchResult) cliUsageContext {
	candidate := cliUsageCandidateFromText(user)
	if candidate.Executable == "" {
		return cliUsageContext{}
	}
	ctx := cliUsageContext{Executable: candidate.Executable}
	wantTitle := "cli usage: " + strings.ToLower(candidate.Executable)
	for _, result := range results {
		title := strings.ToLower(strings.TrimSpace(result.Title))
		if title == wantTitle || (strings.Contains(title, "cli usage") && strings.Contains(title, strings.ToLower(candidate.Executable))) {
			ctx.MemoryFound = true
			break
		}
	}
	return ctx
}

func normalizeCLIExecutableName(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.Trim(raw, "`'\"，。,.、:：;；()[]{}<>")
	if raw == "" || strings.HasPrefix(raw, "-") {
		return ""
	}
	if strings.Contains(raw, "/") {
		raw = filepath.Base(raw)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	if strings.ContainsAny(lower, " \t\n") {
		return ""
	}
	for _, r := range lower {
		if r > 127 {
			return ""
		}
	}
	return lower
}

func cliNameLooksExternal(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	return strings.Contains(name, "-") || strings.HasSuffix(name, "cli") || strings.HasSuffix(name, "ctl") || strings.HasSuffix(name, "cmd")
}

func commonCLIExecutable(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "sh", "zsh", "bash", "fish", "cd", "pwd", "ls", "cat", "sed", "awk", "grep", "rg", "find", "xargs", "curl", "wget", "tar", "unzip", "zip",
		"git", "go", "node", "npm", "npx", "pnpm", "yarn", "python", "python3", "pip", "pip3", "ruby", "cargo", "rustc", "make", "cmake",
		"docker", "kubectl", "brew", "apt", "apt-get", "mateway":
		return true
	default:
		return false
	}
}

func (l *AgentLoop) maybeWriteCLIUsageMemory(resp Response, task session.TaskState) {
	if l.runtime.Config == nil || !l.runtime.Config.Memory.Enabled {
		return
	}
	if resp.Failed || resp.AwaitConfirm || resp.AwaitUserInput || task.Status != session.TaskCompleted {
		return
	}
	if strings.TrimSpace(l.runtime.Memory.Root) == "" {
		return
	}
	candidate := cliUsageCandidateFromTask(task)
	if candidate.Executable == "" || commonCLIExecutable(candidate.Executable) || cliUsageMemoryAlreadyLoaded(l.state.cliUsage, candidate.Executable) {
		return
	}
	body := renderCLIUsageMemoryBody(candidate.Executable, task)
	if strings.TrimSpace(body) == "" {
		return
	}
	agentID := firstNonEmpty(l.runtime.Config.Agents.Default, "main")
	result, err := l.runtime.Memory.WriteLong(memory.LongMemoryInput{
		AgentID:    agentID,
		Scope:      "agent",
		Type:       "playbook",
		Title:      "CLI usage: " + candidate.Executable,
		Body:       body,
		Sources:    cliUsageMemorySources(task),
		Tags:       []string{"cli-usage", "auto-memory"},
		Confidence: "medium",
		CreatedAt:  time.Now(),
	})
	if err != nil {
		l.runtime.Logger.Event("runtime.cli_usage_memory_failed", map[string]any{
			"trace_id":   l.state.traceID,
			"executable": candidate.Executable,
			"error":      err.Error(),
		})
		return
	}
	l.runtime.Logger.Event("runtime.cli_usage_memory_written", map[string]any{
		"trace_id":   l.state.traceID,
		"executable": candidate.Executable,
		"path":       result.Path,
	})
}

func cliUsageMemoryAlreadyLoaded(ctx cliUsageContext, executable string) bool {
	return ctx.MemoryFound && strings.EqualFold(ctx.Executable, executable)
}

func cliUsageCandidateFromTask(task session.TaskState) cliUsageCandidate {
	counts := map[string]int{}
	for _, step := range task.StepStates {
		if strings.TrimSpace(step.Tool) != "terminal.run" {
			continue
		}
		command := strings.TrimSpace(firstNonEmpty(step.Args["command"], stringValue(step.Evidence["command"])))
		root := normalizeCLIExecutableName(commandRoot(command))
		if root == "" || commonCLIExecutable(root) {
			continue
		}
		counts[root]++
	}
	if len(counts) == 0 {
		return cliUsageCandidateFromText(task.ResolvedQuery + " " + task.UserText + " " + task.PlanSummary)
	}
	var names []string
	for name := range counts {
		names = append(names, name)
	}
	sort.SliceStable(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	return cliUsageCandidate{Executable: names[0]}
}

func renderCLIUsageMemoryBody(executable string, task session.TaskState) string {
	var helps, versions, writes, failures []string
	for _, step := range task.StepStates {
		if strings.TrimSpace(step.Tool) != "terminal.run" {
			continue
		}
		command := strings.TrimSpace(firstNonEmpty(step.Args["command"], stringValue(step.Evidence["command"])))
		if normalizeCLIExecutableName(commandRoot(command)) != executable {
			continue
		}
		line := "- `" + scrubCLIUsageCommand(command) + "`"
		if step.ResultOK {
			line += " -> ok"
		} else if step.ResultError != "" {
			line += " -> " + compactText(step.ResultError, 80)
		}
		switch {
		case terminalCommandLooksHelp(command):
			helps = append(helps, line)
		case strings.Contains(strings.ToLower(command), "--version") || strings.Contains(strings.ToLower(command), " version"):
			versions = append(versions, line)
		case terminalCommandLooksExternalWriteAction(command):
			writes = append(writes, line)
		case step.ResultError != "":
			failures = append(failures, line)
		}
	}
	if len(helps) == 0 && len(writes) == 0 && len(versions) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "This playbook was generated after a successful local CLI task. Source quality: `local_help_only` unless sources below include official URLs.\n\n")
	fmt.Fprintf(&b, "## Executable\n\n- `%s`\n\n", executable)
	if len(versions) > 0 {
		fmt.Fprintf(&b, "## Version / Availability Checks\n\n%s\n\n", strings.Join(dedupeStrings(versions), "\n"))
	}
	if len(helps) > 0 {
		fmt.Fprintf(&b, "## Help / Usage Checks\n\n%s\n\n", strings.Join(dedupeStrings(helps), "\n"))
	}
	if len(writes) > 0 {
		fmt.Fprintf(&b, "## Verified Command Templates\n\n%s\n\n", strings.Join(dedupeStrings(writes), "\n"))
	}
	if len(failures) > 0 {
		fmt.Fprintf(&b, "## Failure Notes\n\n%s\n\n", strings.Join(dedupeStrings(failures), "\n"))
	}
	fmt.Fprintf(&b, "## Safety Boundary\n\n- Read-only help/version/status commands can run without confirmation.\n- Write actions such as send/create/update/delete/upload still require runtime confirmation.\n")
	return strings.TrimSpace(b.String())
}

func scrubCLIUsageCommand(command string) string {
	fields := strings.Fields(strings.TrimSpace(command))
	for i := 0; i < len(fields); i++ {
		trimmed := strings.Trim(fields[i], `'"`)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "oc_") || strings.HasPrefix(lower, "ou_") || strings.HasPrefix(lower, "om_") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") {
			fields[i] = "<redacted>"
			continue
		}
		if strings.HasPrefix(fields[i], "--") && i+1 < len(fields) {
			flag := strings.ToLower(strings.TrimPrefix(fields[i], "--"))
			if flag == "chat-id" || flag == "chat_id" || flag == "user-id" || flag == "user_id" || flag == "open-id" || flag == "open_id" || strings.Contains(flag, "token") || strings.Contains(flag, "secret") {
				fields[i+1] = "<redacted>"
			}
			if flag == "text" || flag == "content" || flag == "title" || flag == "markdown" {
				fields[i+1] = "<value>"
			}
		}
	}
	return strings.Join(fields, " ")
}

func cliUsageMemorySources(task session.TaskState) []string {
	sources := []string{"task:" + task.ID}
	for _, step := range task.StepStates {
		if strings.TrimSpace(step.Tool) != "web.fetch" {
			continue
		}
		if url := strings.TrimSpace(stringValue(step.Evidence["url"])); url != "" {
			sources = append(sources, url)
		}
	}
	return dedupeStrings(sources)
}
