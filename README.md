# Mateway

<p align="center">
  <img src="banner.png" alt="Mateway — local-first agent runtime" width="100%" />
</p>

[English](./README.md) | [中文](./README.zh.md)

**Mateway is a local-first Go agent runtime for real workspaces. It turns a user task into a lightweight contract, runs a small transcript-driven tool loop, and finishes only when the required evidence is present or a concrete blocker is known.**

It is intentionally not a heavy workflow platform. The project is about loop engineering: keeping the core AgentCore loop small while adding reliability through contracts, tool boundaries, editable skills, white-box memory, and traceable evidence.

```text
message -> task contract/checklist -> selected skill preflight
        -> AgentCore ReAct loop -> tool evidence
        -> completion evaluator -> final answer or blocker
```

## What Makes It Different

- **Task contracts, not blind chatting.** Action tasks carry required tools, expected evidence, selected skills, and plan items. The runtime uses that checklist to decide whether the task is done.
- **Transcript-driven execution.** Mateway does not mechanically replay a PlanExecute graph or DAG. The model still acts through a normal ReAct loop, while hooks and the evaluator keep it honest.
- **Editable skills.** Skills live as local `SKILL.md` files under the workspace. Planning can select relevant skills, but skill names never become tool names; real work still uses tools such as `file.read`, `file.write`, `terminal.run`, `web.search`, and `web.fetch`.
- **White-box memory.** Long-term memory is Markdown with YAML frontmatter, plus proposals and audit trails. The agent can suggest durable memory, but the user decides what gets committed.
- **Trace ledger.** Runs produce JSONL traces with model turns, tool calls, evidence, hook events, timing, token diagnostics, and secret redaction.
- **Small local runtime.** CLI, Feishu, Weixin, scheduled jobs, and tests share the same runtime instead of separate agent stacks.

## Current Capabilities

- CLI entrypoint: `mateway ask`
- Feishu WebSocket gateway and native Weixin iLink Bot channel
- Built-in tools: `file.read`, `file.write`, `file.edit`, `file.delete`, `terminal.run`, `web.search`, `web.fetch`, `secret.set`, `schedule.manage`, `task.search`, `task.resume`, and `toolresult.read`
- Tool policy with destructive terminal command blocking, path validation, and secret redaction
- Task plan review for complex or risky work
- Context budgeting, compacted tool output, and raw output retrieval by `raw_ref`
- Multi-agent profile foundations with channel bindings, agent-specific skills, and agent-scoped memory
- Memory proposal, lint, search, index rebuild, and learning heartbeat commands

`terminal.run` is the only command execution tool. It can inject secrets through `env_secrets`; traces record only secret ids and environment variable names, never secret values.

## Quick Start

```bash
git clone https://github.com/dragon123960-collab/mateway.git
cd mateway

go test ./...
go build -o build/mateway ./cmd/mateway
./build/mateway init
./build/mateway doctor
```

Then configure local models and channels:

```bash
cp ~/.mateway/config/mateway.env.sample ~/.mateway/config/mateway.env
vim ~/.mateway/config/mateway.env
vim ~/.mateway/config/config.yaml
```

Try the CLI:

```bash
./build/mateway ask "Read README.md and summarize this project."
./build/mateway ask "Inspect the current project directory and identify the runtime entrypoint."
```

`mateway init` creates the local home under `~/.mateway/`:

```text
config/      models, channels, runtime settings
workspace/   agent profiles, shared skills, Markdown memory
sessions/    compacted session state
trace/       JSONL runtime traces
observe/     memory and skill learning evidence
indexes/     derived indexes
run/         runtime locks and channel state
```

## Documentation

- [Architecture](./docs/architecture.md) describes package boundaries and the small-runtime design.
- [Configuration](./docs/configuration.md) explains local paths, models, channels, skills, memory, and security settings.
- [Execution Flow](./docs/execution-flow.md) follows a task from user message to final answer or blocker.
- [Roadmap](./docs/roadmap.md) records the current direction and non-goals.

Development scratch notes live in `dev-notes/` and are intentionally short-lived.

## Design Boundaries

Mateway does not aim to become a workflow platform. Current non-goals:

- No PlanExecute framework.
- No DAG runtime.
- No multi-agent supervisor or subagent spawning.
- No gateway business routing layer.
- No command execution tool besides `terminal.run`.

The future direction is deeper loop engineering: better planning contracts, tighter execution context, stronger evidence evaluation, safer terminal isolation, and more useful skill/memory crystallization.
