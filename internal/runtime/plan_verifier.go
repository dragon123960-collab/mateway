package runtime

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/dongping/mateway/internal/model"
	"github.com/dongping/mateway/internal/tool"
)

type PlanVerification struct {
	Warnings           []string
	RepairableWarnings []string
	Errors             []string
}

func (v PlanVerification) Blocking() bool {
	return len(v.Errors) > 0
}

func (v PlanVerification) ShouldRepair() bool {
	return v.Blocking() || len(v.RepairableWarnings) > 0
}

func (v PlanVerification) RepairGuidance() string {
	parts := make([]string, 0, len(v.Errors)+len(v.RepairableWarnings)+len(v.Warnings))
	for _, err := range v.Errors {
		parts = append(parts, "error: "+strings.TrimSpace(err))
	}
	for _, warning := range v.RepairableWarnings {
		parts = append(parts, "repairable_warning: "+strings.TrimSpace(warning))
	}
	for _, warning := range v.Warnings {
		parts = append(parts, "warning: "+strings.TrimSpace(warning))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func VerifyPlanContract(plan model.Plan, registry *tool.Registry, user string) PlanVerification {
	return verifyPlanContract(plan, registry, user, taskUnderstanding{})
}

func verifyPlanContract(plan model.Plan, registry *tool.Registry, user string, understanding taskUnderstanding) PlanVerification {
	var out PlanVerification
	seen := map[string]bool{}
	usedTools := make([]string, 0, len(plan.Steps))
	for i, step := range plan.Steps {
		label := step.ID
		if strings.TrimSpace(label) == "" {
			label = fmt.Sprintf("step-%d", i+1)
		}
		if registry == nil {
			out.Errors = append(out.Errors, label+": tool registry is unavailable")
			continue
		}
		def, ok := registry.Get(step.Tool)
		if !ok {
			out.Errors = append(out.Errors, label+": unknown tool "+step.Tool)
		} else {
			usedTools = append(usedTools, def.Name)
			for _, missing := range missingRequiredArgs(def, step.Args) {
				out.Errors = append(out.Errors, label+": missing required arg "+missing)
			}
			if placeholderCommand(step.Tool, step.Args) {
				out.Errors = append(out.Errors, label+": command contains unresolved download placeholder")
			}
			if placeholderArgs(step.Tool, step.Args) {
				out.Errors = append(out.Errors, label+": args contain unresolved placeholder values")
			}
			for _, warning := range toolBoundaryWarnings(step, def, user) {
				out.RepairableWarnings = append(out.RepairableWarnings, label+": "+warning)
			}
		}
		for _, dep := range step.DependsOn {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}
			if !seen[dep] {
				out.Errors = append(out.Errors, label+": dependency "+dep+" does not reference an earlier step")
			}
		}
		if scheduleCreateMissingSafeVerificationBoundary(step, plan.Steps) {
			out.Errors = append(out.Errors, label+": schedule.create must depend on a verification step whose on_failure is stop or ask_user")
		}
		if step.Tool != "user.ask" && len(step.ExpectedEvidence) == 0 && requiresStepEvidence(user, step) {
			out.Warnings = append(out.Warnings, label+": expected_evidence is empty")
		}
		if step.Tool != "user.ask" && len(step.SuccessCriteria) == 0 && requiresStepEvidence(user, step) {
			out.Warnings = append(out.Warnings, label+": success_criteria is empty")
		}
		if len(understanding.EvidenceHints) > 0 && !evidenceHintsMatchStep(understanding.EvidenceHints, step) {
			out.RepairableWarnings = append(out.RepairableWarnings, label+": expected_evidence does not clearly align with understanding evidence hints")
		}
		seen[step.ID] = true
	}
	for _, warning := range toolNeedsCoverageWarnings(understanding.Capabilities, usedTools) {
		out.RepairableWarnings = append(out.RepairableWarnings, warning)
	}
	for _, warning := range localCLIExecutableValidationWarnings(plan, user) {
		out.RepairableWarnings = append(out.RepairableWarnings, warning)
	}
	for _, warning := range cliMessageParameterWarnings(plan, user) {
		out.RepairableWarnings = append(out.RepairableWarnings, warning)
	}
	if len(understanding.CompletionDraft) > 0 && !successCriteriaMatchUnderstanding(understanding.CompletionDraft, plan.Steps) {
		out.RepairableWarnings = append(out.RepairableWarnings, "plan success_criteria do not clearly align with understanding completion criteria")
	}
	return out
}

func placeholderCommand(toolName string, args map[string]string) bool {
	switch strings.TrimSpace(toolName) {
	case "terminal.run", "shell.run", "software.install":
		return strings.Contains(strings.TrimSpace(args["command"]), "<下载URL>")
	default:
		return false
	}
}

func placeholderArgs(toolName string, args map[string]string) bool {
	for key, value := range args {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		lower := strings.ToLower(value)
		if strings.Contains(value, "<需从") || strings.Contains(value, "<从 step-") || strings.Contains(lower, "<download_url>") || strings.Contains(lower, "<url>") || strings.Contains(lower, "<todo>") {
			return true
		}
		if strings.Contains(value, "根据 step-") || strings.Contains(value, "根据官方") || strings.Contains(value, "官方说明填写") || strings.Contains(value, "待填写") {
			return true
		}
		if strings.TrimSpace(toolName) == "web.fetch" && key == "url" && !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
			return true
		}
	}
	return false
}

