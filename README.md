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

The agent can propose a durable memory after a useful task. The user can reply `save` to commit it or `ignore` to reject it. Chinese aliases such as `保存` and `忽略` are also supported. Under the hood this mirrors a lightweight Git flow:

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
Save to long-term memory:
mateway memory proposal commit <proposal_id>

Ignore this candidate:
mateway memory proposal reject <proposal_id>
```

In chat channels, the user can also reply `save` / `ignore` or `保存` / `忽略`. Mateway stores that as a pending `memory_proposal_review`, so a short reply is interpreted by runtime state, not guessed by the model.

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
- model request count and token usage when the provider returns usage metadata
- tool calls and tool results
- hook events
- pending confirmations
- final reply
- runtime timings

Persistent traces, session transcripts, and task step summaries redact secret-like fields such as `api_key`, `token`, `password`, `smtp_pass`, `imap_pass`, and bearer tokens. The model still sees live tool output for the current task; persistent logs avoid storing obvious credentials.

### 5. Bounded Session Context
Sessions are runtime state, not an ever-growing raw chat log. Before each model call, Mateway builds context from:

- fresh system/runtime context from `context_hook`
- the current agent profile Markdown files
- discovered skill guidance
- relevant long-term memory snippets when `memory_safe_read` triggers
- the compacted recent session transcript
- the current user message

System context is regenerated on every request and is not stored back into the session transcript. Stored session messages are compacted: system messages are dropped, large tool results are truncated, and only the most recent conversation messages are retained. Task nodes keep short summaries, trace ids, and tool-step evidence so old work remains auditable without forcing the whole transcript into the next prompt.

Send `/new`, `/新会话`, or `新会话` to archive the current session and clear the active state under the same `session_key`. This is useful for long Feishu threads where the channel session key stays fixed but the agent should start from a clean context.

### 6. Skills As Editable Behavior, Not Magic Tools
Mateway discovers local `SKILL.md` files and injects concise guidance into the runtime context. Current default skills include:

- `software-install`
- `fresh-search`
- `source-evaluation`

### 7. Internationalization Boundary

Mateway keeps internal machine interfaces in English: config keys, trace keys, audit events, pending kinds, JSONL evidence, tool names, and machine-readable CLI output are not localized. Human-facing runtime prompts use a small message catalog.

Default config uses `app.locale: auto`: Chinese user text receives Chinese prompts; other text receives English prompts. You can force a language with `app.locale: en-US` or `app.locale: zh-CN`. Additional locales can be added through `app.message_catalog_dir` by placing files such as `de-DE.yaml` or `fr-FR.yaml` with stable message keys and `aliases.<action>` entries.

Review aliases are locale-independent. For example, tool approval accepts `confirm` / `cancel` and `确认` / `取消`; memory review accepts `save` / `ignore` and `保存` / `忽略`; schedule review accepts `run` / `cancel` and `执行` / `取消`.

Example catalog fragment:

```yaml
approval.confirm.generic: Bitte bestätigen, um fortzufahren. Antworten Sie mit "confirm" oder "cancel".
aliases.confirm:
  - bestätigen
aliases.memory_commit:
  - speichern
