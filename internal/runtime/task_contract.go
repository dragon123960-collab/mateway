package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/session"
)

const taskContractSystemPrompt = `You create a lightweight completion contract and execution plan for one user task.
Return only one JSON object. Do not execute tools. Do not write narrative outside JSON.
The contract states what evidence is required before the task may be marked complete, and the plan states the smallest concrete execution steps.
Use English JSON keys and concise English values.
Use the same natural language as the user for user-visible string values: summary, expected_outcome, completion_policy, required_evidence.description, plan_items.title, and plan_items.criteria.
Preserve the user's target exactly. Do not reinterpret a server/service/process status request as a software release or project status request.

Schema:
{
  "summary": "short task summary",
  "requires_tools": true,
  "required_tools": ["web.search"],
  "required_evidence": [{"kind":"current_external_fact","tool":"web.search","description":"current weather for relevant cities and date"}],
  "plan_items": [{"id":"plan-1","title":"search current weather","status":"pending","tool":"web.search","criteria":"collect current weather for the requested city"}],
  "expected_outcome": "travel recommendation with reasoning",
  "completion_policy": "final answer must cite tool evidence or state a concrete blocker"
}

Plan item rules:
- Use id values plan-1, plan-2, ...
- status must be "pending".
- tool is the exact tool name when a tool is expected, or empty for reasoning-only work.
- criteria is a short verifiable completion condition.
- Prefer 1-4 plan items.

Set requires_tools=false only when the task can be answered from general reasoning, existing conversation context, or user-provided facts without external/current/local verification.`

func (rt Runtime) ensureTaskContract(ctx context.Context, msg channel.InboundMessage, state *session.State, task *session.TaskNode, userText string, model agentcore.Model, trace *traceRecorder) session.TaskContract {
	if task == nil {
		return session.TaskContract{}
	}
	if task.Execution.Contract != nil {
		_ = trace.write(map[string]any{"type": "task_contract_reused", "task_id": task.ID})
		return *task.Execution.Contract
	}
	if shouldSkipTaskContractModel(task.Goal, userText) {
		contract := fallbackTaskContract(task.Goal, userText)
		if strings.TrimSpace(contract.Summary) == "" {
			contract.Summary = summarize(firstNonEmpty(userText, task.Goal))
		}
		state.SetTaskContract(task.ID, contract)
		_ = trace.write(map[string]any{
			"type":           "task_contract_skipped",
			"task_id":        task.ID,
			"reason":         "simple_non_tool_turn",
			"summary":        contract.Summary,
			"requires_tools": contract.RequiresTools,
		})
		if updated := state.TaskByID(task.ID); updated != nil {
			*task = *updated
		}
		return contract
	}
	contractModel := rt.Pool.RoleModelForMessage(msg, "contract", model)
	if rt.ContractModel != nil {
		contractModel = rt.ContractModel
	}
	profile := rt.Pool.ProfileForMessage(msg)
	contract, err := rt.generateTaskContract(ctx, task, userText, contractModel, discoverSkillsForAgent(rt.Config, profile.ID, 12))
	if err != nil {
		_ = trace.write(map[string]any{"type": "task_contract_parse_failed", "task_id": task.ID, "error": err.Error()})
		contract = fallbackTaskContract(task.Goal, userText)
	}
	if strings.TrimSpace(contract.Summary) == "" {
		contract.Summary = summarize(firstNonEmpty(userText, task.Goal))
	}
	contract = strengthenTaskContract(contract, task.Goal, userText)
	state.SetTaskContract(task.ID, contract)
	_ = trace.write(map[string]any{
		"type":              "task_contract_created",
		"task_id":           task.ID,
		"summary":           contract.Summary,
		"requires_tools":    contract.RequiresTools,
		"required_tools":    contract.RequiredTools,
		"required_evidence": contract.RequiredEvidence,
		"expected_outcome":  contract.ExpectedOutcome,
	})
	if updated := state.TaskByID(task.ID); updated != nil {
		*task = *updated
	}
	return contract
}