func toolBoundaryWarnings(step model.PlanStep, def tool.Definition, user string) []string {
	var warnings []string
	switch strings.TrimSpace(def.Name) {
	case "terminal.run":
		if terminalLooksLikeProjectIndex(step) {
			warnings = append(warnings, "terminal.run looks like repository or directory overview work; prefer project.index")
		}
		if terminalLooksLikeSingleFileRead(step) {
			warnings = append(warnings, "terminal.run looks like one-file reading; prefer file.read or file.summary")
		}
		if terminalHelpLooksLikeGuessedSubcommand(step, user) {
			warnings = append(warnings, "terminal.run help command looks like a guessed subcommand path; prefer the exact user-mentioned command, or inspect the parent CLI help before drilling into a subcommand")
		}
	case "file.read":
		if fileReadLooksLikeSummary(step) {
			warnings = append(warnings, "file.read looks like summary-only work; prefer file.summary unless exact full content is needed")
		}
	case "web.search":
		if webSearchLooksLikeKnownURLRead(step) {
			warnings = append(warnings, "web.search looks like reading a known URL; prefer web.fetch")
		}
		if webSearchLooksLikeSoftwareDiscovery(step) {
			warnings = append(warnings, "web.search looks like public software or install-source discovery; prefer software.search before generic web.search")
		}
	case "web.fetch":
		if webFetchLooksLikeSourceDiscovery(step) {
			warnings = append(warnings, "web.fetch looks like source discovery without a known URL; prefer web.search or software.search first")
		}
	case "software.search":
		if softwareSearchLooksLikeKnownURLRead(step) {
			warnings = append(warnings, "software.search looks like reading a known upstream page; prefer web.fetch")
		}
	case "software.install":
		if softwareInstallLooksSpeculative(step) {
			warnings = append(warnings, "software.install looks speculative; include explicit upstream install command and verify_command before installing")
		}
	}
	return warnings
}

func toolNeedsCoverageWarnings(toolNeeds []string, tools []string) []string {
	var warnings []string
	for _, need := range toolNeeds {
		if toolNeedSatisfiedByTools(need, tools) {
			continue
		}
		warnings = append(warnings, "plan tools do not clearly cover tool_need "+need)
	}
	return warnings
}

func terminalLooksLikeProjectIndex(step model.PlanStep) bool {
	command := strings.ToLower(strings.TrimSpace(step.Args["command"]))
	goal := strings.ToLower(strings.TrimSpace(step.Goal))
	text := command + " " + goal
	if !(strings.Contains(text, "tree") || strings.Contains(text, "rg --files") || strings.Contains(text, "find ") || strings.Contains(text, "ls -r") || strings.Contains(text, "file tree") || strings.Contains(text, "project overview") || strings.Contains(text, "repository map") || strings.Contains(text, "目录结构")) {
		return false
	}
	return !terminalLooksLikeSingleFileRead(step)
}

