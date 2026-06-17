package runtime

import (
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
)

const graphPlannerSystemPrompt = `You are a task graph planner. Your job is to break a user task into an ordered list of atomic execution nodes with dependencies.

Output exactly one JSON object. Do NOT execute tools. Do NOT write narrative, reasoning, or shell scripts outside the JSON.

Node types:
- "subtask": a verifiable sub-task with acceptance criteria.
  * mode "direct": a single model reasoning step (simple Q&A, synthesis, text generation).
  * mode "react": a complex sub-task where the executor reasons and uses tools iteratively inside the node.
- "human_review": pauses for human review of intermediate output.
- "human_confirm": pauses for human confirmation before continuing.

Rules:
- A node is a SUB-TASK, NOT a single tool call. Tool calls happen inside react nodes.
- Every node MUST have a clear "goal" describing what it produces.
- Every node MUST have "acceptance": a verifiable condition that must be met for the node to be complete.
- Use "depends" to express ordering. A node depends on another node if it needs that node's output.
- For simple fact/QA tasks, emit a single "subtask" node with mode "direct".
- For tasks requiring tool interactions (file read, terminal, web search), emit "subtask" nodes with mode "react" and populate "allowed_tools" with the exact tool names the node may use.
- Do NOT emit individual "tool" nodes for each file read or command. Group related work into react subtask nodes.
- For high-risk operations (file writes, deletes, production deploys), insert "human_confirm" nodes before the mutation.
- If the user explicitly asks for confirmation, approval, review, or permission before an action, the graph MUST include a "human_confirm" or "human_review" node before that action.
- Skill names are NOT tool names. Put skill references in "skill" field, never claim a skill name is a tool name.
- Tool names in "allowed_tools" or "task.required_capabilities.tools" MUST match exactly the tool names from Available tools below.
- Do NOT decide parallelism; the runtime handles that.
- Risk must be one of "low", "medium", or "high".
- IDs should be short descriptive labels (lowercase, no spaces).
- Task-level "required_capabilities.tools" should list all tools the overall task needs.
- Task-level "required_capabilities.skills" should list any skills the task requires.
- Task-level "acceptance" describes the overall task completion condition.

Output schema:
{
  "task": {
    "goal": "user-visible task summary",
    "risk": "low|medium|high",
    "acceptance": "overall task completion condition",
    "required_capabilities": {
      "tools": ["file.read", "terminal.run"],
      "skills": [],
      "human_gates": []
    },
    "final_output": {
      "text": true,
      "structured": ["summary", "files_modified"]
    }
  },
  "nodes": [
    {
      "id": "analyze-codebase",
      "type": "subtask",
      "mode": "react",
      "goal": "analyze repository structure and entrypoints",
      "depends": [],
      "allowed_tools": ["file.read", "terminal.run"],
      "inputs": ["repo_path"],
      "outputs": ["architecture_summary"],
      "acceptance": "covers entrypoints, modules, run commands, and risks"
    },
    {
      "id": "synthesize-answer",
      "type": "subtask",
      "mode": "direct",
      "goal": "synthesize analysis into final answer",
      "depends": ["analyze-codebase"],
      "input": {"context": "architecture_summary"},
      "outputs": ["final_answer"],
      "acceptance": "includes architecture summary and identified risks"
    }
  ]
}`

