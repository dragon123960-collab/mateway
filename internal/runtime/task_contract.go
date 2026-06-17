package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/channel"
	"github.com/dongping/mateway/internal/session"
)

const taskContractSystemPrompt = `You create a lightweight completion contract and execution plan for one user task.
Return only one JSON object. Do not execute tools. Do not write narrative outside JSON.
The contract has two parts:
  1. Tool execution list (plan_items) — always required, even for simple Q&A. Each plan_items entry is one logical action; plan_items[].criteria is the per-step acceptance condition.
  2. Acceptance list (required_evidence) — required for tool/action tasks, may be omitted/empty for direct Q&A. Each required_evidence.description is the global acceptance condition.
Together they form the universal "tool execution list + acceptance list" shape: every task always has a plan (plan_items); tool/action tasks also carry an acceptance list (required_evidence) against which the completion evaluator judges the task done.
Use English JSON keys and concise English values.
Use the same natural language as the user for user-visible string values: summary, expected_outcome, completion_policy, required_evidence.description, plan_items.title, and plan_items.criteria.
Preserve the user's target exactly. Do not reinterpret a server/service/process status request as a software release or project status request.

CRITICAL: required_tools and plan_items[].tool must use exact tool names from "Available tools" below.
Skill names from "Available skills" are NOT tools. Put skill references in required_skills, never in required_tools or plan_items[].tool.

Schema:
{
  "summary": "short task summary",
  "requires_tools": true,
  "required_tools": ["web.search"],
  "required_skills": [{"name": "skill-name", "path": "/path/to/SKILL.md", "reason": "why this skill is needed"}],
  "required_evidence": [{"kind":"current_external_fact","tool":"web.search","description":"current weather for relevant cities and date"}],
  "plan_items": [{"id":"plan-1","title":"search current weather","status":"pending","tool":"web.search","criteria":"collect current weather for the requested city"}],
  "expected_outcome": "travel recommendation with reasoning",
  "completion_policy": "final answer must cite tool evidence or state a concrete blocker"
}

Plan item rules:
- Use id values plan-1, plan-2, ...
- status must be "pending".
- tool is the exact tool name from Available tools, or empty for reasoning-only work.
- criteria is a short verifiable completion condition (the per-step acceptance condition).
- Prefer 1-4 plan items.
- For direct Q&A, still emit one plan_item (tool="") so the task has a plan shape.

Acceptance list rules:
- required_evidence entries describe what evidence must be gathered before final answer.
- Each entry's description is a verifiable acceptance condition (the global acceptance condition).
- For tool tasks, mirror the tool/skill behind every required_evidence entry.
- For direct Q&A (requires_tools=false), required_evidence may be left empty. The task completes through final answer, input request, or the empty-action promise check without needing evidence entries.

Set requires_tools=false only when the task can be answered from general reasoning, existing conversation context, or user-provided facts without external/current/local verification. Direct Q&A still emits a plan_item (tool="") but may leave required_evidence empty — the task completes through final answer, input request, or the empty-action promise check.`

