# Mateway
<p align="center">
  <img src="banner.png" alt="Mateway — memory-native local agent runtime" width="100%" />
</p>
[English](./README.md) | [中文](./README.zh.md)
**Mateway is a local-first Agent Runtime for real workspaces, built around white-box memory, self-learning, and auditable tool use.**

It is not a heavy workflow platform and not a toy chatbot demo. Mateway is a small Go runtime built around one compact AgentCore loop, with multi-agent profile and binding foundations for different work identities, channels, skills, and memory scopes.

> **In a nutshell: Mateway = Small AgentCore + Multi-Agent Profiles + Hook Runtime + Tool Boundaries + Git-like Memory + Self-learning Proposals + Trace Ledger.**

```text
receive -> followup_hook -> context_hook -> model/tool loop
        -> tool_policy_hook -> observe_hook -> response_hook -> reply
```

## Why Mateway?
Most agent frameworks can produce an impressive first demo. The hard part is making an agent trustworthy after it has worked with you for weeks:

- It should remember useful experience without silently rewriting its own beliefs.
- It should learn from completed tasks, but still ask before committing durable memory.
- It should explain which tools it used, what evidence it saw, and where a result came from.
- It should work the same way from CLI, Feishu, tests, and future scheduled jobs.
- It should keep connector gaps honest instead of pretending it sent an email or logged into a server.

Mateway takes a conservative path: keep the AgentCore loop small, then add capability through profiles, hooks, skills, tools, and Markdown memory.

## What Is Unique Here?

### 1. Git-like Memory
Mateway treats memory as a reviewable working tree, not a black-box vector blob.

```text
task / trace / tool evidence
  -> diary
  -> proposal
  -> save or ignore
  -> Markdown long-term memory
  -> rebuildable index
  -> safe-read injection
```

The agent can propose a durable memory after a useful task. The user can reply `保存` to commit it or `忽略` to reject it. Under the hood this mirrors a lightweight Git flow:

| Memory step | Git-like idea |
|---|---|
| diary | working notes |
| proposal | staged candidate |
| save / commit | durable long-term memory |
| reject | discard candidate |
| audit log | commit history |
| index rebuild | derived index, never source of truth |

Long-term memory is stored as Markdown with YAML frontmatter under `~/.mateway/workspace/memory/`, so it can be opened in Obsidian, edited by hand, linted, searched, rebuilt, and audited.

### 2. Self-learning Without Silent Mutation
After task completion, `observe_hook` records task steps and may generate a memory proposal. The proposal is shown in the final answer as a human decision:

```text
保存到长期记忆:
mateway memory proposal commit <proposal_id>

忽略这条候选:
mateway memory proposal reject <proposal_id>
```

In chat channels, the user can also reply `保存` or `忽略`. Mateway stores that as a pending `memory_proposal_review`, so a short reply is interpreted by runtime state, not guessed by the model.

### 3. Hook-first Runtime
The core loop stays small. Extension points are explicit:

| Hook | Purpose |
|---|---|
| `followup_hook` | Bind "continue", "retry", "what about Tianjin?" to the right task, or ask for clarification |
| `context_hook` | Inject runtime context, workspace profile, discovered skills, and relevant memory snippets |
| `tool_policy_hook` | Enforce tool risk, confirmation boundaries, and dangerous command checks |
| `observe_hook` | Record accepted tool steps, task evidence, diary entries, and memory proposals |
| `response_hook` | Sanitize final replies and add memory review prompts |

### 4. Trace Ledger With Secret Redaction
Every run writes a JSONL trace:

- request and channel
- model turns
- tool calls and tool results
- hook events
- pending confirmations
- final reply
- runtime timings

Persistent traces, session transcripts, and task step summaries redact secret-like fields such as `api_key`, `token`, `password`, `smtp_pass`, `imap_pass`, and bearer tokens. The model still sees live tool output for the current task; persistent logs avoid storing obvious credentials.

### 5. Skills As Editable Behavior, Not Magic Tools
Mateway discovers local `SKILL.md` files and injects concise guidance into the runtime context. Current default skills include:

- `software-install`
- `fresh-search`
- `source-evaluation`
- `connector-gap`

Skills are guidance, not executable capabilities by themselves. If a task needs a real action, the agent must still use an actual tool or script and show evidence.

## What It Can Do Today

Mateway currently supports:

- CLI task entrypoint: `mateway ask`
- Feishu WebSocket gateway
- real runtime tests: `mateway test`
- trace review: `mateway trace`
- task tree and follow-up binding
- pending confirmation for risky tools
- safe built-in tools: `file.read`, `file.write`, `project.index`, `terminal.run`, `web.search`, `web.fetch`
- hook events in trace
- workspace profile injection
- skill discovery from `workspace/skills` and agent-specific skill overrides
- Markdown memory lint/search/index
- memory proposal create/list/commit/reject
- automatic diary/proposal generation after useful completed tasks
- conversation reply handling for memory proposal `保存` / `忽略`
- memory safe-read injection through `context_hook`
- session and project distill commands
- manual memory heartbeat: lint + index rebuild
- secret redaction in persistent runtime records
- multi-agent profile foundations: `config.agents.profiles[]`, channel bindings, agent-specific skills, and agent-scoped memory directories

## Quick Start

### Build
```bash
git clone https://github.com/dragon123960-collab/mateway.git
cd mateway

go test ./...
go build -o build/mateway ./cmd/mateway
```

### Init
```bash
./build/mateway init
```

This creates:

```text
~/.mateway/
  config/
  workspace/
    agents/
    skills/
    memory/
  sessions/
  trace/
  observe/
  indexes/
  run/
```

### Configure
```bash
cp ~/.mateway/config/mateway.env.sample ~/.mateway/config/mateway.env
vim ~/.mateway/config/mateway.env
vim ~/.mateway/config/config.yaml
```

Validate configuration:

```bash
./build/mateway doctor
```

### Ask From CLI
```bash
./build/mateway ask "Read README.md and summarize this project."
./build/mateway ask "Inspect the current project directory and identify the runtime entrypoint."
./build/mateway ask "Search today's latest AI news and summarize 5 high-value items."
```

### Run Real Runtime Tests
```bash
./build/mateway test --case read-readme
./build/mateway test --case project-index
./build/mateway test --case web-search
```

Custom task:

```bash
./build/mateway test --session-key demo:a001 --message "Read README.md and explain the memory system."
```

### Start Feishu Gateway
```bash
./build/mateway gateway serve
```

`gateway serve` runs in the foreground. Use launchd, systemd, or another service manager if you want it hosted as a background service.

## Memory Commands

Lint memory:

```bash
./build/mateway memory lint
```

Rebuild index:

```bash
./build/mateway memory index rebuild
```

Search memory:

```bash
./build/mateway memory search "README experience"
```

Review proposals:

```bash
./build/mateway memory proposal list
./build/mateway memory proposal commit <proposal_id>
./build/mateway memory proposal reject <proposal_id> --reason "not reusable"
```

Distill session or project:

```bash
./build/mateway memory distill session <session_key>
./build/mateway memory distill project close <project_id>
```

Run manual heartbeat maintenance:

```bash
./build/mateway memory heartbeat lint-index
```

Run heartbeat maintenance in a foreground loop:

```bash
./build/mateway memory heartbeat serve
```

The heartbeat command lints Markdown memory, rebuilds `indexes/memory_index.json` when safe, and writes an audit entry.

## Scheduled Tasks

Scheduled tasks are channel-neutral. Mateway stores the task, optionally test-runs it, runs it when due, and writes a run record under `~/.mateway/schedules/runs/`. It does not automatically send results back to Feishu, email, Slack, or any other channel.

Create a task. By default it is pending until a test run succeeds:

```bash
./build/mateway schedule create --run-at 2026-05-29T18:00:00+08:00 "check unread mail and summarize important items"
./build/mateway schedule test <task_id>
./build/mateway schedule list
```

Run due tasks once or keep a foreground runner alive:

```bash
./build/mateway schedule run-due
./build/mateway schedule serve
```

If a scheduled task needs notification, make notification part of the task itself through an available tool, local script, connector, or skill. If no delivery channel exists, the agent should explain the gap and ask whether to create the relevant script or skill.

## Trace Commands

```bash
./build/mateway trace <trace-jsonl-path>
```

Use traces to inspect:

- model/tool/runtime latency
- hook decisions
- tool calls and acceptance evidence
- pending confirmations
- memory proposal generation
- Feishu gateway timing

## HOME Directory Structure

```text
~/.mateway/
  config/        # config.yaml, env files, model/channel config
  workspace/     # agent profiles, skills, Markdown memory
  sessions/      # transcripts, task trees, pending states
  trace/         # JSONL traces
  observe/       # diary, proposals, reflections, audit logs
  indexes/       # rebuildable memory indexes
  run/           # runtime files such as gateway locks
```

Workspace:

```text
~/.mateway/workspace/
  agents/
    main/
      agent.md
      soul.md
      user.md
      tools.md
      memory.md
      skills/
  skills/
    software-install/
    fresh-search/
    source-evaluation/
    connector-gap/
  memory/
    user/
      long/
    org/
      long/
    agents/
      main/
        memory.md
        experiences/
```

## Skills

Mateway skills are editable behavioral guidelines stored as:

```text
workspace/agents/<agent_id>/skills/<skill_name>/SKILL.md
workspace/skills/<skill_name>/SKILL.md
```

Discovery order:

1. agent-specific skills
2. shared workspace skills

Agent-specific skills win when names collide.

Current behavior:

- Runtime discovers local skills and injects short guidance into context.
- Default initialized shared skills cover fresh search, source evaluation, connector gaps, and software installation workflow.
- Agents can inspect existing skills and use them as guidance.

Not yet implemented:

- `mateway skill search <query>`
- `mateway skill install <name-or-url>`
- external skill catalog integration. Planned initial sources: `skills.sh`, `skillhub.cn`, and `clawhub.ai`
- automatic skill patch/promotion workflow

## Multi-Agent Profiles

Mateway does not yet include a multi-agent supervisor, subagent spawning, or DAG router. It does already include the foundation for multiple agent profiles:

- `config.agents.default`
- `config.agents.profiles[]`
- `config.agents.bindings[]`
- `workspace/agents/<agent_id>/`
- `workspace/agents/<agent_id>/skills/`
- `workspace/memory/agents/<agent_id>/`

This means different channels or session namespaces can select different agent identities, prompt files, skill overrides, and memory scopes while still sharing the same small AgentCore runtime. The next development stage productizes this with agent list/report/create/bind commands, profile linting, and multi-profile acceptance tests.

The boundary is deliberate: profiles and bindings are in scope; autonomous multi-agent orchestration is not part of the current release.

## Current Limits

Mateway intentionally does not claim these are finished:

- no multi-agent supervisor or DAG router
- no OS-level sandbox wrapper yet
- no general mail/SSH/GitHub connector framework yet
- no visual workspace UI yet
- no external skill marketplace installer yet

The current usable release is focused on: a stable small-core runtime, multi-agent profile foundations, hook pipeline, real tools with risk boundaries, Feishu/CLI entrypoints, traceability, and white-box memory.

## Image Prompt For The Banner

The README banner (`mateway-banner.svg`) should communicate:

```text
A wide 1600x520 technical product banner for "Mateway", a local-first AI agent runtime.
Visual metaphor: an illuminated memory ledger / commit graph / agent runtime pipeline.
Show a compact AgentCore connected to five labeled flows: Profiles, Hooks, Tools, Memory, Traces.
Memory should feel like a Git-like commit ledger: proposals, commits, audit trail, Markdown pages.
Style: precise, developer-tool, dark background, crisp vector geometry, subtle cyan/amber/green accents.
Avoid cute robots, generic chat bubbles, clouds, and abstract blobs.
Text to include: "Mateway" and "Memory-native local agent runtime".
```

## Roadmap

### Done Enough For Developer Use

- Small AgentCore runtime loop
- Multi-agent profile and binding foundation
- CLI / test / Feishu entrypoints
- Hook pipeline
- Tool policy and confirmation boundaries
- JSONL traces
- Skill discovery and context injection
- Markdown memory lint/search/index
- Memory proposal commit/reject workflow
- Self-learning diary/proposal generation
- Memory safe-read context injection
- Session/project distill commands
- Heartbeat `lint-index` and foreground heartbeat runner
- Channel-neutral scheduled task create/test/run-due/serve
- Secret redaction for persistent runtime records

### Next

- multi-agent profile productization: agent list/report/create/bind, profile lint, and multi-profile tests
- script bridge specification for user-provided connectors
- skill source adapters and promote workflow
- safer terminal sandbox wrappers
- read-only trace/task/memory workspace UI
- connector packages for mail, SSH, GitHub, and publishing

## Design Principles

### Keep the core small
The main loop remains small and clear. Complex capabilities integrate through hooks, tool contracts, skills, and scripts.

### Memory must be reviewable
Long-term memory should show origins, support editing/rejection, and allow index reconstruction.

### Trust comes from traces
Agent trustworthiness comes from knowing what happened, which evidence was used, and which boundaries were enforced.

### Local-first does not mean local-only
Mateway prioritizes local workspaces and user machines, but integrates with Feishu, web search, external CLIs, scripts, and future connectors.

### Do not fake connectors
When mail, SSH, GitHub, or publishing connectors are missing, Mateway should report the gap, inspect safe local options, and propose an integration path instead of fabricating execution.

## License

Apache License 2.0.
