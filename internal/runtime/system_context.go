package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentprofile"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/config"
	"github.com/dongping/mateway/internal/session"
)

func buildRuntimeSystemContext(cfg *config.Root, profile config.AgentProfileConfig) string {
	var b strings.Builder
	b.WriteString("Runtime context:\n")
	loc, timezone := cfg.TimezoneLocation()
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	b.WriteString("- Current date: ")
	b.WriteString(today)
	b.WriteString("\n")
	b.WriteString("- Current time: ")
	b.WriteString(now.Format("15:04"))
	b.WriteString(" ")
	b.WriteString(timezone)
	b.WriteString("\n")
	b.WriteString("- Operating system: ")
	b.WriteString(runtime.GOOS)
	b.WriteString("/")
	b.WriteString(runtime.GOARCH)
	b.WriteString("\n")
	b.WriteString("- Executable environment: local Mateway Go process with CLI and Feishu channel support.\n")
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		b.WriteString("- Current working directory: ")
		b.WriteString(cwd)
		b.WriteString("\n")
	}
	if cfg != nil {
		writeContextLine(&b, "- Mateway home: ", cfg.App.Home)
		writeContextLine(&b, "- Workspace root: ", cfg.App.Workspace)
		b.WriteString(fmt.Sprintf("- Security: enforce_workspace_paths=%v, terminal_sandbox=%v\n", cfg.Security.EnforceWorkspacePaths, cfg.Security.TerminalSandbox.Enabled))
		if len(cfg.Search.ProviderOrder) > 0 {
			b.WriteString("- Web search provider order: ")
			b.WriteString(strings.Join(cfg.Search.ProviderOrder, ", "))
			b.WriteString("\n")
		}
	}
	b.WriteString("\nTask freshness policy:\n")
	b.WriteString("- First decide whether the task needs real-time or external information.\n")
	b.WriteString("- Use web.search or web.fetch for weather, news, prices, schedules, software versions, laws, APIs, or anything likely to have changed.\n")
	b.WriteString("- When building search queries for today/current/latest tasks, use the current date above exactly; do not silently substitute an older year.\n")
	b.WriteString("- Do not present stale dated search results as current. If sources disagree or are old, say so clearly.\n")
	b.WriteString("- Prefer official and primary sources when available; otherwise summarize uncertainty.\n")
	b.WriteString("\nMateway self-knowledge policy:\n")
	b.WriteString("- When the user asks about Mateway's own configuration, sandbox, security, tools, CLI commands, or architecture, always read docs/ or internal/config/ first before using web.search.\n")
	b.WriteString("- Web search results about mateway are likely about unrelated projects with the same name; prefer local doc evidence.\n")
	b.WriteString("\nConnector gap policy:\n")
	b.WriteString("- When a task needs a missing connector such as mail, SSH, or publishing, do not simply stop at \"not supported\".\n")
	b.WriteString("- First use safe local inspection with terminal.run when useful: check available CLIs, local app configuration, config files, and documented commands without exposing secrets.\n")
	b.WriteString("- If the user asks for SSH/server/service status and has a configured local command or host alias, try terminal.run with the smallest safe command before asking the user to run it manually. Treat \"do not write a script\" as permission for a one-shot command unless they explicitly forbid terminal execution.\n")
	b.WriteString("- If no configured connector exists, propose a concrete script or integration plan with required inputs, safety boundaries, and verification commands.\n")
	b.WriteString("- Static runtime context is not proof that a scripting runtime exists. Before creating Python, Node, shell, or other scripts, verify the required executable with command -v and a version command.\n")
	b.WriteString("- Do not claim that email was sent, remote servers were checked, or content was published unless an actual tool or script completed that action.\n")
	if workspace := workspaceProfileContext(cfg, profile); workspace != "" {
		b.WriteString("\nWorkspace profile context:\n")
		b.WriteString(workspace)
	}
	if skills := skillsPrompt(discoverSkillsForAgent(cfg, profile.ID, 12)); skills != "" {
		b.WriteString("\n\n")
		b.WriteString(skills)
	}
	return strings.TrimSpace(b.String())
}

