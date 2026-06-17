package runtime

import (
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
)

const graphPlannerSystemPrompt = `You are a task graph planner. Your job is to break a user task into an ordered list of atomic execution nodes with dependencies.

Output exactly one JSON object. Do NOT execute tools. Do NOT write narrative, reasoning, or shell scripts outside the JSON.

Node types:
- "model": a reasoning/LLM step. No executor needed.
- "tool": a single tool invocation. Must include executor (exact tool name from Available tools below).
- "skill": a skill workflow. Must include executor (skill path) or an input.skill field.
- "human_review": pauses for human review of intermediate output.
- "human_confirm": pauses for human confirmation before continuing.

Rules:
- Every node MUST be atomic: one tool call, one reasoning step, or one human interaction. Do NOT hide multi-step workflows inside a single node.
- Every node MUST have a clear "goal" describing what it produces.
- Use "depends" to express ordering. A node depends on another node if it needs that node's output.
- For simple fact/QA tasks, emit a single "model" node.
- For tool tasks, emit "tool" nodes followed by a "model" node that synthesizes results.
- For high-risk tasks (file writes, deletes, production deployments), insert "human_review" or "human_confirm" nodes.
- If the user explicitly asks for confirmation, approval, review, or permission before an action, the graph MUST include a "human_confirm" or "human_review" node before that action. A "model" node must not ask the user for approval.
- Skill names are NOT tool names. Put skill references in executor or input.skill, never claim a skill name is a tool name.
- Tool nodes MUST include an "input" object whose keys match the selected tool schema exactly.
- Do NOT use free-form tool args such as {"goal": "..."} unless the tool schema actually requires "goal".
- Do NOT decide parallelism; the runtime handles that.
- Risk must be one of "low", "medium", or "high".
- IDs should be short descriptive labels (lowercase, no spaces).

Output schema:
{
  "goal": "user-visible task summary",
  "risk": "low|medium|high",
  "nodes": [
    {
      "id": "collect-files",
      "type": "tool",
      "goal": "collect repository files",
      "depends": [],
      "executor": "file.read",
      "input": {"path": "/absolute/path/to/file"},
      "outputs": ["file summary"],
      "acceptance": "repository files are collected and summarized"
    },
    {
      "id": "analyze",
      "type": "model",
      "goal": "analyze collected files",
      "depends": ["collect-files"],
      "input": {"context": "file summary"},
      "outputs": ["analysis result"],
      "acceptance": "analysis covers key architectural patterns"
    }
  ],
  "task_acceptance": "final answer includes architecture summary and identified risks"
}`

func renderGraphPlannerPrompt(goal, userText string, tools *agentcore.ToolRegistry, skills []discoveredSkill) string {
	var b strings.Builder
	b.WriteString("User task:\n")
	b.WriteString(strings.TrimSpace(goal))
	if strings.TrimSpace(userText) != "" && strings.TrimSpace(userText) != strings.TrimSpace(goal) {
		b.WriteString("\n\nCurrent user message:\n")
		b.WriteString(strings.TrimSpace(userText))
	}

	b.WriteString("\n\nGuidance:\n")
	b.WriteString("- Break the task into atomic nodes with dependencies.\n")
	b.WriteString("- Use exact tool names from Available tools below as executors for tool nodes.\n")
	b.WriteString("- For every tool node, fill input with exact JSON keys required by that tool schema.\n")
	b.WriteString("- For simple Q&A, emit a single model node.\n")
	b.WriteString("- For high-risk operations, include human_review or human_confirm nodes.\n")
	b.WriteString("- If the user asks for confirmation, approval, review, or permission before an action, include a human_confirm or human_review node before that action; never use a model node to ask for approval.\n")

	if len(skills) > 0 {
		b.WriteString("\nAvailable skills:\n")
		b.WriteString("- Skill names are instructional references, NOT executable tools. Do NOT put skill names in executor unless the skill can be invoked directly.\n")
		for _, skill := range skills {
			b.WriteString("- name: ")
			b.WriteString(skill.Name)
			b.WriteString("\n  description: ")
			b.WriteString(strings.TrimSpace(skill.Description))
			b.WriteString("\n  stage: ")
			b.WriteString(defaultText(skill.Stage, "instruction"))
			b.WriteString("\n  path: ")
			b.WriteString(skill.Path)
			if hint := executionHint(skill); hint != "" {
				b.WriteString("\n  execution_hint: ")
				b.WriteString(hint)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("\nAvailable tools:\n")
	for _, tool := range toolsForContract(tools) {
		contract := agentcore.ContractFor(tool)
		schema := tool.Schema()
		b.WriteString("- ")
		b.WriteString(tool.Name())
		b.WriteString(": ")
		b.WriteString(tool.Description())
		if len(schema.Required) > 0 {
			b.WriteString(" Required input keys: ")
			b.WriteString(strings.Join(schema.Required, ", "))
			b.WriteString(".")
		}
		if strings.TrimSpace(contract.WhenToUse) != "" {
			b.WriteString(" Use: ")
			b.WriteString(contract.WhenToUse)
		}
		b.WriteString("\n")
	}
	return b.String()
}