```
- `connector-gap`

Skills are guidance, not executable capabilities by themselves. If a task needs a real action, the agent must still use an actual tool or script and show evidence.

## What It Can Do Today

Mateway currently supports:

- CLI task entrypoint: `mateway ask`
- Feishu WebSocket gateway
- native Weixin iLink Bot channel: `mateway weixin login`, `mateway weixin enable`
- channel discovery from runtime channel config files: `mateway channel list`
- real runtime tests: `mateway test`
- trace review: `mateway trace`
- session inspect/archive commands: `mateway session list`, `mateway session show`, `mateway session archive list/show`
- task tree and follow-up binding
- pending confirmation for risky tools
- safe built-in tools: `file.read`, `file.write`, `project.index`, `terminal.run`, `web.search`, `web.fetch`
- local secret store: `mateway secret set/get/list/delete`
- hook events in trace
- workspace profile injection
- skill discovery from `workspace/skills` and agent-specific skill overrides
- Markdown memory lint/search/index
- memory proposal create/list/show/commit/reject
- automatic diary/proposal generation after useful completed tasks
- configurable memory proposal nudges by channel, interval, and max displayed candidates
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

### Start Gateway
```bash
./build/mateway gateway serve
```

`gateway serve` runs enabled built-in channels in the foreground. Use launchd, systemd, or another service manager if you want it hosted as a background service.

List channel ids from local channel config files:

```bash
./build/mateway channel list
```

Example:

```text
ID      ENABLED  CONFIG
feishu  true     ~/.mateway/config/channels/feishu.yaml
weixin  true     ~/.mateway/config/channels/weixin.yaml
```

Use the `ID` column when configuring channel-scoped behavior such as memory proposal nudges.

### Connect Weixin

Mateway's native Weixin channel follows the Hermes-style iLink Bot API path. It supports QR login, saved accounts, long-poll inbound text messages, and text replies.

```bash
./build/mateway weixin login
./build/mateway weixin enable
./build/mateway gateway restart
```

The login command saves credentials under `~/.mateway/run/weixin/accounts/`. `weixin enable` updates `~/.mateway/config/channels/weixin.yaml` without writing the token into the config file. Media/CDN support is intentionally out of scope for this release.

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
./build/mateway memory proposal show <proposal_id>
./build/mateway memory proposal commit <proposal_id>
./build/mateway memory proposal reject <proposal_id> --reason "not reusable"
```

Memory proposal nudges are configurable in `~/.mateway/config/config.yaml`:

```yaml
memory:
  proposal_nudge:
    enabled: true
    interval: 24h
    channels:
      - cli
    max_proposals: 3
```

The nudge is generated by runtime, but only for configured channel ids. It shows a small candidate summary and points to `mateway memory proposal show <proposal_id>` for details instead of dumping all pending proposals into chat.

Distill session or project:

```bash
./build/mateway memory distill session <session_key>
./build/mateway memory distill project close <project_id>
```

Run manual heartbeat maintenance:

```bash
./build/mateway memory heartbeat lint-index
./build/mateway memory heartbeat learning
./build/mateway memory heartbeat skill
```

Run heartbeat maintenance in a foreground loop:

```bash
./build/mateway memory heartbeat serve
```

The heartbeat command lints Markdown memory, rebuilds `indexes/memory_index.json` when safe, distills learning evidence, proposes skill patches, and writes audit entries.

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
- model request count and input/output/total tokens
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
    archive/     # archived sessions created by /new
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

Skills must not store plaintext credentials. Put credentials in the local secret store and reference secret ids from skill frontmatter:

```yaml
required_secrets:
  - id: mail.smtp_pass
    env: SMTP_PASS
```

`mateway skill install` and `file.write` writes to `SKILL.md` reject secret-like plaintext such as passwords, API keys, tokens, and bearer tokens.

Current behavior:

- Runtime discovers local skills and injects short guidance into context.
- Default initialized shared skills cover fresh search, source evaluation, connector gaps, and software installation workflow.
- Agents can inspect existing skills, install local/raw skills, and review skill patch proposals before promotion.

Available:

- `mateway skill catalog report`
- `mateway skill search <query>`
- `mateway skill install <name-or-url>`
- `mateway skill proposal list|show|promote|reject`
- `mateway skill usage report`
- external skill catalog integration. Planned initial sources: `skills.sh`, `skillhub.cn`, and `clawhub.ai`
- heartbeat-generated skill patch proposal workflow

Script Bridge is intentionally small: executable scripts under `~/.mateway/scripts/`, `workspace/scripts/`, or configured `scripts.dirs` can be listed with `mateway script list` and run through `script.run` / `mateway script run`. Script headers may declare `mateway.required_secret` entries so credentials come from `mateway secret`, not from `SKILL.md`, trace, or memory.