func buildRuntimeSystemContextForMessage(cfg *config.Root, profile config.AgentProfileConfig, msg channel.InboundMessage) string {
	if strings.TrimSpace(msg.Channel) == "" && strings.TrimSpace(msg.ThreadID) == "" && strings.TrimSpace(msg.SessionKey) == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("Current channel context:\n")
	writeContextLine(&b, "- channel: ", msg.Channel)
	writeContextLine(&b, "- thread_id: ", msg.ThreadID)
	writeContextLine(&b, "- user_id: ", msg.UserID)
	writeContextLine(&b, "- session_key: ", msg.SessionKey)
	b.WriteString("- Scheduled tasks are channel-neutral: schedule.manage stores the task for later execution, but the scheduler does not automatically send results back to Feishu, email, or other channels.\n")
	b.WriteString("- If a scheduled task must notify someone, make notification part of the scheduled task itself through an available tool, script, connector, or skill. If no notification channel is configured, explain the gap and ask whether to create a script or skill.\n")
	return strings.TrimSpace(b.String())
}

func prependTaskFocus(systemPrompt string, task *session.TaskNode, userText string) string {
	goal := ""
	if task != nil {
		goal = strings.TrimSpace(task.Goal)
	}
	current := strings.TrimSpace(userText)
	if mergedGoal, additional, ok := splitMergedTaskInstruction(current); ok {
		if goal == "" {
			goal = mergedGoal
		}
		current = additional
	}
	if goal == "" && current == "" {
		return strings.TrimSpace(systemPrompt)
	}
	var b strings.Builder
	b.WriteString("Current task focus:\n")
	if goal != "" {
		b.WriteString("- Original user task: ")
		b.WriteString(goal)
		b.WriteString("\n")
	}
	if current != "" && current != goal {
		b.WriteString("- Additional follow-up request: ")
		b.WriteString(current)
		b.WriteString("\n")
	}
	b.WriteString("- Before every tool call or final answer, check the next action against the original user task above.\n")
	b.WriteString("- Do not finish with a plan; continue with tools until the original task is completed or a concrete blocker/user input is required.\n")
	b.WriteString("- A message like \"I will check now\" or \"let me confirm\" is not a final answer. If you say you will check, confirm, create, update, or inspect something, call the required tool in the same turn.\n")
	b.WriteString("- If file.read is blocked by path policy while the task still requires local file evidence, try an allowed safe-read terminal.run fallback such as ls, find, cat, sed, or rg. If that fallback is also blocked, state the concrete path-policy blocker instead of inventing content.\n")
	b.WriteString("- Do not replace server/service/process status requests with software release or project status research unless the user explicitly asks for versions or releases.\n")
	b.WriteString("- For long tasks, work in verifiable stages, preserve completed stage evidence, and summarize progress between stages when possible.\n")
	b.WriteString("- If a tool is slow, cancelled, or timed out, name the tool, elapsed time, and fallback action instead of asking the user to keep waiting.\n")
	b.WriteString("- When creating or updating long-term memory pages under workspace/memory/agents/<agent>/, update that agent's index.md navigation page in the same task.\n")
	if prompt := strings.TrimSpace(systemPrompt); prompt != "" {
		b.WriteString("\n")
		b.WriteString(prompt)
	}
	return strings.TrimSpace(b.String())
}