func (rt Runtime) ensureTaskContract(ctx context.Context, msg channel.InboundMessage, state *session.State, task *session.TaskNode, userText string, model agentcore.Model, trace *traceRecorder) session.TaskContract {
	if task == nil {
		return session.TaskContract{}
	}
	if task.Execution.Contract != nil {
		_ = trace.write(map[string]any{"type": "task_contract_reused", "task_id": task.ID})
		return *task.Execution.Contract
	}

	profile := rt.Pool.ProfileForMessage(msg)
	workspace := strings.TrimSpace(rt.Config.App.Workspace)
	if workspace == "" {
		workspace = filepath.Join(rt.Config.App.Home, "workspace")
	}
	const contractSkillsLimit = 24
	allSkills := discoverSkillsForAgent(rt.Config, profile.ID, 0)
	// Slice 6B: only let execution skills pull a direct-looking task through
	// the contract model when the user text is action-like. Plain Q&A should
	// keep the minimal direct contract even if execution skills are installed.
	hasExecutionSkill := false
	for _, s := range allSkills {
		if !isGuidanceSkillStage(s.Stage) {
			hasExecutionSkill = true
			break
		}
	}
	fallback := fallbackTaskContract(task.Goal, userText)
	fallbackStrategy := classifyContractStrategy(task.Goal, userText, fallback)
	if fallbackStrategy == contractStrategyDirect && (!hasExecutionSkill || !looksLikeActionTask(firstNonEmpty(userText, task.Goal))) {
		_ = trace.write(map[string]any{
			"type":           "task_contract_strategy",
			"task_id":        task.ID,
			"strategy":       string(fallbackStrategy),
			"summary":        fallback.Summary,
			"requires_tools": fallback.RequiresTools,
		})
		contract := fallback
		if strings.TrimSpace(contract.Summary) == "" {
			contract.Summary = summarize(firstNonEmpty(userText, task.Goal))
		}
		state.SetTaskContract(task.ID, contract)
		// Slice 6A: every task has a plan/contract shape, even direct Q&A.
		// Emit task_contract_created for the direct path so trace readers see
		// the minimal contract (plan_items) and acceptance list.
		_ = trace.write(map[string]any{
			"type":              "task_contract_created",
			"task_id":           task.ID,
			"summary":           contract.Summary,
			"requires_tools":    contract.RequiresTools,
			"required_tools":    contract.RequiredTools,
			"required_skills":   requiredSkillsWithoutBody(contract.RequiredSkills),
			"required_evidence": contract.RequiredEvidence,
			"expected_outcome":  contract.ExpectedOutcome,
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
	skills := allSkills
	if len(skills) > contractSkillsLimit {
		skills = skills[:contractSkillsLimit]
	}
	discoveredCount := len(allSkills)
	selectedCount := len(skills)
	omittedCount := discoveredCount - selectedCount
	if len(skills) > 0 || discoveredCount > 0 {
		var traceSkills []map[string]any
		for _, s := range skills {
			traceSkills = append(traceSkills, map[string]any{
				"name":     s.Name,
				"priority": skillPriority(s),
				"path":     s.Path,
				"scope":    s.Scope,
			})
		}
		_ = trace.write(map[string]any{
			"type":             "task_contract_skills_selected",
			"skills":           traceSkills,
			"selected_count":   selectedCount,
			"discovered_count": discoveredCount,
			"omitted_count":    omittedCount,
		})
	}
	contract, err := rt.generateTaskContract(ctx, task, userText, contractModel, skills)
	if err != nil {
		_ = trace.write(map[string]any{"type": "task_contract_parse_failed", "task_id": task.ID, "error": err.Error()})
		contract = fallbackTaskContract(task.Goal, userText)
	}
	if strings.TrimSpace(contract.Summary) == "" {
		contract.Summary = summarize(firstNonEmpty(userText, task.Goal))
	}
	contract = strengthenTaskContract(contract, task.Goal, userText)
	contract = repairContractSkillUsage(contract, skills)
	contract = readSelectedSkillBodies(contract, skills, workspace, profile.ID)
	contract = augmentContractWithSkillPlanItems(contract, skills)
	traceSelectedSkillBodies(trace, contract)
	validation := validateContractTools(contract, rt.Tools, skills)
	replanAttempted := false
	if !validation.IsValid() && !replanAttempted {
		_ = trace.write(map[string]any{
			"type":           "task_contract_invalid_tool",
			"task_id":        task.ID,
			"invalid_tools":  validation.InvalidTools,
			"invalid_skills": validation.InvalidSkills,
			"skill_mismatch": validation.HasSkillNameMismatch,
		})
		replanErr := contractReplanFeedback(validation, contract)
		replanContract, replanErr2 := rt.generateTaskContract(ctx, task, userText, contractModel, skills, replanErr)
		if replanErr2 == nil {
			if strings.TrimSpace(replanContract.Summary) == "" {
				replanContract.Summary = summarize(firstNonEmpty(userText, task.Goal))
			}
			replanContract = strengthenTaskContract(replanContract, task.Goal, userText)
			replanContract = repairContractSkillUsage(replanContract, skills)
			replanContract = readSelectedSkillBodies(replanContract, skills, workspace, profile.ID)
			replanContract = augmentContractWithSkillPlanItems(replanContract, skills)
			traceSelectedSkillBodies(trace, replanContract)
			contract = replanContract
			replanAttempted = true
			validation = validateContractTools(contract, rt.Tools, skills)
			_ = trace.write(map[string]any{
				"type":          "task_contract_replanned",
				"task_id":       task.ID,
				"replan_reason": "invalid_tool_detected",
				"invalid_tools": validation.InvalidTools,
			})
		}
	}
	if len(validation.InvalidTools) > 0 || len(validation.InvalidSkills) > 0 {
		reason := validation.InvalidReason()
		_ = trace.write(map[string]any{
			"type":           "task_contract_invalid_after_replan",
			"task_id":        task.ID,
			"invalid_tools":  validation.InvalidTools,
			"invalid_skills": validation.InvalidSkills,
			"reason":         reason,
			"skill_mismatch": validation.HasSkillNameMismatch,
		})
	}
	state.SetTaskContract(task.ID, contract)
	// Record the final strategy based on the fully processed contract
	finalStrategy := classifyContractStrategy(task.Goal, userText, contract)
	_ = trace.write(map[string]any{
		"type":           "task_contract_strategy",
		"task_id":        task.ID,
		"strategy":       string(finalStrategy),
		"summary":        contract.Summary,
		"requires_tools": contract.RequiresTools,
	})
	_ = trace.write(map[string]any{
		"type":              "task_contract_created",
		"task_id":           task.ID,
		"summary":           contract.Summary,
		"requires_tools":    contract.RequiresTools,
		"required_tools":    contract.RequiredTools,
		"required_skills":   contract.RequiredSkills,
		"required_evidence": contract.RequiredEvidence,
		"expected_outcome":  contract.ExpectedOutcome,
	})
	if updated := state.TaskByID(task.ID); updated != nil {
		*task = *updated
	}
	return contract
}

func (rt Runtime) generateTaskContract(ctx context.Context, task *session.TaskNode, userText string, model agentcore.Model, skills []discoveredSkill, replanFeedback ...string) (session.TaskContract, error) {
	if model == nil {
		return fallbackTaskContract(task.Goal, userText), nil
	}
	prompt := renderTaskContractPrompt(task.Goal, userText, rt.Tools, skills)
	if len(replanFeedback) > 0 && strings.TrimSpace(replanFeedback[0]) != "" {
		prompt = prompt + "\n\nContract repair needed:\n" + strings.TrimSpace(replanFeedback[0])
	}
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
	if len(skills) > 0 {
		b.WriteString("\nAvailable skills:\n")
		b.WriteString("- Skill names are instructional references, NOT executable tools. Do NOT put skill names in required_tools or plan_items[].tool.\n")
		b.WriteString("- Put a skill in required_skills only when reading and following that specific SKILL.md is required to finish the task.\n")
		b.WriteString("- Do not put planning/synthesis guidance skills in required_skills merely because they are relevant; use their descriptions as guidance instead.\n")
		b.WriteString("- For every required_skills entry, add both required_evidence tool=file.read and a plan_items entry tool=file.read that references the skill name or SKILL.md path.\n")
		b.WriteString("- After reading a skill, follow its workflow using real runtime tools listed under Available tools below.\n")
		for _, skill := range skills {
			b.WriteString("- name: ")
			b.WriteString(skill.Name)
			b.WriteString("\n  description: ")
			b.WriteString(strings.TrimSpace(skill.Description))
			b.WriteString("\n  stage: ")
			b.WriteString(defaultText(skill.Stage, "instruction"))
			b.WriteString("\n  priority: ")
			b.WriteString(defaultText(skill.Priority, "0"))
			b.WriteString("\n  path: ")
			b.WriteString(skill.Path)
			b.WriteString("\n  scope: ")
			b.WriteString(defaultText(skill.Scope, "unknown"))
			if hint := executionHint(skill); hint != "" {
				b.WriteString("\n  execution_hint: ")
				b.WriteString(hint)
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
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
	if len(contract.PlanItems) == 0 {
		// Slice 6A universal plan shape: every task carries at least one
		// plan_items entry, even direct Q&A. The model's contract may omit
		// plan_items for plain Q&A; fall back to a single empty-tool item
		// so completion evaluator and trace readers see a plan.
		contract.PlanItems = fallbackPlanItems(firstNonEmpty(contract.Summary, contract.ExpectedOutcome), contract.RequiresTools, contract.RequiredTools)
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
	contract := session.TaskContract{
		Summary:          summarize(text),
		RequiresTools:    false,
		PlanItems:        fallbackPlanItems(text, false, nil),
		ExpectedOutcome:  "answer the user task directly from available context",
		CompletionPolicy: "final answer should address the user task or ask for required input",
		CreatedAt:        time.Now(),
	}
	contract = strengthenActionTaskContract(contract, strings.ToLower(strings.TrimSpace(text)))
	return strengthenTaskContract(contract, goal, userText)
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

func strengthenActionTaskContract(contract session.TaskContract, lower string) session.TaskContract {
	if lower == "" {
		return contract
	}
	needsFreshInfo := containsAnyLiteral(lower,
		"today", "latest", "current", "recent", "price", "market", "stock", "index", "weather", "news", "schedule",
		"今天", "最新", "当前", "最近", "近三天", "近3天", "走势", "行情", "指数", "天气", "新闻",
	)
	needsLocalWrite := containsAnyLiteral(lower,
		"write", "document", "markdown", "report", "file", "整理成文档", "文档", "报告", "写入",
	)
	needsExternalPublish := containsAnyLiteral(lower,
		"feishu", "lark", "cloud doc", "send to", "publish", "飞书", "云文档", "发送", "发到",
	)
	if !needsFreshInfo && !needsLocalWrite && !needsExternalPublish {
		return contract
	}
	contract.RequiresTools = true
	if needsFreshInfo {
		contract.RequiredTools = cleanStringList(append(contract.RequiredTools, "web.search"))
		contract.RequiredEvidence = append(contract.RequiredEvidence, session.TaskEvidenceContract{
			Kind:        "current_external_fact",
			Tool:        "web.search",
			Description: "current external data with source/date, or a concrete search blocker",
		})
		contract.PlanItems = ensurePlanItem(contract.PlanItems, session.TaskPlanItem{
			ID:       "plan-1",
			Title:    "collect current information",
			Status:   "pending",
			Tool:     "web.search",
			Criteria: "collect current data with source/date, or record a concrete blocker",
		})
	}
	if needsLocalWrite || needsExternalPublish {
		contract.RequiredTools = cleanStringList(append(contract.RequiredTools, "file.write"))
		contract.RequiredEvidence = append(contract.RequiredEvidence, session.TaskEvidenceContract{
			Kind:        "local_file",
			Tool:        "file.write",
			Description: "local markdown/report file written before publishing, or a concrete file blocker",
		})
		contract.PlanItems = ensurePlanItem(contract.PlanItems, session.TaskPlanItem{
			ID:       "plan-2",
			Title:    "write local document",
			Status:   "pending",
			Tool:     "file.write",
			Criteria: "write the report to a local file, or record a concrete blocker",
		})
	}
	if needsExternalPublish {
		contract.RequiredTools = cleanStringList(append(contract.RequiredTools, "terminal.run"))
		contract.RequiredEvidence = append(contract.RequiredEvidence, session.TaskEvidenceContract{
			Kind:        "remote_publish",
			Tool:        "terminal.run",
			Description: "publish or send the document through the configured CLI/helper, or a concrete connector blocker",
		})
		contract.PlanItems = ensurePlanItem(contract.PlanItems, session.TaskPlanItem{
			ID:       "plan-3",
			Title:    "publish document",
			Status:   "pending",
			Tool:     "terminal.run",
			Criteria: "publish/send the document through the configured CLI/helper, or record a concrete blocker",
		})
	}
	contract.RequiredEvidence = cleanEvidenceContracts(contract.RequiredEvidence)
	if strings.TrimSpace(contract.ExpectedOutcome) == "" || strings.Contains(strings.ToLower(contract.ExpectedOutcome), "directly from available context") {
		contract.ExpectedOutcome = "complete the requested action with tool evidence, or state a concrete blocker"
	}
	if strings.TrimSpace(contract.CompletionPolicy) == "" || strings.Contains(strings.ToLower(contract.CompletionPolicy), "answer directly") {
		contract.CompletionPolicy = "use required tool evidence before final answer, unless a concrete blocker prevents it"
	}
	return contract
}

func repairContractSkillUsage(contract session.TaskContract, skills []discoveredSkill) session.TaskContract {
	if len(skills) == 0 {
		return contract
	}
	byName := map[string]discoveredSkill{}
	for _, skill := range skills {
		name := strings.ToLower(strings.TrimSpace(skill.Name))
		if name != "" {
			byName[name] = skill
		}
	}
	if len(byName) == 0 {
		return contract
	}
	var tools []string
	for _, tool := range contract.RequiredTools {
		name := strings.ToLower(strings.TrimSpace(tool))
		if name == "" {
			continue
		}
		if _, isSkill := byName[name]; isSkill {
			continue
		}
		tools = append(tools, tool)
	}
	contract.RequiredTools = cleanStringList(tools)
	for i := range contract.PlanItems {
		name := strings.ToLower(strings.TrimSpace(contract.PlanItems[i].Tool))
		if _, isSkill := byName[name]; isSkill {
			contract.PlanItems[i].Tool = ""
		}
	}
	for _, req := range contract.RequiredSkills {
		name := strings.ToLower(strings.TrimSpace(req.Name))
		skill, ok := byName[name]
		if !ok {
			continue
		}
		path := firstNonEmpty(req.Path, skill.Path)
		if !contractHasFileReadEvidenceForSkill(contract, req.Name, path) {
			contract.RequiredEvidence = append(contract.RequiredEvidence, session.TaskEvidenceContract{
				Kind:        "local_file",
				Tool:        "file.read",
				Description: "read " + path,
			})
		}
		if !contractHasFileReadPlanItemForSkill(contract, req.Name, path) {
			contract.RequiredTools = cleanStringList(append(contract.RequiredTools, "file.read"))
			contract.PlanItems = appendSkillReadPlanItem(contract.PlanItems, req.Name, path)
		}
	}
	contract.RequiredEvidence = cleanEvidenceContracts(contract.RequiredEvidence)
	contract.PlanItems = normalizePlanItems(contract.PlanItems)
	return contract
}

func appendSkillReadPlanItem(items []session.TaskPlanItem, skillName, skillPath string) []session.TaskPlanItem {
	id := fmt.Sprintf("plan-%d", len(items)+1)
	used := map[string]bool{}
	for _, item := range items {
		used[strings.ToLower(strings.TrimSpace(item.ID))] = true
	}
	for used[strings.ToLower(id)] {
		id = fmt.Sprintf("plan-%d", len(used)+1)
	}
	newItem := session.TaskPlanItem{
		ID:       id,
		Title:    "read " + strings.TrimSpace(skillName) + " SKILL.md",
		Status:   "pending",
		Tool:     "file.read",
		Criteria: "read " + strings.TrimSpace(skillPath),
	}
	return append([]session.TaskPlanItem{newItem}, items...)
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
	var b strings.Builder
	b.WriteString("Task completion checklist:\n")
	writeContextLine(&b, "- summary: ", contract.Summary)
	if len(contract.RequiredTools) > 0 {
		b.WriteString("- required_tools: ")
		b.WriteString(strings.Join(contract.RequiredTools, ", "))
		b.WriteString("\n")
	}
	if len(contract.RequiredSkills) > 0 {
		var skills []string
		for _, skill := range contract.RequiredSkills {
			name := strings.TrimSpace(skill.Name)
			if name == "" {
				continue
			}
			if strings.TrimSpace(skill.Path) != "" {
				name += " (" + strings.TrimSpace(skill.Path) + ")"
			}
			skills = append(skills, name)
		}
		if len(skills) > 0 {
			b.WriteString("- selected_skills: ")
			b.WriteString(strings.Join(skills, ", "))
			b.WriteString("\n")
		}
	}
	if len(contract.RequiredEvidence) > 0 {
		b.WriteString("- required_evidence:\n")
		for _, evidence := range contract.RequiredEvidence {
			desc := strings.TrimSpace(evidence.Description)
			if desc == "" {
				desc = strings.TrimSpace(evidence.Tool)
			}
			if desc == "" {
				continue
			}
			b.WriteString("  - ")
			if tool := strings.TrimSpace(evidence.Tool); tool != "" {
				b.WriteString("[")
				b.WriteString(tool)
				b.WriteString("] ")
			}
			b.WriteString(desc)
			b.WriteString("\n")
		}
	}
	if len(contract.PlanItems) > 0 {
		b.WriteString("- plan_items:\n")
		for _, item := range contract.PlanItems {
			title := strings.TrimSpace(item.Title)
			if title == "" {
				title = strings.TrimSpace(item.Criteria)
			}
			if title == "" {
				continue
			}
			status := defaultText(normalizePlanStatus(item.Status), "pending")
			b.WriteString("  - ")
			if id := strings.TrimSpace(item.ID); id != "" {
				b.WriteString(id)
				b.WriteString(" ")
			}
			b.WriteString("[")
			b.WriteString(status)
			b.WriteString("]")
			if tool := strings.TrimSpace(item.Tool); tool != "" {
				b.WriteString(" [")
				b.WriteString(tool)
				b.WriteString("]")
			}
			b.WriteString(" ")
			b.WriteString(title)
			b.WriteString("\n")
		}
		if next := nextPendingPlanItem(contract); next != "" {
			b.WriteString("- next_required_action: ")
			b.WriteString(next)
			b.WriteString("\n")
		}
	}
	writeContextLine(&b, "- expected_outcome: ", contract.ExpectedOutcome)
	writeContextLine(&b, "- completion_policy: ", contract.CompletionPolicy)
	b.WriteString("- Final answer only after the checklist is satisfied, or state the concrete blocker.\n")
	return strings.TrimSpace(b.String())
}

func nextPendingPlanItem(contract session.TaskContract) string {
	for _, item := range contract.PlanItems {
		status := normalizePlanStatus(item.Status)
		if status == "" || status == "pending" || status == "running" {
			title := strings.TrimSpace(item.Title)
			if title == "" {
				title = strings.TrimSpace(item.Criteria)
			}
			if title == "" {
				continue
			}
			if tool := strings.TrimSpace(item.Tool); tool != "" {
				return "call " + tool + " for " + title
			}
			return title
		}
	}
	return ""
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
	fetchCanBeSubstituted := fetchCanBeSubstitutedBySearch(contract, task)
	var missing []string
	for _, tool := range contract.RequiredTools {
		toolName := strings.TrimSpace(tool)
		if toolName == "" {
			continue
		}
		if strings.EqualFold(toolName, "web.fetch") && fetchCanBeSubstituted {
			continue
		}
		if !accepted[strings.ToLower(toolName)] {
			missing = append(missing, "tool:"+toolName)
		}
	}
	for _, evidence := range contract.RequiredEvidence {
		toolName := strings.TrimSpace(evidence.Tool)
		if toolName == "" {
			continue
		}
		if strings.EqualFold(toolName, "web.fetch") && fetchCanBeSubstituted {
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

func allFetchPlanItemsBlocked(contract session.TaskContract) bool {
	hasFetchItem := false
	for _, item := range contract.PlanItems {
		if !strings.EqualFold(strings.TrimSpace(item.Tool), "web.fetch") {
			continue
		}
		hasFetchItem = true
		if normalizePlanStatus(item.Status) != "blocked" {
			return false
		}
	}
	return hasFetchItem
}

func fetchCanBeSubstitutedBySearch(contract session.TaskContract, task session.TaskNode) bool {
	if !allFetchPlanItemsBlocked(contract) {
		return false
	}
	searchEvidence := acceptedEvidenceTextForTool(task, "web.search")
	if strings.TrimSpace(searchEvidence) == "" {
		return false
	}
	hasFetchRequirement := false
	for _, evidence := range contract.RequiredEvidence {
		if !strings.EqualFold(strings.TrimSpace(evidence.Tool), "web.fetch") {
			continue
		}
		hasFetchRequirement = true
		if !evidenceTextCoversRequirement(searchEvidence, evidence.Description) {
			return false
		}
	}
	if hasFetchRequirement {
		return true
	}
	for _, item := range contract.PlanItems {
		if !strings.EqualFold(strings.TrimSpace(item.Tool), "web.fetch") {
			continue
		}
		requirement := strings.TrimSpace(item.Criteria)
		if requirement == "" {
			requirement = item.Title
		}
		if !evidenceTextCoversRequirement(searchEvidence, requirement) {
			return false
		}
	}
	return true
}

func acceptedEvidenceTextForTool(task session.TaskNode, toolName string) string {
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	var parts []string
	for _, step := range task.Steps {
		if !strings.EqualFold(strings.TrimSpace(step.Tool), toolName) {
			continue
		}
		if !step.Accepted && strings.TrimSpace(step.Status) != "accepted" {
			continue
		}
		parts = append(parts, step.Summary, evidenceMapText(step.Evidence))
	}
	for _, event := range task.Execution.Events {
		if strings.TrimSpace(event.Type) != "tool_result" || strings.TrimSpace(event.Status) != "accepted" {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(event.Tool), toolName) {
			continue
		}
		parts = append(parts, event.Summary, evidenceMapText(event.Evidence))
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func evidenceMapText(evidence map[string]any) string {
	if len(evidence) == 0 {
		return ""
	}
	var parts []string
	for key, value := range evidence {
		parts = append(parts, key, fmt.Sprint(value))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func evidenceTextCoversRequirement(evidenceText, requirement string) bool {
	evidenceText = strings.ToLower(strings.TrimSpace(evidenceText))
	if evidenceText == "" {
		return false
	}
	keywords := requirementKeywords(requirement)
	if len(keywords) == 0 {
		return true
	}
	matched := 0
	for _, keyword := range keywords {
		if strings.Contains(evidenceText, keyword) {
			matched++
		}
	}
	if len(keywords) <= 2 {
		return matched == len(keywords)
	}
	return matched >= 2
}

func requirementKeywords(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, raw := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		word := strings.TrimSpace(raw)
		if len(word) < 4 || seen[word] || fetchSubstitutionStopWords[word] {
			continue
		}
		seen[word] = true
		out = append(out, word)
	}
	return out
}

var fetchSubstitutionStopWords = map[string]bool{
	"fetch":    true,
	"page":     true,
	"source":   true,
	"sources":  true,
	"result":   true,
	"results":  true,
	"data":     true,
	"with":     true,
	"from":     true,
	"that":     true,
	"this":     true,
	"current":  true,
	"external": true,
	"evidence": true,
}

func checkContractToolAvailability(agentRegistry, fullRegistry *agentcore.ToolRegistry, contract session.TaskContract) map[string]string {
	if agentRegistry == nil {
		agentRegistry = fullRegistry
	}
	if agentRegistry == nil {
		unavailable := map[string]string{}
		for _, name := range contract.RequiredTools {
			name = strings.TrimSpace(name)
			if name != "" {
				unavailable[name] = "tool registry unavailable"
			}
		}
		return unavailable
	}
	unavailable := map[string]string{}
	for _, name := range contract.RequiredTools {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := agentRegistry.Get(name); !ok {
			unavailable[name] = availabilityReason(name, agentRegistry, fullRegistry)
		}
	}
	for _, item := range contract.PlanItems {
		name := strings.TrimSpace(item.Tool)
		if name == "" {
			continue
		}
		if _, ok := agentRegistry.Get(name); !ok {
			if _, exists := unavailable[name]; !exists {
				unavailable[name] = availabilityReason(name, agentRegistry, fullRegistry)
			}
		}
	}
	return unavailable
}

func availabilityReason(toolName string, agentRegistry, fullRegistry *agentcore.ToolRegistry) string {
	if fullRegistry == nil {
		return "tool not registered"
	}
	if _, ok := fullRegistry.Get(toolName); ok {
		return "denied by profile"
	}
	return "tool not registered"
}

func contractBlockerText(contract session.TaskContract, validation taskContractValidation, rt Runtime, msg channel.InboundMessage) string {
	fullRegistry := rt.Tools
	agentRegistry := rt.Tools
	if agent := rt.Pool.AgentForMessage(msg); agent != nil && agent.Tools != nil {
		agentRegistry = agent.Tools
	}
	return renderContractBlockerText(contract, validation, agentRegistry, fullRegistry)
}

// renderContractBlockerText formats the user-facing blocker text for a
// contract that could not be satisfied. It uses structured English with
// per-tool reason annotations and intentionally avoids runtime language
// detection; the model can localize the eventual final reply via the
// system prompt.
func renderContractBlockerText(contract session.TaskContract, validation taskContractValidation, agentRegistry, fullRegistry *agentcore.ToolRegistry) string {
	if len(validation.Missing) == 0 {
		return "\n\nThe task is blocked. Review the contract requirements and profile configuration, or start a new task with /new."
	}
	if agentRegistry == nil {
		missing := strings.Join(validation.Missing, "; ")
		return fmt.Sprintf("\n\nTask contract could not be satisfied. Missing evidence: %s.\nThe task is blocked. Review the contract requirements and profile configuration, or start a new task with /new.", missing)
	}
	var parts []string
	for _, m := range validation.Missing {
		label := m
		if toolName := toolNameFromMissing(m); toolName != "" {
			if _, ok := agentRegistry.Get(toolName); !ok {
				reason := availabilityReason(toolName, agentRegistry, fullRegistry)
				label = m + " (" + reason + ")"
			}
		}
		parts = append(parts, label)
	}
	missing := strings.Join(parts, "; ")
	return fmt.Sprintf("\n\nTask contract could not be satisfied. Missing evidence: %s.\nThe task is blocked. Review the contract requirements and profile configuration, or start a new task with /new.", missing)
}

func toolNameFromMissing(missing string) string {
	if strings.HasPrefix(missing, toolMissingPrefix) {
		return strings.TrimPrefix(missing, toolMissingPrefix)
	}
	return ""
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
		return "Missing: task contract evidence. Next required action: call the smallest appropriate tool or state blocker."
	}
	return fmt.Sprintf("Missing: %s. Next required action: call %s or state blocker.", strings.Join(missing, "; "), nextToolForMissing(missing, session.TaskContract{}))
}

func taskContractFollowupWithGuidance(missing []string, failures map[string]FailureInfo, contract session.TaskContract, accepted map[string]bool) string {
	if len(missing) == 0 {
		return "Missing: task contract evidence. Next required action: call the smallest appropriate tool or state blocker."
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
	for _, skill := range contract.RequiredSkills {
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			continue
		}
		if requiredSkillReadCompleted(contract, skill) {
			parts = append(parts, fmt.Sprintf("skill:%s read", name))
		} else {
			parts = append(parts, fmt.Sprintf("skill:%s needs file.read SKILL.md", name))
		}
	}
	return fmt.Sprintf("Missing: %s. Next required action: call %s or state blocker.", strings.Join(parts, "; "), nextToolForMissing(missing, contract))
}

func nextToolForMissing(missing []string, contract session.TaskContract) string {
	for _, item := range contract.PlanItems {
		status := normalizePlanStatus(item.Status)
		if status != "" && status != "pending" && status != "running" {
			continue
		}
		if tool := strings.TrimSpace(item.Tool); tool != "" {
			return tool
		}
	}
	for _, m := range missing {
		if tool := toolNameFromMissing(m); tool != "" {
			return tool
		}
	}
	for _, evidence := range contract.RequiredEvidence {
		if tool := strings.TrimSpace(evidence.Tool); tool != "" {
			return tool
		}
	}
	return "the smallest appropriate tool"
}

func requiredSkillReadCompleted(contract session.TaskContract, skill session.RequiredSkill) bool {
	for _, item := range contract.PlanItems {
		if normalizePlanStatus(item.Status) != "completed" {
			continue
		}
		if fileReadPlanItemMatchesSkill(item, skill.Name, skill.Path) {
			return true
		}
	}
	return false
}

func requiredSkillReadCompletedWithSteps(steps []session.TaskStep, contract session.TaskContract, skill session.RequiredSkill) bool {
	if requiredSkillReadCompleted(contract, skill) {
		return true
	}
	return skillStepReadAccepted(skill, steps)
}

func skillStepReadAccepted(skill session.RequiredSkill, steps []session.TaskStep) bool {
	needle := strings.ToLower(strings.TrimSpace(skill.Path))
	if needle == "" {
		needle = strings.ToLower(strings.TrimSpace(skill.Name))
	}
	for _, step := range steps {
		if !strings.EqualFold(step.Tool, "file.read") || !step.Accepted {
			continue
		}
		if needle == "" {
			return true
		}
		summary := strings.ToLower(step.Summary)
		if strings.Contains(summary, needle) {
			return true
		}
	}
	return false
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
	// Slice 6A: plan review must show the ordered tool checklist (plan_items)
	// alongside skills, expected outcome, and acceptance criteria.
	return renderTaskPlanForReviewEN(contract, true)
}

func renderTaskPlanForExecution(contract session.TaskContract, userText string) string {
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
	if len(contract.RequiredSkills) > 0 {
		b.WriteString("- Required skills: ")
		for i, s := range contract.RequiredSkills {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(strings.TrimSpace(s.Name))
			if strings.TrimSpace(s.Reason) != "" {
				b.WriteString(" (")
				b.WriteString(strings.TrimSpace(s.Reason))
				b.WriteString(")")
			}
		}
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
	if len(contract.RequiredEvidence) > 0 {
		b.WriteString("\nAcceptance criteria:\n")
		for _, ev := range contract.RequiredEvidence {
			b.WriteString("- ")
			if tool := strings.TrimSpace(ev.Tool); tool != "" {
				b.WriteString("[")
				b.WriteString(tool)
				b.WriteString("] ")
			}
			if kind := strings.TrimSpace(ev.Kind); kind != "" {
				b.WriteString(kind)
				b.WriteString(": ")
			}
			b.WriteString(strings.TrimSpace(ev.Description))
			b.WriteString("\n")
		}
	}
	if strings.TrimSpace(contract.CompletionPolicy) != "" {
		b.WriteString("\nCompletion policy: ")
		b.WriteString(contract.CompletionPolicy)
		b.WriteString("\n")
	}
	b.WriteString("\nReply 1 to execute, 2 to replan, or describe what to change.")
	return strings.TrimSpace(b.String())
}

type contractToolValidation struct {
	InvalidTools         []string
	InvalidSkills        []string
	HasSkillNameMismatch bool
	SkillNames           map[string]string
}

func addInvalidContractExecutionEvent(state *session.State, taskID string, validation contractToolValidation) {
	if state == nil {
		return
	}
	state.AddExecutionEvent(taskID, session.ExecutionEvent{
		Type:    "task_contract_invalid",
		Status:  "failed",
		Summary: validation.InvalidReason(),
		Evidence: map[string]any{
			"invalid_tools":  validation.InvalidTools,
			"invalid_skills": validation.InvalidSkills,
			"skill_mismatch": validation.HasSkillNameMismatch,
		},
	})
}

func (v contractToolValidation) IsValid() bool {
	return len(v.InvalidTools) == 0 && len(v.InvalidSkills) == 0
}

func (v contractToolValidation) InvalidReason() string {
	if v.HasSkillNameMismatch {
		return "skill name used as tool"
	}
	if len(v.InvalidSkills) > 0 {
		return "required skill lacks file.read evidence"
	}
	return "tool not registered"
}

func validateContractTools(contract session.TaskContract, registry *agentcore.ToolRegistry, skills []discoveredSkill) contractToolValidation {
	v := contractToolValidation{}
	skillNames := map[string]string{}
	for _, s := range skills {
		skillNames[strings.ToLower(strings.TrimSpace(s.Name))] = s.Path
	}
	v.SkillNames = skillNames

	seen := map[string]bool{}
	for _, name := range contract.RequiredTools {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if _, ok := registry.Get(name); !ok {
			v.InvalidTools = append(v.InvalidTools, name)
			if _, isSkill := skillNames[strings.ToLower(name)]; isSkill {
				v.HasSkillNameMismatch = true
			}
		}
	}
	for _, item := range contract.PlanItems {
		name := strings.TrimSpace(item.Tool)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if _, ok := registry.Get(name); !ok {
			v.InvalidTools = append(v.InvalidTools, name)
			if _, isSkill := skillNames[strings.ToLower(name)]; isSkill {
				v.HasSkillNameMismatch = true
			}
		}
	}

	for _, skill := range contract.RequiredSkills {
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			continue
		}
		hasEvidence := contractHasFileReadEvidenceForSkill(contract, name, skill.Path)
		hasPlanItem := contractHasFileReadPlanItemForSkill(contract, name, skill.Path)
		if !hasEvidence && !hasPlanItem {
			v.InvalidSkills = append(v.InvalidSkills, name+": missing file.read evidence and plan item for SKILL.md")
		} else if !hasEvidence {
			v.InvalidSkills = append(v.InvalidSkills, name+": missing file.read evidence for SKILL.md")
		} else if !hasPlanItem {
			v.InvalidSkills = append(v.InvalidSkills, name+": missing file.read plan item for SKILL.md")
		}
	}

	return v
}

func contractHasFileReadEvidenceForSkill(contract session.TaskContract, skillName, skillPath string) bool {
	for _, ev := range contract.RequiredEvidence {
		if strings.EqualFold(strings.TrimSpace(ev.Tool), "file.read") {
			desc := strings.ToLower(strings.TrimSpace(ev.Description))
			needle := strings.ToLower(strings.TrimSpace(skillPath))
			if needle != "" && strings.Contains(desc, needle) {
				return true
			}
			if strings.Contains(desc, "skill") && strings.Contains(desc, strings.ToLower(skillName)) {
				return true
			}
		}
	}
	return false
}

func contractHasFileReadPlanItemForSkill(contract session.TaskContract, skillName, skillPath string) bool {
	for _, item := range contract.PlanItems {
		if fileReadPlanItemMatchesSkill(item, skillName, skillPath) {
			return true
		}
	}
	return false
}

func fileReadPlanItemMatchesSkill(item session.TaskPlanItem, skillName, skillPath string) bool {
	if !strings.EqualFold(strings.TrimSpace(item.Tool), "file.read") {
		return false
	}
	criteria := strings.ToLower(strings.TrimSpace(item.Criteria))
	title := strings.ToLower(strings.TrimSpace(item.Title))
	needle := strings.ToLower(strings.TrimSpace(skillPath))
	if needle != "" && (strings.Contains(criteria, needle) || strings.Contains(title, needle)) {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(skillName))
	if name == "" {
		return false
	}
	return (strings.Contains(criteria, "skill") || strings.Contains(title, "skill")) &&
		(strings.Contains(criteria, name) || strings.Contains(title, name))
}

func contractReplanFeedback(v contractToolValidation, contract session.TaskContract) string {
	var b strings.Builder
	hasTools := len(v.InvalidTools) > 0
	hasSkills := len(v.InvalidSkills) > 0

	if hasTools {
		b.WriteString("The contract contains invalid tool names. ")
		b.WriteString("The following names are not registered tools")
		if v.HasSkillNameMismatch {
			b.WriteString(" and appear to be skill names, not tools")
		}
		b.WriteString(": ")
		b.WriteString(strings.Join(v.InvalidTools, ", "))
		b.WriteString(".\n")
		if v.HasSkillNameMismatch {
			b.WriteString("Skill names go in required_skills, not required_tools or plan_items[].tool. ")
			b.WriteString("For skill usage, require file.read to read the SKILL.md, and require the execution tool (e.g. terminal.run) for CLI/helper-script skills.\n")
		}
		b.WriteString("Please regenerate the contract using ONLY tool names from the Available tools list.")
	}

	if hasSkills {
		if hasTools {
			b.WriteString(" ")
		}
		b.WriteString("The contract has required_skills without corresponding file.read evidence and/or plan_items. ")
		b.WriteString("Each required skill must include both: (1) required_evidence with kind=local_file tool=file.read referencing that skill's SKILL.md path, and (2) a plan_items entry with tool=file.read whose title or criteria references the same skill name or SKILL.md path. ")
		b.WriteString("Missing skill read requirements for: ")
		b.WriteString(strings.Join(v.InvalidSkills, "; "))
		b.WriteString(".\n")
		b.WriteString("Add the file.read plan item before any execution step that follows the skill workflow.")
	}

	if !hasTools && !hasSkills {
		b.WriteString("Please regenerate the contract using ONLY tool names from the Available tools list.")
	}
	return b.String()
}

func invalidContractBlockerText(contract session.TaskContract, v contractToolValidation, msg channel.InboundMessage) string {
	return invalidContractBlockerEN(contract, v)
}

func invalidContractBlockerENWithRegistries(contract session.TaskContract, v contractToolValidation, agentRegistry, fullRegistry *agentcore.ToolRegistry) string {
	if len(v.InvalidTools) == 0 || agentRegistry == nil {
		return invalidContractBlockerEN(contract, v)
	}
	withReasons := v
	withReasons.InvalidTools = make([]string, 0, len(v.InvalidTools))
	for _, name := range v.InvalidTools {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		reason := availabilityReason(name, agentRegistry, fullRegistry)
		withReasons.InvalidTools = append(withReasons.InvalidTools, name+" ("+reason+")")
	}
	return invalidContractBlockerEN(contract, withReasons)
}

func traceSelectedSkillBodies(trace *traceRecorder, contract session.TaskContract) {
	if trace == nil {
		return
	}
	count := 0
	for _, skill := range contract.RequiredSkills {
		if skill.Body != "" {
			count++
		}
	}
	if count == 0 {
		return
	}
	var skillInfos []map[string]any
	for _, skill := range contract.RequiredSkills {
		if skill.Body == "" {
			continue
		}
		skillInfos = append(skillInfos, map[string]any{
			"name":     skill.Name,
			"path":     skill.Path,
			"body_len": len(skill.Body),
		})
	}
	_ = trace.write(map[string]any{
		"type":         "task_contract_skill_read",
		"read_count":   count,
		"total_skills": len(contract.RequiredSkills),
		"skills":       skillInfos,
	})
}

func requiredSkillsWithoutBody(skills []session.RequiredSkill) []session.RequiredSkill {
	if len(skills) == 0 {
		return nil
	}
	out := make([]session.RequiredSkill, len(skills))
	for i, s := range skills {
		out[i] = session.RequiredSkill{
			Name:   s.Name,
			Path:   s.Path,
			Reason: s.Reason,
		}
	}
	return out
}

func invalidContractBlockerEN(contract session.TaskContract, v contractToolValidation) string {
	var b strings.Builder
	b.WriteString("\n\nTask contract could not be satisfied.")

	hasTools := len(v.InvalidTools) > 0
	hasSkills := len(v.InvalidSkills) > 0

	if hasTools {
		if v.HasSkillNameMismatch {
			b.WriteString(" The following names appear to be skill names, not registered tools")
		} else {
			b.WriteString(" The following tools are not registered")
		}
		b.WriteString(": ")
		b.WriteString(strings.Join(v.InvalidTools, ", "))
		b.WriteString(".\n")
		if v.HasSkillNameMismatch {
			b.WriteString("Skill names belong in required_skills, not in required_tools or plan_items[].tool. ")
			b.WriteString("For each required skill, add file.read evidence and a file.read plan item for the SKILL.md file.\n")
		}
	}

	if hasSkills {
		b.WriteString(" Required skills lack file.read evidence and/or file.read plan items: ")
		b.WriteString(strings.Join(v.InvalidSkills, "; "))
		b.WriteString(".\n")
		b.WriteString("Each required skill must have corresponding file.read evidence and a plan_items entry for its SKILL.md.\n")
	}

	b.WriteString("The task is blocked. Review the contract requirements and profile configuration, or start a new task with /new.")
	return b.String()
}
