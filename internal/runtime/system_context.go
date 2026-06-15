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
	return buildRuntimeSystemContextForTask(cfg, profile, "", session.TaskContract{})
}

func buildRuntimeSystemContextForTask(cfg *config.Root, profile config.AgentProfileConfig, userText string, contract session.TaskContract) string {
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
	if needsFreshnessContext(userText, contract) {
		b.WriteString("\nTask freshness policy:\n")
		b.WriteString("- Use web.search or web.fetch for weather, news, prices, schedules, software versions, laws, APIs, or anything likely to have changed.\n")
		b.WriteString("- When building search queries for today/current/latest tasks, use the current date above exactly; do not silently substitute an older year.\n")
		b.WriteString("- Do not present stale dated search results as current. Prefer official and primary sources when available.\n")
	}
	if needsMatewaySelfKnowledgeContext(userText) {
		b.WriteString("\nMateway self-knowledge policy:\n")
		b.WriteString("- When the user asks about Mateway configuration, sandbox, security, tools, CLI commands, or architecture, read local docs/ and source before using web.search.\n")
		b.WriteString("- Web search results about mateway are likely unrelated projects; prefer local evidence.\n")
	}
	if needsConnectorGapContext(userText, contract) {
		b.WriteString("\nConnector gap policy:\n")
		b.WriteString("- When a missing connector is needed, first inspect safe local CLIs, config, and documented commands before stopping.\n")
		b.WriteString("- Verify required Python, Node, shell, or other runtimes before creating scripts.\n")
		b.WriteString("- Do not claim email, server checks, publishing, or notification succeeded unless a real tool or script completed the action.\n")
	}
	if workspace := workspaceProfileContext(cfg, profile); workspace != "" {
		b.WriteString("\nWorkspace profile context:\n")
		b.WriteString(workspace)
	}
	if skills := selectedSkillsPrompt(contract); skills != "" {
		b.WriteString("\n\n")
		b.WriteString(skills)
	}
	return strings.TrimSpace(b.String())
}

func needsFreshnessContext(userText string, contract session.TaskContract) bool {
	text := strings.ToLower(userText + " " + contract.Summary + " " + contract.ExpectedOutcome + " " + contract.CompletionPolicy)
	if containsAnyLiteral(text,
		"today", "latest", "current", "real-time", "realtime", "now", "news", "weather", "price", "prices",
		"schedule", "version", "release", "api", "law", "regulation", "stock", "market", "exchange rate",
		"今天", "最新", "当前", "现在", "实时", "新闻", "天气", "价格", "股价", "汇率", "版本", "法规",
	) {
		return true
	}
	for _, evidence := range contract.RequiredEvidence {
		kind := strings.ToLower(strings.TrimSpace(evidence.Kind))
		desc := strings.ToLower(strings.TrimSpace(evidence.Description))
		if strings.Contains(kind, "current") || strings.Contains(kind, "external") || strings.Contains(desc, "current") || strings.Contains(desc, "latest") {
			return true
		}
	}
	return false
}

func needsMatewaySelfKnowledgeContext(userText string) bool {
	text := strings.ToLower(userText)
	if !strings.Contains(text, "mateway") {
		return false
	}
	return containsAnyLiteral(text,
		"config", "configuration", "sandbox", "security", "tool", "tools", "cli", "command", "architecture", "runtime", "agentcore",
		"配置", "沙箱", "安全", "工具", "命令", "架构", "运行时",
	)
}

func needsConnectorGapContext(userText string, contract session.TaskContract) bool {
	text := strings.ToLower(userText + " " + contract.Summary + " " + contract.ExpectedOutcome)
	return containsAnyLiteral(text,
		"mail", "email", "smtp", "imap", "ssh", "server", "publish", "deploy", "notify", "notification", "calendar", "saas",
		"邮件", "邮箱", "服务器", "发布", "部署", "通知", "日历", "连接器",
	)
}

func selectedSkillsPrompt(contract session.TaskContract) string {
	if len(contract.RequiredSkills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Selected task skills:\n")
	b.WriteString("- Only these contract-selected skills are relevant for execution; skill names are not tool names.\n")
	for _, skill := range contract.RequiredSkills {
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(name)
		writeInlineContext(&b, "path", skill.Path)
		writeInlineContext(&b, "reason", skill.Reason)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func writeInlineContext(b *strings.Builder, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	b.WriteString(" (")
	b.WriteString(key)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteString(")")
}

func skillsForRuntimeContext(cfg *config.Root, agentID string) []discoveredSkill {
	const runtimeSkillsLimit = 24
	skills := discoverSkillsForAgent(cfg, agentID, 0)
	if len(skills) > runtimeSkillsLimit {
		return skills[:runtimeSkillsLimit]
	}
	return skills
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
	b.WriteString("- Do not finish with only a plan; complete the task with tools or state the concrete blocker.\n")
	b.WriteString("- If you promise to check, create, update, inspect, or run something, call the required tool in the same turn.\n")
	b.WriteString("- Before final answer, satisfy the current task contract/checklist or explain why it is blocked.\n")
	if prompt := strings.TrimSpace(systemPrompt); prompt != "" {
		b.WriteString("\n")
		b.WriteString(prompt)
	}
	return strings.TrimSpace(b.String())
}

func appendPreviousTaskContext(systemPrompt string, state session.State, currentTaskID string, userText string) string {
	if !shouldInjectPreviousTaskContext(state, currentTaskID, userText) {
		return strings.TrimSpace(systemPrompt)
	}
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

func shouldInjectPreviousTaskContext(state session.State, currentTaskID string, userText string) bool {
	if strings.TrimSpace(renderSessionSummaryContext(state.Summary)) != "" && likelyFollowupText(userText) {
		return true
	}
	tasks := recentPreviousTasks(state, currentTaskID, 3)
	if len(tasks) == 0 {
		return false
	}
	return likelyFollowupText(userText)
}

func likelyFollowupText(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	if len([]rune(lower)) <= 24 {
		return true
	}
	return containsAnyFollowupPhrase(lower,
		"continue", "resume", "continue that", "resume that", "try again", "do it again",
		"ok now", "yes now", "done now", "fixed now", "approved now", "authorized now",
		"继续", "接着", "刚才", "上次", "那个", "这个", "好了", "可以了", "修复了", "授权了", "同意",
	)
}

func containsAnyFollowupPhrase(text string, phrases ...string) bool {
	for _, phrase := range phrases {
		phrase = strings.TrimSpace(phrase)
		if phrase == "" {
			continue
		}
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
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