func shouldSkipTaskContractModel(goal, userText string) bool {
	text := strings.TrimSpace(firstNonEmpty(userText, goal))
	if text == "" || len([]rune(text)) > 240 {
		return false
	}
	lower := strings.ToLower(text)
	for _, marker := range []string{
		"read ", "write ", "edit ", "create ", "delete ", "run ", "test ", "fix ", "implement ",
		"file", "repo", "repository", "project", "code", "web", "http", "https", "today", "latest",
		"weather", "price", "schedule", "news", "search", "lookup", "verify", "check", "travel",
		"decide", "answer",
	} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	for _, prefix := range []string{"hi", "hello", "thanks", "thank you", "what is ", "what are ", "how does ", "why is ", "explain "} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	if strings.HasSuffix(text, "?") {
		return len(strings.Fields(text)) <= 12
	}
	return false
}

func (rt Runtime) generateTaskContract(ctx context.Context, task *session.TaskNode, userText string, model agentcore.Model, skills []discoveredSkill) (session.TaskContract, error) {
	if model == nil {
		return fallbackTaskContract(task.Goal, userText), nil
	}
	prompt := renderTaskContractPrompt(task.Goal, userText, rt.Tools, skills)
	contractCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	reply, err := model.Next(contractCtx, agentcore.Context{
		SystemPrompt: taskContractSystemPrompt,
		Messages:     []agentcore.Message{{Role: agentcore.RoleUser, Content: prompt}},
	})
	if err != nil {
		return session.TaskContract{}, err
	}
	return parseTaskContract(reply.Content)
}