func terminalLooksLikeSingleFileRead(step model.PlanStep) bool {
	command := strings.ToLower(strings.TrimSpace(step.Args["command"]))
	goal := strings.ToLower(strings.TrimSpace(step.Goal))
	text := command + " " + goal
	if strings.Contains(text, "cat ") || strings.Contains(text, "sed -n") || strings.Contains(text, "head ") || strings.Contains(text, "tail ") {
		return true
	}
	return strings.Contains(goal, "read ") || strings.Contains(goal, "读取") || strings.Contains(goal, "查看文件")
}

func terminalHelpLooksLikeGuessedSubcommand(step model.PlanStep, user string) bool {
	command := strings.TrimSpace(step.Args["command"])
	if !strings.HasSuffix(command, " --help") {
		return false
	}
	fields := strings.Fields(command)
	if len(fields) < 3 {
		return false
	}
	userText := normalizeIntentText(user)
	if userText == "" {
		return false
	}
	root := strings.ToLower(strings.TrimSpace(fields[0]))
	normalizedRoot := strings.ReplaceAll(strings.ReplaceAll(root, "-", ""), "_", "")
	if root == "" || (!strings.Contains(userText, root) && !strings.Contains(userText, normalizedRoot)) {
		return false
	}
	for _, token := range fields[1 : len(fields)-1] {
		normalized := strings.ToLower(strings.TrimSpace(token))
		if normalized == "" || normalized == "--help" {
			continue
		}
		if strings.HasPrefix(normalized, "+") {
			normalized = strings.TrimPrefix(normalized, "+")
		}
		normalized = strings.ReplaceAll(normalized, "_", "")
		normalized = strings.ReplaceAll(normalized, "-", "")
		if normalized == "" {
			continue
		}
		if strings.Contains(userText, normalized) {
			return false
		}
		if terminalHelpTokenMatchesUserIntent(normalized, userText) {
			return false
		}
	}
	return true
}

func terminalHelpTokenMatchesUserIntent(token, userText string) bool {
	if token == "" || userText == "" {
		return false
	}
	switch {
	case token == "im" || token == "message" || token == "messages" || token == "messagessend":
		return strings.Contains(userText, "发消息") ||
			strings.Contains(userText, "发送消息") ||
			strings.Contains(userText, "消息") ||
			strings.Contains(userText, "sendmessage") ||
			strings.Contains(userText, "send")
	case strings.Contains(token, "reply"):
		return strings.Contains(userText, "回复") || strings.Contains(userText, "reply")
	case strings.Contains(token, "chat"):
		return strings.Contains(userText, "聊天") || strings.Contains(userText, "会话") || strings.Contains(userText, "chat")
	default:
		return false
	}
}

func fileReadLooksLikeSummary(step model.PlanStep) bool {
	goal := strings.ToLower(strings.TrimSpace(step.Goal))
	if goal == "" {
		return false
	}
	if strings.Contains(goal, "line") || strings.Contains(goal, "exact") || strings.Contains(goal, "full content") || strings.Contains(goal, "原文") || strings.Contains(goal, "逐行") {
		return false
	}
	return strings.Contains(goal, "summary") || strings.Contains(goal, "summarize") || strings.Contains(goal, "概览") || strings.Contains(goal, "摘要") || strings.Contains(goal, "快速了解")
}

func webSearchLooksLikeKnownURLRead(step model.PlanStep) bool {
	goal := strings.ToLower(strings.TrimSpace(step.Goal))
	query := strings.ToLower(strings.TrimSpace(step.Args["query"]))
	text := goal + " " + query
	return strings.Contains(query, "http://") || strings.Contains(query, "https://") ||
		strings.Contains(text, "known url") || strings.Contains(text, "read this page") || strings.Contains(text, "读取这个链接") || strings.Contains(text, "这个 url")
}

