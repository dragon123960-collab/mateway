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

const taskContractSystemPrompt = `You create a lightweight completion contract for one user task.
Return only one JSON object. Do not execute tools. Do not write a plan.
The contract states what evidence is required before the task may be marked complete.
Use English JSON keys and concise English values.
Preserve the user's target exactly. Do not reinterpret a server/service/process status request as a software release or project status request.

Schema:
{
  "summary": "short task summary",
  "requires_tools": true,
  "required_tools": ["web.search"],
  "required_evidence": [{"kind":"current_external_fact","tool":"web.search","description":"current weather for relevant cities and date"}],
  "expected_outcome": "travel recommendation with reasoning",
  "completion_policy": "final answer must cite tool evidence or state a concrete blocker"
}

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
	contract, err := rt.generateTaskContract(ctx, task, userText, contractModel)
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

func (rt Runtime) generateTaskContract(ctx context.Context, task *session.TaskNode, userText string, model agentcore.Model) (session.TaskContract, error) {
	if model == nil {
		return fallbackTaskContract(task.Goal, userText), nil
	}
	prompt := renderTaskContractPrompt(task.Goal, userText, rt.Tools)
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

func renderTaskContractPrompt(goal, userText string, tools *agentcore.ToolRegistry) string {
	var b strings.Builder
	b.WriteString("Original user task:\n")
	b.WriteString(strings.TrimSpace(goal))
	if strings.TrimSpace(userText) != "" && strings.TrimSpace(userText) != strings.TrimSpace(goal) {
		b.WriteString("\n\nCurrent user message:\n")
		b.WriteString(strings.TrimSpace(userText))
	}
	b.WriteString("\n\nRuntime freshness policy:\nUse tools for weather, news, prices, schedules, software versions, laws, APIs, local files, commands, or anything likely to have changed or requiring verification.\n")
	b.WriteString("If the user asks to inspect a local or remote machine, service, process, daemon, port, plist, systemctl unit, SSH host, or current configuration, require terminal.run evidence. Running a single command through terminal.run is not the same as writing a script file.\n")
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
	if len(contract.RequiredTools) > 0 || len(contract.RequiredEvidence) > 0 {
		contract.RequiresTools = true
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
		string(data)
}

type taskContractValidation struct {
	Satisfied bool
	Missing   []string
}

func validateTaskContract(contract session.TaskContract, task session.TaskNode) taskContractValidation {
	if !contract.RequiresTools {
		return taskContractValidation{Satisfied: true}
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