func renderTaskContractPrompt(goal, userText string, tools *agentcore.ToolRegistry, skills []discoveredSkill) string {
	var b strings.Builder
	b.WriteString("Original user task:\n")
	b.WriteString(strings.TrimSpace(goal))
	if strings.TrimSpace(userText) != "" && strings.TrimSpace(userText) != strings.TrimSpace(goal) {
		b.WriteString("\n\nCurrent user message:\n")
		b.WriteString(strings.TrimSpace(userText))
	}
	b.WriteString("\n\nRuntime freshness policy:\nUse tools for weather, news, prices, schedules, software versions, laws, APIs, local files, commands, or anything likely to have changed or requiring verification.\n")
	b.WriteString("If the user asks to inspect a local or remote machine, service, process, daemon, port, plist, systemctl unit, SSH host, or current configuration, require terminal.run evidence. Running a single command through terminal.run is not the same as writing a script file.\n")
	if skills := skillsPrompt(skills); skills != "" {
		b.WriteString("\nAvailable skills:\n")
		b.WriteString(skills)
		b.WriteString("\n")
		b.WriteString("If the task matches an available skill, require file.read evidence for that SKILL.md and require the execution tool named by the skill workflow, usually terminal.run for CLI/helper-script skills.\n")
	}
	b.WriteString("\nAvailable tools:\n")
	for _, tool := range toolsForContract(tools) {
		contract := agentcore.ContractFor(tool)
		b.WriteString("- ")
		b.WriteString(tool.Name())
		b.WriteString(": ")
		b.WriteString(tool.Description())
		if strings.TrimSpace(contract.WhenToUse) != "" {
			b.WriteString(" Use: ")
			b.WriteString(contract.WhenToUse)
		}
		if strings.TrimSpace(contract.Evidence) != "" {
			b.WriteString(" Evidence: ")
			b.WriteString(contract.Evidence)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func toolsForContract(registry *agentcore.ToolRegistry) []agentcore.Tool {
	if registry == nil {
		return nil
	}
	tools := registry.List()
	sort.SliceStable(tools, func(i, j int) bool { return tools[i].Name() < tools[j].Name() })
	return tools
}

func parseTaskContract(raw string) (session.TaskContract, error) {
	text := strings.TrimSpace(raw)
	if start := strings.Index(text, "{"); start >= 0 {
		if end := strings.LastIndex(text, "}"); end >= start {
			text = text[start : end+1]
		}
	}
	var contract session.TaskContract
	if err := json.Unmarshal([]byte(text), &contract); err != nil {
		return session.TaskContract{}, err
	}
	contract.Summary = summarize(contract.Summary)
	contract.RequiredTools = cleanStringList(contract.RequiredTools)
	contract.ExpectedOutcome = summarize(contract.ExpectedOutcome)
	contract.CompletionPolicy = summarize(contract.CompletionPolicy)
	contract.RequiredEvidence = cleanEvidenceContracts(contract.RequiredEvidence)
	contract.PlanItems = normalizePlanItems(contract.PlanItems)
	if len(contract.RequiredTools) > 0 || len(contract.RequiredEvidence) > 0 {
		contract.RequiresTools = true
	}
	if contract.RequiresTools && len(contract.PlanItems) == 0 {
		contract.PlanItems = fallbackPlanItems(firstNonEmpty(contract.Summary, contract.ExpectedOutcome), true, contract.RequiredTools)
	}
	if contract.RequiresTools && len(contract.RequiredTools) == 0 {
		for _, evidence := range contract.RequiredEvidence {
			if strings.TrimSpace(evidence.Tool) != "" {
				contract.RequiredTools = append(contract.RequiredTools, strings.TrimSpace(evidence.Tool))
			}
		}
		contract.RequiredTools = cleanStringList(contract.RequiredTools)
	}
	return contract, nil
}

func cleanStringList(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

func cleanEvidenceContracts(values []session.TaskEvidenceContract) []session.TaskEvidenceContract {
	var out []session.TaskEvidenceContract
	for _, value := range values {
		item := session.TaskEvidenceContract{
			Kind:        strings.TrimSpace(value.Kind),
			Tool:        strings.TrimSpace(value.Tool),
			Description: summarize(value.Description),
		}
		if item.Kind == "" && item.Tool == "" && item.Description == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func fallbackTaskContract(goal, userText string) session.TaskContract {
	text := firstNonEmpty(userText, goal)
	return strengthenTaskContract(session.TaskContract{
		Summary:          summarize(text),
		RequiresTools:    false,
		PlanItems:        fallbackPlanItems(text, false, nil),
		ExpectedOutcome:  "answer the user task directly from available context",
		CompletionPolicy: "final answer should address the user task or ask for required input",
		CreatedAt:        time.Now(),
	}, goal, userText)
}

func strengthenTaskContract(contract session.TaskContract, goal, userText string) session.TaskContract {
	text := strings.ToLower(strings.TrimSpace(firstNonEmpty(userText, goal)))
	if text == "" || !looksLikeCommandInspectionTask(text) {
		return contract
	}
	contract.RequiresTools = true
	contract.RequiredTools = cleanStringList(append(contract.RequiredTools, "terminal.run"))
	contract.PlanItems = ensurePlanItem(contract.PlanItems, session.TaskPlanItem{
		ID:       "plan-1",
		Title:    "inspect requested runtime state",
		Status:   "pending",
		Tool:     "terminal.run",
		Criteria: "collect command output or a concrete terminal blocker",
	})
	if strings.TrimSpace(contract.Summary) == "" || looksLikeProjectStatusSummary(contract.Summary) {
		contract.Summary = summarize(firstNonEmpty(userText, goal))
	}
	contract.RequiredEvidence = append(contract.RequiredEvidence, session.TaskEvidenceContract{
		Kind:        "local_or_remote_runtime_state",
		Tool:        "terminal.run",
		Description: "command output proving the requested machine/service/process/configuration status, or a concrete terminal blocker",
	})
	contract.RequiredEvidence = cleanEvidenceContracts(contract.RequiredEvidence)
	if strings.TrimSpace(contract.ExpectedOutcome) == "" || looksLikeProjectStatusSummary(contract.ExpectedOutcome) {
		contract.ExpectedOutcome = "status report based on terminal command evidence, or a concrete blocker"
	}
	if strings.TrimSpace(contract.CompletionPolicy) == "" {
		contract.CompletionPolicy = "run the smallest safe terminal command before final answer, unless a concrete blocker prevents it"
	}
	return contract
}

func looksLikeCommandInspectionTask(lower string) bool {
	action := containsAnyLiteral(lower,
		"status", "check", "inspect", "verify", "run ", "terminal", "ssh", "systemctl", "service ",
		"ps ", "ss ", "netstat", "launchctl", "plist", "port", "process", "daemon", "server", "config",
		"状态", "查看", "检查", "访问", "服务器", "服务", "进程", "端口", "配置", "重启",
	)
	target := containsAnyLiteral(lower,
		"ssh", "systemctl", "service ", "singbox", "sing-box", "server", "overseas", "plist", "launchctl",
		"terminal.run", "terminal", "端口", "进程", "服务", "服务器", "国外服务器", "海外服务器",
	)
	return action && target
}

func looksLikeProjectStatusSummary(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "project") || strings.Contains(lower, "release") || strings.Contains(lower, "github") || strings.Contains(lower, "version")
}

func containsAnyLiteral(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func renderTaskContractContext(contract session.TaskContract) string {
	if strings.TrimSpace(contract.Summary) == "" && !contract.RequiresTools {
		return ""
	}
	data, err := json.Marshal(contract)
	if err != nil {
		return ""
	}
	return "Task completion contract:\n" +
		"- Continue using the normal ReAct loop; this contract only gates completion.\n" +
		"- Before final answer, satisfy the required evidence or state a concrete blocker.\n" +
		"- Treat plan_items as the current task checklist; complete or block required action items before final answer.\n" +
		string(data)
}

type taskContractValidation struct {
	Satisfied bool
	Missing   []string
}

func validateTaskContract(contract session.TaskContract, task session.TaskNode) taskContractValidation {
	if !contract.RequiresTools {
		missing := missingPlanItems(contract)
		return taskContractValidation{Satisfied: len(missing) == 0, Missing: missing}
	}
	accepted := acceptedTools(task)
	var missing []string
	for _, tool := range contract.RequiredTools {
		if strings.TrimSpace(tool) == "" {
			continue
		}
		if !accepted[strings.ToLower(strings.TrimSpace(tool))] {
			missing = append(missing, "tool:"+strings.TrimSpace(tool))
		}
	}
	for _, evidence := range contract.RequiredEvidence {
		toolName := strings.TrimSpace(evidence.Tool)
		if toolName == "" {
			continue
		}
		if !accepted[strings.ToLower(toolName)] {
			desc := strings.TrimSpace(evidence.Description)
			if desc == "" {
				desc = toolName
			}
			missing = append(missing, "evidence:"+desc)
		}
	}
	missing = append(missing, missingPlanItems(contract)...)
	missing = cleanStringList(missing)
	return taskContractValidation{Satisfied: len(missing) == 0, Missing: missing}
}

func taskContractFromState(state session.State, taskID string) session.TaskContract {
	task := taskFromState(state, taskID)
	if task.Execution.Contract == nil {
		return session.TaskContract{}
	}
	return *task.Execution.Contract
}

func acceptedTools(task session.TaskNode) map[string]bool {
	out := map[string]bool{}
	for _, step := range task.Steps {
		if step.Accepted || strings.TrimSpace(step.Status) == "accepted" {
			if strings.TrimSpace(step.Tool) != "" {
				out[strings.ToLower(strings.TrimSpace(step.Tool))] = true
			}
		}
	}
	for _, event := range task.Execution.Events {
		if strings.TrimSpace(event.Type) != "tool_result" || strings.TrimSpace(event.Status) != "accepted" {
			continue
		}
		if strings.TrimSpace(event.Tool) != "" {
			out[strings.ToLower(strings.TrimSpace(event.Tool))] = true
		}
	}
	return out
}

func taskContractFollowup(missing []string) string {
	if len(missing) == 0 {
		return "The task contract is not satisfied yet. Use the smallest appropriate tool call now, or state the concrete blocker."
	}
	return fmt.Sprintf("The task contract is not satisfied yet. Missing evidence: %s. Use the smallest appropriate tool call now, or state the concrete blocker.", strings.Join(missing, "; "))
}

func taskContractFollowupWithGuidance(missing []string, failures map[string]FailureInfo) string {
	if len(missing) == 0 {
		return "The task contract is not satisfied yet. Use the smallest appropriate tool call now, or state the concrete blocker."
	}
	var parts []string
	for _, m := range missing {
		info, ok := lookupFailureGuidance(m, failures)
		if ok && info.Guidance != "" {
			parts = append(parts, m+" — "+info.Guidance)
		} else {
			parts = append(parts, m)
		}
	}
	return fmt.Sprintf("The task contract is not satisfied yet. Missing evidence: %s. Use the smallest appropriate tool call now, or state the concrete blocker.", strings.Join(parts, "; "))
}

const toolMissingPrefix = "tool:"

func lookupFailureGuidance(missing string, failures map[string]FailureInfo) (FailureInfo, bool) {
	if strings.HasPrefix(missing, toolMissingPrefix) {
		toolName := strings.TrimPrefix(missing, toolMissingPrefix)
		if info, ok := failures[toolName]; ok {
			return info, true
		}
	}
	if info, ok := failures[missing]; ok {
		return info, true
	}
	return FailureInfo{}, false
}

func normalizePlanItems(items []session.TaskPlanItem) []session.TaskPlanItem {
	if len(items) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]session.TaskPlanItem, 0, len(items))
	for i, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = fmt.Sprintf("plan-%d", i+1)
		}
		key := strings.ToLower(id)
		if seen[key] {
			continue
		}
		status := normalizePlanStatus(item.Status)
		if status == "" {
			status = "pending"
		}
		title := summarize(item.Title)
		if title == "" {
			title = summarize(item.Criteria)
		}
		if title == "" {
			continue
		}
		seen[key] = true
		out = append(out, session.TaskPlanItem{
			ID:        id,
			Title:     title,
			Status:    status,
			Tool:      strings.TrimSpace(item.Tool),
			Criteria:  summarize(item.Criteria),
			Evidence:  summarize(item.Evidence),
			UpdatedAt: item.UpdatedAt,
		})
	}
	return out
}

func normalizePlanStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending", "running", "completed", "blocked":
		return strings.ToLower(strings.TrimSpace(status))
	default:
		return ""
	}
}