func webSearchLooksLikeSoftwareDiscovery(step model.PlanStep) bool {
	goal := strings.ToLower(strings.TrimSpace(step.Goal))
	query := strings.ToLower(strings.TrimSpace(step.Args["query"]))
	text := goal + " " + query
	return strings.Contains(text, "install") || strings.Contains(text, "brew ") || strings.Contains(text, "npm ") || strings.Contains(text, "pip ") ||
		strings.Contains(text, "cargo ") || strings.Contains(text, "github") || strings.Contains(text, "repo") || strings.Contains(text, "repository") ||
		strings.Contains(text, "cli") || strings.Contains(text, "命令行") || strings.Contains(text, "安装")
}

func webFetchLooksLikeSourceDiscovery(step model.PlanStep) bool {
	url := strings.TrimSpace(step.Args["url"])
	if url != "" {
		return false
	}
	goal := strings.ToLower(strings.TrimSpace(step.Goal))
	return strings.Contains(goal, "find") || strings.Contains(goal, "search") || strings.Contains(goal, "discover") ||
		strings.Contains(goal, "查找") || strings.Contains(goal, "搜索") || strings.Contains(goal, "找一下")
}

func softwareSearchLooksLikeKnownURLRead(step model.PlanStep) bool {
	goal := strings.ToLower(strings.TrimSpace(step.Goal))
	query := strings.ToLower(strings.TrimSpace(step.Args["query"]))
	text := goal + " " + query
	return strings.Contains(query, "http://") || strings.Contains(query, "https://") ||
		strings.Contains(text, "readme") || strings.Contains(text, "official docs") || strings.Contains(text, "文档内容") || strings.Contains(text, "读取页面")
}

func softwareInstallLooksSpeculative(step model.PlanStep) bool {
	command := strings.TrimSpace(step.Args["command"])
	verify := strings.TrimSpace(step.Args["verify_command"])
	return command == "" || strings.Contains(command, "<") || strings.Contains(command, "TODO") || verify == ""
}

func localCLIExecutableValidationWarnings(plan model.Plan, user string) []string {
	userText := strings.ToLower(strings.TrimSpace(user))
	if !textLooksLikeLocalCLIRequest(userText) {
		return nil
	}
	explicitNames := explicitCLIExecutableNames(user)
	seenCommandVByRoot := map[string]bool{}
	seenSourceDiscovery := false
	requiresExactLocalCheck := len(explicitNames) > 0 && textLooksLikeLocalCLIUseRequest(userText) && !textLooksLikeInstallIntent(userText)
	var warnings []string
	for i, step := range plan.Steps {
		switch strings.TrimSpace(step.Tool) {
		case "software.search", "web.search", "web.fetch":
			if requiresExactLocalCheck && !anyExplicitCommandVSeen(seenCommandVByRoot, explicitNames) {
				warnings = append(warnings, planStepLabel(step, i)+": local CLI use request should first check the exact user-provided executable with command -v before searching, installing, or switching to a canonical name")
			}
			seenSourceDiscovery = true
			continue
		case "software.install":
			if requiresExactLocalCheck && !anyExplicitCommandVSeen(seenCommandVByRoot, explicitNames) {
				warnings = append(warnings, planStepLabel(step, i)+": local CLI use request should not install before checking whether the exact user-provided executable already exists with command -v")
			}
			continue
		}
		if strings.TrimSpace(step.Tool) != "terminal.run" {
			continue
		}
		command := strings.TrimSpace(step.Args["command"])
		root := commandRoot(command)
		if root == "" || rootLooksLikeLocalProjectCommand(root) {
			continue
		}
		if terminalCommandLooksCommandVForRoot(command, root) {
			seenCommandVByRoot[root] = true
			if len(explicitNames) > 0 && !explicitNames[root] && !seenSourceDiscovery {
				warnings = append(warnings, planStepLabel(step, i)+": command existence check uses a different executable name than the user provided; first check the exact user-provided name before trying aliases or canonical names")
			}
			continue
		}
		if seenCommandVByRoot[root] {
			continue
		}
		if len(explicitNames) > 0 && !explicitNames[root] && !seenSourceDiscovery {
			warnings = append(warnings, planStepLabel(step, i)+": terminal.run uses a rewritten executable name before evidence; first check the exact user-provided CLI name, and only switch names after local or upstream evidence confirms the canonical executable")
			continue
		}
		if terminalCommandLooksCLIReadinessPreflight(command) || terminalCommandLooksExternalWriteAction(command) {
			warnings = append(warnings, planStepLabel(step, i)+": before using a local CLI, first verify the executable exists with command -v for that exact command name; if it is missing, stop and tell the user or use software.search/web evidence to find the canonical command")
		}
	}
	return warnings
}

