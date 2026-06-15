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
- Skill names are NOT tool names. Put skill references in executor or input.skill, never claim a skill name is a tool name.
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
      "inputs": ["workspace path"],
      "outputs": ["file summary"],
      "acceptance": "repository files are collected and summarized"
    },
    {
      "id": "analyze",
      "type": "model",
      "goal": "analyze collected files",
      "depends": ["collect-files"],
      "inputs": ["file summary"],
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
	b.WriteString("- For simple Q&A, emit a single model node.\n")
	b.WriteString("- For high-risk operations, include human_review or human_confirm nodes.\n")

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
		b.WriteString("- ")
		b.WriteString(tool.Name())
		b.WriteString(": ")
		b.WriteString(tool.Description())
		if strings.TrimSpace(contract.WhenToUse) != "" {
			b.WriteString(" Use: ")
			b.WriteString(contract.WhenToUse)
		}
		b.WriteString("\n")
	}
	return b.String()
}