func fallbackPlanItems(text string, requiresTools bool, tools []string) []session.TaskPlanItem {
	title := summarize(text)
	if title == "" {
		title = "answer the user task"
	}
	tool := ""
	if requiresTools && len(tools) > 0 {
		tool = strings.TrimSpace(tools[0])
	}
	return []session.TaskPlanItem{{
		ID:       "plan-1",
		Title:    title,
		Status:   "pending",
		Tool:     tool,
		Criteria: "address the user request with available context or required evidence",
	}}
}

func ensurePlanItem(items []session.TaskPlanItem, item session.TaskPlanItem) []session.TaskPlanItem {
	for _, existing := range items {
		if item.Tool != "" && strings.EqualFold(existing.Tool, item.Tool) {
			return normalizePlanItems(items)
		}
	}
	ids := map[string]bool{}
	for _, existing := range items {
		ids[strings.ToLower(strings.TrimSpace(existing.ID))] = true
	}
	if ids[strings.ToLower(strings.TrimSpace(item.ID))] {
		item.ID = fmt.Sprintf("plan-%d", len(items)+1)
	}
	return normalizePlanItems(append(items, item))
}

func missingPlanItems(contract session.TaskContract) []string {
	var missing []string
	for _, item := range contract.PlanItems {
		if strings.TrimSpace(item.Tool) == "" {
			continue
		}
		switch normalizePlanStatus(item.Status) {
		case "completed", "blocked":
			continue
		default:
			label := strings.TrimSpace(item.Title)
			if label == "" {
				label = strings.TrimSpace(item.ID)
			}
			missing = append(missing, "plan:"+label)
		}
	}
	return missing
}