func textLooksLikeLocalCLIRequest(text string) bool {
	return strings.Contains(text, "cli") ||
		strings.Contains(text, "命令") ||
		strings.Contains(text, "本机") ||
		strings.Contains(text, "本地执行") ||
		strings.Contains(text, "command")
}

func textLooksLikeLocalCLIUseRequest(text string) bool {
	return strings.Contains(text, "本机") ||
		strings.Contains(text, "本地执行") ||
		strings.Contains(text, "local") ||
		strings.Contains(text, "用本机")
}

func textLooksLikeInstallIntent(text string) bool {
	return strings.Contains(text, "安装") ||
		strings.Contains(text, "装一下") ||
		strings.Contains(text, "install")
}

func anyExplicitCommandVSeen(seen map[string]bool, explicit map[string]bool) bool {
	for name := range explicit {
		if seen[name] {
			return true
		}
	}
	return false
}

func explicitCLIExecutableNames(user string) map[string]bool {
	out := map[string]bool{}
	matches := cliExecutableNamePattern.FindAllString(user, -1)
	for _, match := range matches {
		name := strings.ToLower(strings.TrimSpace(match))
		if name == "" || strings.HasPrefix(name, "-") {
			continue
		}
		if strings.Contains(name, "/") {
			continue
		}
		if strings.Contains(name, "cli") || strings.Contains(name, "ctl") {
			out[name] = true
		}
	}
	return out
}

var cliExecutableNamePattern = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9._-]*(?:cli|ctl)(?:[A-Za-z0-9._-]*)?`)

func terminalCommandLooksCommandVForRoot(command, root string) bool {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) < 3 {
		return false
	}
	if fields[0] != "command" || fields[1] != "-v" {
		return false
	}
	checked := strings.Trim(strings.TrimSpace(fields[2]), `'"`)
	return strings.EqualFold(checked, root)
}

func rootLooksLikeLocalProjectCommand(root string) bool {
	switch strings.ToLower(strings.TrimSpace(root)) {
	case "go", "git", "make", "npm", "pnpm", "yarn", "node", "python", "python3", "ruby", "cargo", "mateway", "./mateway", "./build/mateway":
		return true
	default:
		return false
	}
}

func commandRoot(command string) string {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return ""
	}
	if len(fields) >= 3 && fields[0] == "command" && fields[1] == "-v" {
		return strings.ToLower(strings.Trim(strings.TrimSpace(fields[2]), `'"`))
	}
	return strings.ToLower(strings.Trim(strings.TrimSpace(fields[0]), `'"`))
}

func planStepLabel(step model.PlanStep, index int) string {
	label := strings.TrimSpace(step.ID)
	if label == "" {
		label = fmt.Sprintf("step-%d", index+1)
	}
	return label
}

func terminalCommandLooksCLIReadinessPreflight(command string) bool {
	lower := strings.ToLower(strings.TrimSpace(command))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "--dry-run") ||
		strings.HasSuffix(lower, " --help") ||
		strings.HasSuffix(lower, " -h") ||
		strings.HasSuffix(lower, " --version") ||
		strings.Contains(lower, " auth list") ||
		strings.Contains(lower, " profile list") ||
		strings.Contains(lower, " config show") ||
		strings.Contains(lower, " config current") ||
		strings.Contains(lower, " status") ||
		strings.Contains(lower, " doctor") ||
		strings.Contains(lower, " whoami")
}