func appendPreviousTaskContext(systemPrompt string, state session.State, currentTaskID string) string {
	tasks := recentPreviousTasks(state, currentTaskID, 3)
	sessionSummary := renderSessionSummaryContext(state.Summary)
	if len(tasks) == 0 && sessionSummary == "" {
		return strings.TrimSpace(systemPrompt)
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(systemPrompt))
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	if sessionSummary != "" {
		b.WriteString(sessionSummary)
		b.WriteString("\n\n")
	}
	if len(tasks) == 0 {
		return strings.TrimSpace(b.String())
	}
	b.WriteString("Continuity judgment:\n")
	b.WriteString("- These are recent tasks from this session, not active instructions by themselves.\n")
	b.WriteString("- Use them to decide whether the current user message is likely continuing prior work, especially when the message is short, has no clear standalone object, or appears to confirm a blocker from a prior task.\n")
	b.WriteString("- If the current message is clearly a new task, ignore this context and work on the current task.\n")
	b.WriteString("- If the user likely wants to continue a previous task, continue toward that previous task's original goal rather than treating the short message as the whole goal.\n")
	for _, task := range tasks {
		b.WriteString("- title: ")
		b.WriteString(summarize(task.Goal))
		b.WriteString("\n  status: ")
		b.WriteString(defaultText(task.Status, "unknown"))
		if strings.TrimSpace(task.Summary) != "" {
			b.WriteString("\n  summary: ")
			b.WriteString(summarize(task.Summary))
		}
		if strings.TrimSpace(task.TraceID) != "" {
			b.WriteString("\n  trace_id: ")
			b.WriteString(strings.TrimSpace(task.TraceID))
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func recentPreviousTasks(state session.State, currentTaskID string, limit int) []session.TaskNode {
	if limit <= 0 {
		return nil
	}
	var out []session.TaskNode
	for i := len(state.Tasks) - 1; i >= 0 && len(out) < limit; i-- {
		task := state.Tasks[i]
		if task.ID == currentTaskID {
			continue
		}
		out = append(out, task)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func splitMergedTaskInstruction(text string) (string, string, bool) {
	const prefix = "Continue the existing task:\nOriginal task: "
	if !strings.HasPrefix(text, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(text, prefix)
	goal, additional, ok := strings.Cut(rest, "\nAdditional request: ")
	if !ok {
		return strings.TrimSpace(rest), "", true
	}
	return strings.TrimSpace(goal), strings.TrimSpace(additional), true
}

func writeContextLine(b *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	b.WriteString(label)
	b.WriteString(value)
	b.WriteString("\n")
}

func workspaceProfileContext(cfg *config.Root, profile config.AgentProfileConfig) string {
	if cfg == nil {
		return ""
	}
	workspace := strings.TrimSpace(cfg.App.Workspace)
	if workspace == "" {
		workspace = filepath.Join(cfg.App.Home, "workspace")
	}
	agentID := strings.TrimSpace(profile.ID)
	if agentID == "" {
		agentID = "main"
	}
	var paths []string
	for _, name := range agentprofile.CoreProfileFileNames() {
		paths = append(paths, filepath.Join(workspace, "agents", agentID, name))
	}
	paths = append(paths, filepath.Join(workspace, "memory", "user", "index.md"))
	var sections []string
	for _, path := range paths {
		if text := readPromptContextFile(path, 2048); text != "" {
			sections = append(sections, "From "+path+":\n"+text)
		}
	}
	return strings.Join(sections, "\n\n")
}

func readPromptContextFile(path string, limit int64) string {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() <= 0 {
		return ""
	}
	if limit <= 0 {
		limit = 2048
	}
	if info.Size() > limit {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(data))
	if text == "" || looksSensitivePromptContext(path, text) || agentprofile.UnsafePromptContext(text) {
		return ""
	}
	return text
}

func readPromptContextHead(path string, limit int64) string {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() <= 0 {
		return ""
	}
	if limit <= 0 {
		limit = 4096
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	buf := make([]byte, limit)
	n, err := file.Read(buf)
	if err != nil && n == 0 {
		return ""
	}
	text := strings.TrimSpace(string(buf[:n]))
	if text == "" || looksSensitivePromptContext(path, text) || agentprofile.UnsafePromptContext(text) {
		return ""
	}
	return text
}

func looksSensitivePromptContext(path, text string) bool {
	lower := strings.ToLower(path + "\n" + text)
	return strings.Contains(lower, "api_key") ||
		strings.Contains(lower, "app_secret") ||
		strings.Contains(lower, "token") ||
		strings.Contains(lower, "password")
}
