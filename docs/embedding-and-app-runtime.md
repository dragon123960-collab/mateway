# Embedding And App Runtime

Mateway can act as a local agent runtime kernel under applications such as Electron apps, desktop tools, CLIs, bots, scheduled jobs and external schedulers.

## App Boundary

An application owns UI, domain data, user interaction and presentation. Mateway owns task planning, node execution, tool policy, trace, recovery, verifier and memory observe.

```text
Application UI / Service
  -> Mateway task request
  -> TaskGraph Runtime
  -> progress / trace / structured output
  -> Application presentation
```

## Recommended Integration Shape

A stable embedding API should expose these semantics, regardless of whether transport is CLI, local HTTP, JSON-RPC, Unix socket or embedded process:

- run task with structured input
- continue task with user input for pending actions
- fork a new task from a previous task/node/evidence reference
- stream progress and trace events
- get task status and graph state
- get final text and structured output
- cancel or block task
- read trace by task/session id

The transport can evolve without changing the runtime kernel model.

## Structured Input And Output

Applications should not depend only on natural-language final replies. A task may return:

```json
{
  "text": "Human-readable answer",
  "data": {
    "domain_result": "structured value"
  },
  "trace_id": "...",
  "task_id": "..."
}
```

Planner and node outputs should preserve structured fields when the domain requires them.

## Task History And Forking

Applications should treat historical continuation as task lineage, not session mutation. If a user continues an unfinished task, Mateway resumes the existing graph state. If a user starts from a completed task, node, or evidence item, the application should create a new task with parent/context refs.

This lets an Electron or domain app show a tree of historical work without turning the runtime session into a long-lived tree database.

## Domain Skill Packages

Domain applications should package deterministic rules, prompts and references as skills instead of forking runtime code.

Example layout:

```text
workspace/skills/meihua-yishu/
  SKILL.md
  .mateway/metadata.yaml
  scripts/calculate_hexagram.ts
  references/rules.md
```

A deterministic script can calculate domain facts, while a prompt/react skill can explain or adapt the result. For example, an Electron "Book of Fate" app can use a script skill to compute hexagrams from two numbers and a prompt skill to produce the interpretation.

## External Scheduling

Company-level scheduling, distributed workers, queues, resource allocation, tenant isolation and SLA management should live outside Mateway. Such a system can call Mateway as a local agent runtime kernel for individual tasks.

## Non-Goals

Mateway embedding mode does not imply a distributed workflow engine, multi-tenant orchestration layer, or long-running worker platform. It remains a local-first runtime with traceable task graph execution.