const unifiedPlannerSystemPrompt = `You are a task graph planner. Your job is to produce a TaskGraphPlan: task-level acceptance AND a subtask DAG in a single JSON output.

Output exactly one JSON object. Do NOT execute tools. Do NOT write narrative, reasoning, or shell scripts outside the JSON.

Core principle: a node is a verifiable SUB-TASK, NOT a single tool call. Tool calls happen INSIDE react nodes.

Node types:
- "subtask": a verifiable sub-task.
  * mode "direct": a single model reasoning step. No tools used. For simple Q&A, synthesis, text generation.
  * mode "react": the node executor reasons and uses tools iteratively INSIDE the node. "allowed_tools" lists which tools this node may call.
- "human_review": pause for human review of intermediate output.
- "human_confirm": pause for human confirmation before continuing (use before mutations).

Rules:
- Every node MUST have "goal" (what it produces) and "acceptance" (verifiable completion condition).
- Use "depends" to express ordering. A node depends on another if it needs that node's output.
- SIMPLE QA → single "subtask" node, mode "direct", only one node.
- TOOL TASKS → "subtask" nodes, mode "react", with "allowed_tools" listing exact tool names. Do NOT emit a separate tool node for each file read or command.
- HIGH RISK (file writes, deletes, deploys) → insert "human_confirm" node before the mutation node.
- If the user explicitly asks for confirmation/approval/review/permission, include a human_confirm or human_review node before that action.
- Skill names are NOT tool names. Put skill references in the node's "skill" field or in task.required_capabilities.skills.
- "allowed_tools" tool names MUST exactly match Available tools.
- Do NOT decide parallelism; the runtime handles it.
- Risk must be "low", "medium", or "high".
- IDs: short descriptive labels, lowercase, no spaces.

Task-level fields:
- task.goal: user-visible summary.
- task.risk: low|medium|high.
- task.acceptance: overall task completion condition.
- task.required_capabilities.tools: all tools the task needs (must be from Available tools).
- task.required_capabilities.skills: all skills the task needs.
- task.required_capabilities.human_gates: label human gate requirements.
- task.final_output: shape of the final output (text, structured fields).

Node fields (for subtask/react nodes):
- id: short label
- type: "subtask"
- mode: "react" or "direct"
- goal: what the node produces
- depends: list of prerequisite node IDs
- allowed_tools: exact tool names this node may use (only for react mode)
- inputs: conceptual input labels
- outputs: conceptual output labels
- acceptance: verifiable completion condition

Output exactly this JSON shape:
{
  "task": {
    "goal": "...",
    "risk": "low|medium|high",
    "acceptance": "...",
    "required_capabilities": {
      "tools": ["tool.name"],
      "skills": ["skill-name"],
      "human_gates": ["confirm-before-write"]
    },
    "final_output": {
      "text": true,
      "structured": ["summary"]
    }
  },
  "nodes": [
    {
      "id": "analyze",
      "type": "subtask",
      "mode": "react",
      "goal": "analyze the repository",
      "depends": [],
      "allowed_tools": ["file.read"],
      "inputs": ["repo_path"],
      "outputs": ["analysis"],
      "acceptance": "includes entrypoints, modules, and risks"
    },
    {
      "id": "answer",
      "type": "subtask",
      "mode": "direct",
      "goal": "produce final answer",
      "depends": ["analyze"],
      "outputs": ["final_answer"],
      "acceptance": "covers all findings with concrete recommendations"
    }
  ]
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

func renderUnifiedPlannerPrompt(goal, userText string, tools *agentcore.ToolRegistry, skills []discoveredSkill) string {
	var b strings.Builder
	b.WriteString("User task:\n")
	b.WriteString(strings.TrimSpace(goal))
	if strings.TrimSpace(userText) != "" && strings.TrimSpace(userText) != strings.TrimSpace(goal) {
		b.WriteString("\n\nCurrent user message:\n")
		b.WriteString(strings.TrimSpace(userText))
	}

	b.WriteString("\n\nGuidance:\n")
	b.WriteString("- Break the task into verifiable sub-task nodes. A node is a SUB-TASK, NOT a single tool call.\n")
	b.WriteString("- For simple Q&A, emit a single subtask node with mode direct.\n")
	b.WriteString("- For tasks requiring tools, emit subtask nodes with mode react and populate allowed_tools.\n")
	b.WriteString("- Tool calls happen inside react nodes; do NOT emit individual tool nodes.\n")
	b.WriteString("- For high-risk operations, include human_confirm nodes before mutations.\n")
	b.WriteString("- If the user asks for confirmation/approval/review/permission, include human_confirm or human_review nodes.\n")
	b.WriteString("- Populate task.required_capabilities.tools with all tools the overall task needs.\n")

	if len(skills) > 0 {
		b.WriteString("\nAvailable skills:\n")
		b.WriteString("- Skill names are instructional references, NOT executable tools. Do NOT put skill names in allowed_tools.\n")
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
