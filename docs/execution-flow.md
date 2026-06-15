# Execution Flow

Mateway's main flow is:

```text
inbound message
  -> active task steering or new task
  -> task contract
  -> optional plan review
  -> selected skill preflight
  -> AgentCore ReAct loop
  -> tool evidence and plan item updates
  -> completion evaluator
  -> final answer or blocker
```

## 1. Message And Task

Gateway and channel adapters normalize inbound messages. Runtime either routes the message into an active task or starts a new `TaskNode`.

Short follow-ups can reuse previous task context. Independent new tasks should not receive weak previous-task prompt context.

## 2. Contract Planning

Runtime creates a lightweight `TaskContract`:

- direct tasks get a minimal plan shape
- low-risk tool tasks can auto-execute
- complex or risky tasks can pause for plan review

The contract expresses a tool execution checklist and an acceptance checklist. Required tools must be real tool names. Selected skills are recorded separately.

## 3. Skill Preflight

Planning can discover local `SKILL.md` headers and select relevant execution skills. Selected skills can be read before execution and converted into real tool plan items.

Execution does not receive the whole skill catalog by default. It receives only selected task skills or explicit skill/workflow context.

## 4. ReAct Execution

The model runs in the normal AgentCore loop. It can call visible tools, receive observations, and decide the next step from transcript context.

Mateway does not replay the plan mechanically. Hooks and evaluator enforce the task contract while preserving a small loop.

## 5. Tool Evidence

Tool results update task steps, execution events, evidence summaries, and plan item status. Large tool outputs can be compacted and retrieved later through `toolresult.read`.

Secret-like data is redacted before persistent storage and before later model turns.

## 6. Completion Evaluation

Before final answer, the completion evaluator checks:

- required tools were accepted
- required evidence exists or has a valid substitute
- required plan items are completed or blocked
- unavailable tools produce a concrete blocker

If the task is not done, the model receives a short follow-up:

```text
Missing: <requirement>. Next required action: call <tool> or state blocker.
```

Final output should report the result, deliverable path/URL when relevant, or a concrete blocker.