func terminalCommandLooksExternalWriteAction(command string) bool {
	lower := strings.ToLower(strings.TrimSpace(command))
	if lower == "" {
		return false
	}
	if strings.Contains(lower, "--dry-run") {
		return false
	}
	writeHints := []string{
		" send", "-send", "+send",
		" reply", "-reply", "+reply",
		" create", "-create", "+create",
		" update", "-update", "+update",
		" delete", "-delete", "+delete",
		" remove", "-remove", "+remove",
		" rm ",
		" install ", " install",
		" publish", "-publish", "+publish",
		" deploy", "-deploy", "+deploy",
		" upload", "-upload", "+upload",
		" apply", "-apply", "+apply",
		" write", "-write", "+write",
		" patch", "-patch", "+patch",
		" commit", "-commit", "+commit",
		" resume", "-resume", "+resume",
		" pause", "-pause", "+pause",
	}
	for _, hint := range writeHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

func cliMessageParameterWarnings(plan model.Plan, user string) []string {
	userText := normalizeIntentText(user)
	if !strings.Contains(userText, "消息") && !strings.Contains(userText, "send") && !strings.Contains(userText, "chatid") && !strings.Contains(userText, "chat_id") {
		return nil
	}
	allowImplicitBody := strings.Contains(userText, "发一条测试消息") || strings.Contains(userText, "测试消息")
	hasTarget := strings.Contains(userText, "oc_") || strings.Contains(userText, "ou_") || strings.Contains(userText, "chatid") || strings.Contains(userText, "chat_id") || strings.Contains(userText, "userid") || strings.Contains(userText, "user_id")
	hasBody := strings.Contains(userText, "内容") || strings.Contains(userText, "正文") || strings.Contains(userText, "text") || strings.Contains(userText, "markdown") || strings.Contains(userText, "测试消息") || strings.Contains(userText, "消息是")
	wantSend := false
	hasHelpStep := false
	hasAskStep := false
	for i, step := range plan.Steps {
		if strings.TrimSpace(step.Tool) == "user.ask" {
			hasAskStep = true
			continue
		}
		if strings.TrimSpace(step.Tool) != "terminal.run" {
			continue
		}
		command := strings.ToLower(strings.TrimSpace(step.Args["command"]))
		if !terminalCommandLooksMessageSend(command) {
			continue
		}
		if terminalCommandLooksHelp(command) {
			hasHelpStep = true
			continue
		}
		wantSend = true
		label := strings.TrimSpace(step.ID)
		if label == "" {
			label = fmt.Sprintf("step-%d", i+1)
		}
		if !hasHelpStep {
			return []string{label + ": before executing CLI message send, first inspect the exact help or usage for the send command"}
		}
		if !hasTarget && !hasAskStep {
			return []string{label + ": sending a CLI message without an explicit target should ask the user for the missing chat/user id before executing"}
		}
		if !hasBody && !allowImplicitBody && !hasAskStep {
			return []string{label + ": sending a CLI message without explicit message content should ask the user for missing parameters before executing"}
		}
		return nil
	}
	if !wantSend {
		return nil
	}
	if hasTarget && (hasBody || allowImplicitBody) {
		return nil
	}
	return nil
}

func safeDiagnosticPrefixForBlockedPlan(plan model.Plan, user string) (model.Plan, bool) {
	userText := strings.ToLower(strings.TrimSpace(user))
	if !textLooksLikeLocalCLIUseRequest(userText) {
		return model.Plan{}, false
	}
	explicitNames := explicitCLIExecutableNames(user)
	if len(explicitNames) == 0 || len(plan.Steps) == 0 {
		return model.Plan{}, false
	}
	var prefix []model.PlanStep
	for _, step := range plan.Steps {
		if !isSafeExactCommandVDiagnosticStep(step, explicitNames) {
			break
		}
		prefix = append(prefix, step)
	}
	if len(prefix) == 0 || len(prefix) == len(plan.Steps) {
		return model.Plan{}, false
	}
	out := plan
	out.Steps = append([]model.PlanStep(nil), prefix...)
	return out, true
}

func isSafeExactCommandVDiagnosticStep(step model.PlanStep, explicitNames map[string]bool) bool {
	if strings.TrimSpace(step.Tool) != "terminal.run" {
		return false
	}
	command := strings.TrimSpace(step.Args["command"])
	root := commandRoot(command)
	if root == "" || !explicitNames[root] {
		return false
	}
	return terminalCommandLooksCommandVForRoot(command, root) && !terminalCommandLooksExternalWriteAction(command)
}

func terminalCommandLooksMessageSend(command string) bool {
	lower := strings.ToLower(strings.TrimSpace(command))
	return strings.Contains(lower, "send") || strings.Contains(lower, "message")
}

func terminalCommandLooksHelp(command string) bool {
	lower := strings.ToLower(strings.TrimSpace(command))
	return strings.HasSuffix(lower, " --help") || strings.HasSuffix(lower, " -h") || strings.Contains(lower, " help")
}

func scheduleCreateMissingSafeVerificationBoundary(step model.PlanStep, steps []model.PlanStep) bool {
	if strings.TrimSpace(step.Tool) != "schedule.create" || len(step.DependsOn) == 0 {
		return false
	}
	byID := map[string]model.PlanStep{}
	for _, item := range steps {
		byID[strings.TrimSpace(item.ID)] = item
	}
	for _, dep := range step.DependsOn {
		item, ok := byID[strings.TrimSpace(dep)]
		if !ok {
			continue
		}
		if !isVerificationLikeStep(item) {
			continue
		}
		switch strings.TrimSpace(item.OnFailure) {
		case "stop", "ask_user":
			return false
		default:
			return true
		}
	}
	return false
}

func isVerificationLikeStep(step model.PlanStep) bool {
	toolName := strings.TrimSpace(step.Tool)
	if toolName == "terminal.run" || toolName == "web.fetch" || toolName == "software.install" {
		return true
	}
	goal := strings.ToLower(strings.TrimSpace(step.Goal))
	return strings.Contains(goal, "验证") || strings.Contains(goal, "verify") || strings.Contains(goal, "确认是否可执行")
}

func toolNeedSatisfiedByTools(need string, tools []string) bool {
	for _, name := range tools {
		if name == need {
			return true
		}
	}
	return false
}

func evidenceHintsMatchStep(hints []string, step model.PlanStep) bool {
	if len(step.ExpectedEvidence) == 0 {
		return false
	}
	text := strings.ToLower(strings.Join(step.ExpectedEvidence, " "))
	for _, hint := range hints {
		hint = strings.ToLower(strings.TrimSpace(hint))
		switch {
		case strings.Contains(hint, "file path") && (strings.Contains(text, "file") || strings.Contains(text, "path")):
			return true
		case strings.Contains(hint, "line") && strings.Contains(text, "line"):
			return true
		case strings.Contains(hint, "query") && strings.Contains(text, "query"):
			return true
		case strings.Contains(hint, "result count") && strings.Contains(text, "result"):
			return true
		case strings.Contains(hint, "install command") && strings.Contains(text, "install"):
			return true
		case strings.Contains(hint, "verify command") && strings.Contains(text, "verify"):
			return true
		case strings.Contains(hint, "task id") && strings.Contains(text, "task"):
			return true
		case strings.Contains(hint, "exit code") && strings.Contains(text, "exit"):
			return true
		}
	}
	return false
}

func successCriteriaMatchUnderstanding(criteria []string, steps []model.PlanStep) bool {
	if len(criteria) == 0 {
		return true
	}
	var textParts []string
	for _, step := range steps {
		textParts = append(textParts, step.SuccessCriteria...)
		textParts = append(textParts, step.Goal)
	}
	text := strings.ToLower(strings.Join(textParts, " "))
	for _, criterion := range criteria {
		criterion = strings.ToLower(strings.TrimSpace(criterion))
		switch {
		case strings.Contains(criterion, "grounded evidence") && strings.Contains(text, "evidence"):
			return true
		case strings.Contains(criterion, "verify") && strings.Contains(text, "verify"):
			return true
		case strings.Contains(criterion, "summarize") && strings.Contains(text, "summar"):
			return true
		case strings.Contains(criterion, "install") && strings.Contains(text, "install"):
			return true
		case strings.Contains(criterion, "file") && strings.Contains(text, "file"):
			return true
		}
	}
	return false
}

func missingRequiredArgs(def tool.Definition, args map[string]string) []string {
	required := requiredArgsForTool(def.Name)
	var missing []string
	for _, name := range required {
		if strings.TrimSpace(args[name]) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

func requiredArgsForTool(name string) []string {
	switch name {
	case "file.read", "file.summary":
		return []string{"path"}
	case "file.write":
		return []string{"path", "content"}
	case "file.patch":
		return []string{"path"}
	case "shell.run", "terminal.run":
		return []string{"command"}
	case "web.search", "skill.search", "memory.search", "software.search":
		return []string{"query"}
	case "web.fetch":
		return []string{"url"}
	case "skill.install":
		return []string{"name"}
	case "software.install":
		return []string{"command"}
	case "schedule.create":
		return []string{"title", "prompt"}
	case "schedule.show", "schedule.pause", "schedule.resume", "schedule.delete":
		return []string{"id"}
	default:
		return nil
	}
}

func requiresStepEvidence(user string, step model.PlanStep) bool {
	if strings.TrimSpace(step.Tool) == "" {
		return false
	}
	switch step.Tool {
	case "time.now", "config.summary", "user.ask":
		return false
	}
	if strings.Contains(step.Tool, ".") {
		return true
	}
	return requiresGroundingEvidence(user)
}

type StepVerification struct {
	Warnings []string
	Errors   []string
}

func (v StepVerification) Blocking() bool {
	return len(v.Errors) > 0
}

func VerifyStepResult(step model.PlanStep, result model.ToolResult) StepVerification {
	return verifyStepResult(step, result)
}

func verifyStepResult(step model.PlanStep, result model.ToolResult) StepVerification {
	var out StepVerification
	if !result.OK {
		return out
	}
	if strings.TrimSpace(result.Output) == "" {
		out.Errors = append(out.Errors, "tool returned empty output")
	}
	if len(step.ExpectedEvidence) > 0 && !resultHasEvidence(result) {
		out.Errors = append(out.Errors, "expected evidence was not returned")
	}
	if !evidenceMatchesStep(step, result) {
		out.Errors = append(out.Errors, "returned evidence does not match expected evidence")
	}
	return out
}

func resultHasEvidence(result model.ToolResult) bool {
	if len(result.Evidence) == 0 {
		return false
	}
	if kind, _ := result.Evidence["kind"].(string); strings.TrimSpace(kind) != "" {
		return true
	}
	return len(result.Evidence) > 0
}

func evidenceMatchesStep(step model.PlanStep, result model.ToolResult) bool {
	if len(step.ExpectedEvidence) == 0 || len(result.Evidence) == 0 {
		return true
	}
	text := strings.ToLower(strings.Join(step.ExpectedEvidence, " "))
	kind, _ := result.Evidence["kind"].(string)
	switch {
	case strings.TrimSpace(kind) == "web_fetch":
		return evidenceHasAny(result.Evidence, "url", "title", "status", "bytes")
	case strings.Contains(text, "file") || strings.Contains(text, "path") || strings.Contains(text, "line"):
		return evidenceHasAny(result.Evidence, "path", "target_path", "start_line", "end_line")
	case strings.Contains(text, "url") || strings.Contains(text, "search"):
		return evidenceHasAny(result.Evidence, "url", "source_url", "query", "result_count")
	case strings.Contains(text, "install") || strings.Contains(text, "verify"):
		return evidenceHasAny(result.Evidence, "verified", "verify_command", "install_url", "target_path")
	case strings.Contains(text, "task") || strings.Contains(text, "schedule"):
		return evidenceHasAny(result.Evidence, "task_id", "id", "path")
	default:
		return strings.TrimSpace(kind) != ""
	}
}

func evidenceHasAny(evidence map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := evidence[key]; ok {
			return true
		}
	}
	return false
}
