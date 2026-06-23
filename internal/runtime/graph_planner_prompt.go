package runtime

import (
	"fmt"
	"strings"

	"github.com/dongping/mateway/internal/agentcore"
	"github.com/dongping/mateway/internal/session"
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
- "skill": execute one registered skill as a sub-task. Use mode "skill" and set "skill" to the exact skill name.
- "human_review": pause for human review of intermediate output.
- "human_confirm": pause for human confirmation before continuing (use before mutations).

Rules:
- Every node MUST have "goal" (what it produces) and "acceptance" (verifiable completion condition).
- Use "depends" to express ordering. A node depends on another if it needs that node's output.
- SIMPLE QA → single "subtask" node, mode "direct", only one node.
- TOOL TASKS → "subtask" nodes, mode "react", with "allowed_tools" listing exact tool names. Do NOT emit a separate tool node for each file read or command.
- REGISTERED SKILL TASKS → one "skill" node with mode "skill" and the exact "skill" name. Do NOT create a separate "load_skill" subtask just to read SKILL.md.
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

Node fields (for skill nodes):
- type: "skill"
- mode: "skill"
- skill: exact registered skill name from Available skills
- goal: what the skill should accomplish
- depends/outputs/acceptance as usual

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
      "id": "evaluate-source",
      "type": "skill",
      "mode": "skill",
      "skill": "source-evaluation",
      "goal": "evaluate source quality using the registered skill",
      "depends": ["analyze"],
      "outputs": ["source_evaluation"],
      "acceptance": "evaluation follows the skill criteria"
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

const replanSystemPrompt = `You are a node-level replan generator for a task graph runtime. A single node has failed verification and exhausted its retries. Produce ONE replacement node that achieves the failed node's goal while addressing the verifier feedback.

Output exactly one JSON object. No narrative, no tool execution, no text outside the JSON.

Rules:
- Emit exactly one replacement node in "nodes"; "depends" must list the failed node's original prerequisites (provided).
- The replacement must be a SUB-TASK, never a single tool call. Use type "subtask".
- mode is "react" when the failed node had allowed_tools, otherwise "direct".
- "allowed_tools" must exactly match tool names from Available tools and be a subset of the failed node's allowed-tools whitelist.
- "goal" must describe what the replacement produces; "acceptance" must be a verifiable condition that closes the verifier feedback.
- Do NOT invent dependencies or skill names.
- Keep "id" as "repair-<failedNodeID>" (provided in the input).

Output schema:
{
  "task": {
    "goal": "<failed node goal>",
    "risk": "low|medium|high",
    "acceptance": "<rewritten acceptance>",
    "required_capabilities": {"tools": [], "skills": [], "human_gates": []},
    "final_output": {"text": true, "structured": []}
  },
  "nodes": [
    {
      "id": "repair-<failedNodeID>",
      "type": "subtask",
      "mode": "react|direct",
      "goal": "...",
      "depends": ["..."],
      "allowed_tools": ["..."],
      "outputs": ["repair_result"],
      "acceptance": "..."
    }
  ]
}`

func renderReplanPrompt(
	failedNode *session.TaskGraphNode,
	verifierFeedback string,
	siblingOutputs []siblingNodeOutput,
	tools *agentcore.ToolRegistry,
) string {
	var b strings.Builder
	b.WriteString("Failed node:\n")
	b.WriteString(fmt.Sprintf("- id: %s\n", failedNode.ID))
	b.WriteString(fmt.Sprintf("- type: %s\n", failedNode.Type))
	b.WriteString(fmt.Sprintf("- mode: %s\n", failedNode.Mode))
	b.WriteString(fmt.Sprintf("- goal: %s\n", strings.TrimSpace(failedNode.Goal)))
	if strings.TrimSpace(failedNode.FailureReason) != "" {
		b.WriteString(fmt.Sprintf("- failure_reason: %s\n", failedNode.FailureReason))
	}
	if len(failedNode.AllowedTools) > 0 {
		b.WriteString(fmt.Sprintf("- allowed_tools: %s\n", strings.Join(failedNode.AllowedTools, ", ")))
	}
	if strings.TrimSpace(failedNode.Acceptance.Criteria) != "" {
		b.WriteString(fmt.Sprintf("- acceptance_criteria: %s\n", failedNode.Acceptance.Criteria))
	}
	if summary := strings.TrimSpace(failedNode.ResultSummary); summary != "" {
		b.WriteString(fmt.Sprintf("- result_summary: %s\n", summary))
	}
	if feedback := strings.TrimSpace(verifierFeedback); feedback != "" {
		b.WriteString("\nVerifier feedback (the gap the replacement must close):\n")
		b.WriteString(feedback)
		b.WriteString("\n")
	}
	if len(siblingOutputs) > 0 {
		b.WriteString("\nSibling verified node outputs (context, not constraints):\n")
		for _, s := range siblingOutputs {
			b.WriteString(fmt.Sprintf("- %s: %s\n", s.ID, s.Summary))
		}
	}
	b.WriteString("\nReplacement depends (original prerequisites):\n")
	if len(failedNode.Depends) > 0 {
		b.WriteString(strings.Join(failedNode.Depends, ", "))
	} else {
		b.WriteString("(none)")
	}
	b.WriteString("\n\nAvailable tools:\n")
	if tools != nil {
		for _, tool := range toolsForContract(tools) {
			b.WriteString("- ")
			b.WriteString(tool.Name())
			b.WriteString(": ")
			b.WriteString(tool.Description())
			b.WriteString("\n")
		}
	}
	b.WriteString("\nEmit exactly one replacement node as JSON.\n")
	return b.String()
}

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

func renderUnifiedPlannerPrompt(goal, userText, plannerContext string, tools *agentcore.ToolRegistry, skills []discoveredSkill) string {
	var b strings.Builder
	b.WriteString("User task:\n")
	b.WriteString(strings.TrimSpace(goal))
	if strings.TrimSpace(userText) != "" && strings.TrimSpace(userText) != strings.TrimSpace(goal) {
		b.WriteString("\n\nCurrent user message:\n")
		b.WriteString(strings.TrimSpace(userText))
	}
	if context := strings.TrimSpace(plannerContext); context != "" {
		b.WriteString("\n\nPlanner context:\n")
		b.WriteString("- Use this context only to plan the task correctly. Do not treat it as completed evidence.\n")
		b.WriteString(context)
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
		b.WriteString("- Use a skill node only when the skill metadata matches the node: stage must not be planning, granularity must be subtask, and allowed_tools must be a subset of the skill metadata allowed_tools.\n")
		b.WriteString("- Skills with granularity=workflow must be decomposed into normal subtask/react nodes; do not emit them as a single skill node.\n")
		b.WriteString("- Preserve skill metadata inputs and outputs as node inputs/outputs when they are relevant to dependency planning.\n")
		for _, skill := range skills {
			b.WriteString("- name: ")
			b.WriteString(skill.Name)
			b.WriteString("\n  description: ")
			b.WriteString(strings.TrimSpace(skill.Description))
			b.WriteString("\n  stage: ")
			b.WriteString(defaultText(skill.Stage, "instruction"))
			b.WriteString("\n  type: ")
			b.WriteString(defaultText(skill.GraphType, "prompt"))
			b.WriteString("\n  granularity: ")
			b.WriteString(defaultText(skill.Granularity, "subtask"))
			if len(skill.Inputs) > 0 {
				b.WriteString("\n  inputs: ")
				b.WriteString(strings.Join(skill.Inputs, ", "))
			}
			if len(skill.Outputs) > 0 {
				b.WriteString("\n  outputs: ")
				b.WriteString(strings.Join(skill.Outputs, ", "))
			}
			if len(skill.AllowedTools) > 0 {
				b.WriteString("\n  allowed_tools: ")
				b.WriteString(strings.Join(skill.AllowedTools, ", "))
			}
			if strings.TrimSpace(skill.Usage) != "" {
				b.WriteString("\n  usage: ")
				b.WriteString(strings.TrimSpace(skill.Usage))
			}
			if len(skill.Entrypoints) > 0 {
				b.WriteString("\n  entrypoints: ")
				b.WriteString(strings.Join(skill.Entrypoints, " | "))
			}
			if len(skill.Success) > 0 {
				b.WriteString("\n  success_criteria: ")
				b.WriteString(strings.Join(skill.Success, " | "))
			}
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
