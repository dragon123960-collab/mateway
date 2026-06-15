# Architecture

Mateway is a small local-first Go runtime. The main design constraint is to keep one transcript-driven AgentCore loop and add reliability through hooks, tool contracts, task contracts, evidence, and memory.

## Package Map

- `cmd/mateway`: CLI entrypoint.
- `internal/cli`: command handlers, TUI rendering, trace display, and local diagnostics.
- `internal/runtime`: task lifecycle, task contracts, hooks, context budgeting, completion evaluation, progress events, and trace writing.
- `internal/agentcore`: model/tool loop, tool registry, tool contracts, risk classes, and tool call execution.
- `internal/tool`: built-in tools such as file, terminal, web, schedule, task, and secret tools.
- `internal/session`: persisted session state, task nodes, task contracts, pending actions, archives, and compacted transcripts.
- `internal/gateway`: channel routing, dedupe, session keys, asynchronous runtime execution, and reply dispatch.
- `internal/channel`: platform I/O adapters and message normalization.
- `internal/config`: config loading, defaults, init assets, models, agents, channels, skills, and security settings.
- `internal/memory`: Markdown memory, proposals, lint/search/index, learning distill, and skill learning evidence.
- `internal/skill`: skill catalog, validation, install/proposal helpers, and secret scanning.

## Runtime Boundaries

The runtime owns task state and execution policy, but it does not become a workflow engine.

- `AgentCore` remains a model/tool loop.
- Runtime hooks add context, policy, observation, response cleanup, and completion checks.
- Tool implementations perform real actions and return evidence.
- Channel packages only receive, normalize, send, and react to platform messages.
- Gateway handles session routing and channel fan-out, not business-level agent routing.

## Task And Evidence Model

Action tasks are represented by `TaskContract`:

- `required_tools`: real tool names only.
- `required_skills`: selected skills, never tool names.
- `required_evidence`: acceptance conditions.
- `plan_items`: an executable checklist with tool and status.
- `completion_policy`: concise finishing rule.

The execution loop is still ReAct. Mateway does not mechanically replay the checklist. Instead, tool results update plan item status and the completion evaluator decides whether final output is allowed.

## Skills

Skills are editable `SKILL.md` files under:

```text
workspace/agents/<agent_id>/skills/<skill_name>/SKILL.md
workspace/skills/<skill_name>/SKILL.md
```

Planning may discover skill headers and select relevant skills. Execution only receives selected skill context. Skill names are not accepted tools; work must still be performed through real tools such as `terminal.run`, `file.read`, or `web.search`.

## Memory

Long-term memory is stored as Markdown under `workspace/memory`. Runtime observations can produce proposals, but durable memory changes require explicit user action. Derived indexes are rebuildable and are not the source of truth.

## Non-Goals

- No PlanExecute framework.
- No DAG runtime.
- No multi-agent supervisor.
- No gateway business routing.
- No command execution tool besides `terminal.run`.