## Multi-Agent Profiles

Mateway does not yet include a multi-agent supervisor, subagent spawning, or DAG router. It does already include the foundation for multiple agent profiles:

- `config.agents.default`
- `config.agents.profiles[]`
- `config.agents.bindings[]`
- `workspace/agents/<agent_id>/`
- `workspace/agents/<agent_id>/skills/`
- `workspace/memory/agents/<agent_id>/`

Each agent profile uses the same core prompt-facing files: `agent.md`, `soul.md`, `user.md`, `tools.md`, and `memory.md`. New profiles created with `mateway agent create` use English baseline templates and do not overwrite existing files.

This means different channels or session namespaces can select different agent identities, prompt files, skill overrides, and memory scopes while still sharing the same small AgentCore runtime.

The boundary is deliberate: profiles and bindings are in scope; autonomous multi-agent orchestration is not part of the current release.

Profile productization commands:

- `mateway agent list`
- `mateway agent report <agent_id>`
- `mateway agent lint <agent_id>`
- `mateway agent create <agent_id> [--name <name>] [--default]`
- `mateway agent bind --channel <channel> [--account-id <id>] [--peer-id <id>] <agent_id>`
- `mateway agent unbind --channel <channel> [--account-id <id>] [--peer-id <id>]`

## Gateway Boundary

Gateway is the channel aggregation layer: session key, dedupe, async runtime execution, and reply dispatch. `gateway serve` starts enabled built-in channels from `channels/`, including Feishu WebSocket and native Weixin long-poll.

New stable channels should be added as built-in channel specs so one gateway process can manage them. A channel package owns platform I/O and message normalization, while gateway owns session key, dedupe, async runtime execution, and trace events.

The native Weixin channel follows the Hermes-style iLink Bot API path: `mateway weixin login` scans a QR code and saves account credentials under `~/.mateway/run/weixin/accounts/`; `gateway serve` then long-polls `getupdates` and sends text replies through `sendmessage`. Media/CDN support is intentionally out of scope for the first Mateway implementation.

Use `mateway channel list` to see canonical channel ids from `~/.mateway/config/channels/*.yaml`. Runtime config should use those ids, for example `feishu` or `weixin`, rather than aliases such as `lark` or `wechat`.

`gateway serve` uses the same config loader as CLI commands, so it reads `~/.mateway/config/mateway.env`. Existing process environment variables still win over values from that file.

## Current Limits

Mateway intentionally does not claim these are finished:

- no multi-agent supervisor or DAG router
- no OS-level sandbox wrapper yet
- no general mail/SSH/GitHub connector framework yet
- no visual workspace UI yet
- no external skill marketplace installer yet

The current usable release is focused on: a stable small-core runtime, multi-agent profile foundations, hook pipeline, real tools with risk boundaries, Feishu/CLI entrypoints, traceability, and white-box memory.

## Roadmap

### Done Enough For Developer Use

- Small AgentCore runtime loop
- Multi-agent profile and binding foundation
- CLI / test / Feishu / Weixin entrypoints
- Hook pipeline
- Tool policy and confirmation boundaries
- JSONL traces
- Skill discovery and context injection
- Local secret store and skill secret scanning
- Markdown memory lint/search/index
- Memory proposal list/show/commit/reject workflow
- Configurable memory proposal nudges
- Self-learning diary/proposal generation
- Memory safe-read context injection
- Session/project distill commands
- Heartbeat `lint-index` and foreground heartbeat runner
- Channel-neutral scheduled task create/test/run-due/serve
- Built-in channel discovery with `mateway channel list`
- Secret redaction for persistent runtime records

### Next

- more built-in channels such as DingTalk, QQ, WeCom, and Telegram
- richer media handling for channels that support images/files
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