func renderTaskPlanForReview(contract session.TaskContract, userText string) string {
	if prefersChinese(userText, contract.Summary) {
		return renderTaskPlanForReviewZH(contract, false)
	}
	return renderTaskPlanForReviewEN(contract, false)
}

func renderTaskPlanForExecution(contract session.TaskContract, userText string) string {
	if prefersChinese(userText, contract.Summary) {
		return renderTaskPlanForReviewZH(contract, true)
	}
	return renderTaskPlanForReviewEN(contract, true)
}

func renderTaskPlanForReviewEN(contract session.TaskContract, includeItems bool) string {
	var b strings.Builder
	b.WriteString("Task plan:\n")
	if strings.TrimSpace(contract.Summary) != "" {
		b.WriteString("- Summary: ")
		b.WriteString(contract.Summary)
		b.WriteString("\n")
	}
	if strings.TrimSpace(contract.ExpectedOutcome) != "" {
		b.WriteString("- Expected outcome: ")
		b.WriteString(contract.ExpectedOutcome)
		b.WriteString("\n")
	}
	if len(contract.RequiredTools) > 0 {
		b.WriteString("- Required tools: ")
		b.WriteString(strings.Join(contract.RequiredTools, ", "))
		b.WriteString("\n")
	}
	if includeItems && len(contract.PlanItems) > 0 {
		b.WriteString("\nPlan:\n")
		for _, item := range contract.PlanItems {
			b.WriteString("- ")
			b.WriteString(item.Title)
			if strings.TrimSpace(item.Tool) != "" {
				b.WriteString(" [")
				b.WriteString(strings.TrimSpace(item.Tool))
				b.WriteString("]")
			}
			if strings.TrimSpace(item.Criteria) != "" {
				b.WriteString(": ")
				b.WriteString(item.Criteria)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\nReply 1 to execute, 2 to replan, or describe what to change.")
	return strings.TrimSpace(b.String())
}

func renderTaskPlanForReviewZH(contract session.TaskContract, includeItems bool) string {
	var b strings.Builder
	b.WriteString("任务计划：\n")
	if strings.TrimSpace(contract.Summary) != "" {
		b.WriteString("- 摘要：")
		b.WriteString(contract.Summary)
		b.WriteString("\n")
	}
	if strings.TrimSpace(contract.ExpectedOutcome) != "" {
		b.WriteString("- 预期结果：")
		b.WriteString(contract.ExpectedOutcome)
		b.WriteString("\n")
	}
	if len(contract.RequiredTools) > 0 {
		b.WriteString("- 需要工具：")
		b.WriteString(strings.Join(contract.RequiredTools, ", "))
		b.WriteString("\n")
	}
	if includeItems && len(contract.PlanItems) > 0 {
		b.WriteString("\n计划：\n")
		for _, item := range contract.PlanItems {
			b.WriteString("- ")
			b.WriteString(item.Title)
			if strings.TrimSpace(item.Tool) != "" {
				b.WriteString(" [")
				b.WriteString(strings.TrimSpace(item.Tool))
				b.WriteString("]")
			}
			if strings.TrimSpace(item.Criteria) != "" {
				b.WriteString("：")
				b.WriteString(item.Criteria)
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n回复 1 执行，回复 2 重新规划，或直接说明你想调整的地方。")
	return strings.TrimSpace(b.String())
}

func prefersChinese(values ...string) bool {
	for _, value := range values {
		for _, r := range value {
			if r >= '\u4e00' && r <= '\u9fff' {
				return true
			}
		}
	}
	return false
}
