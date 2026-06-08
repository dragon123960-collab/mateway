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
		return *task.Execution.Contract
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
	return session.TaskContract{
		Summary:          summarize(text),
		RequiresTools:    false,
		ExpectedOutcome:  "answer the user task directly from available context",
		CompletionPolicy: "final answer should address the user task or ask for required input",
		CreatedAt:        time.Now(),
	}
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
