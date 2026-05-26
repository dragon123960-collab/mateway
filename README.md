# Mateway

[English](./README.md) | [中文](./README.zh.md)

Mateway is a lightweight Go runtime for building a practical personal or team agent that can use tools, remember useful context, and work from both CLI and Feishu.

It is designed for people who want an agent that can actually operate inside a local workspace and business chat, without starting from a heavy workflow platform.

## Highlights

- **Lightweight agent framework**: a compact single-agent runtime with session/task state, memory, tools, skills, schedule, heartbeat, CLI, and Feishu gateway.
- **One runtime, multiple entries**: CLI, Feishu, and scheduled invocations all reuse the same agent loop instead of becoming separate products.
- **Reviewable memory**: durable memory is Markdown-first, proposal-based, searchable, and governed through explicit `list -> show -> commit/reject` workflows.
- **Clear tool boundaries**: risky writes, patches, and dangerous commands are guarded by confirmation, while read/review commands can run directly.
- **Practical direct commands**: `mateway memory ...`, `mateway skill promote`, schedules, traces, heartbeat, and gateway status paths have stable CLI and runtime behavior.
- **Extensible without becoming a platform**: add capability through tools, skills, and external command wrappers; traditional software can be integrated without a heavy connector system.

## Why Mateway

Most agent demos are easy to start but hard to trust in daily work. Mateway focuses on a smaller, inspectable system:

- **Single binary first**: build one `mateway` binary and run it locally or as a service.
- **CLI and Feishu**: talk to the same runtime from terminal or Feishu chat.
- **Tool use with boundaries**: file writes, patches, and dangerous shell commands require confirmation.
- **Traceable execution**: plans, tool calls, results, and replies are written to trace logs.
- **Session-aware follow-up**: short memory tracks recent tasks, artifacts, pending confirmations, and follow-up context.
- **Markdown-first memory**: durable memory is kept as reviewable Markdown, with evidence and an optional rebuildable index.
- **User scheduled tasks**: create natural-language recurring tasks that run through the same runtime.
- **Skill-oriented extension**: add capabilities through skills, tools, and external command wrappers instead of hard-coding business systems.

The core runtime loop stays intentionally small:

```text
receive -> plan -> policy -> act -> observe -> synthesize -> reply
```

## Current Status

Mateway is early but usable as a first-version single-agent runtime.

Implemented:

- CLI commands: `init`, `doctor`, `ask`, `test`, `eval`, `trace`, `memory`, `heartbeat`, `schedule`, `gateway`
- Feishu WebSocket receive/reply/reaction
- Anthropic-compatible and OpenAI-compatible model clients
- configurable model and agent profiles
- built-in tools: time, config summary, web search, file read/write/patch, terminal run, project index, file summary, memory search/index, user ask
- path guards, dangerous command guards, output truncation, and response sanitization
- persistent session/task state and follow-up resolution
- Markdown memory proposal, commit, reject, lint, index, and search
- heartbeat maintenance jobs for memory lint/review/compact/index rebuild
- planner-selected user scheduled task tools with due detection and runtime execution
- real-model planner routing evaluation with `mateway eval routing`
- workspace skill discovery and default skills

Still evolving:

- multi-agent profile routing beyond the current configuration contract
- optional custom tool contracts for teams that need deeper API or CLI integration
- optional FTS5 or embedding-backed retrieval
- packaging, release automation, and more production hardening

## Releases

GitHub releases are intended to ship prebuilt binaries in addition to source archives.

Recommended release assets:

- `mateway_darwin_arm64`
- `mateway_darwin_amd64`
- `mateway_linux_arm64`
- `mateway_linux_amd64`
- `mateway_windows_amd64.exe`

For local release builds:

```bash
./build-release.sh v0.1.0
```

For tag-driven release uploads, the repository includes a GitHub Actions workflow under `.github/workflows/release.yml`.

## Quick Start

Build from source:

```bash
git clone https://github.com/dragon123960-collab/mateway.git
cd mateway
go test ./...
go build -o build/mateway ./cmd/mateway
```

Initialize local runtime files:

```bash
./build/mateway init
```

This creates `~/.mateway` with config templates, workspace files, memory scaffolding, and default skills. Existing real config files are not overwritten.

Configure secrets and validate:

```bash
cp ~/.mateway/config/mateway.env.sample ~/.mateway/config/mateway.env
vim ~/.mateway/config/mateway.env
vim ~/.mateway/config/config.yaml
./build/mateway doctor
```

Ask from CLI:

```bash
./build/mateway ask "What time is it?"
./build/mateway ask "Read README.md and summarize this project."
./build/mateway ask "Run pwd, then explain the current working directory."
```

Start the gateway process:

```bash
./build/mateway gateway serve
```

`gateway serve` is the foreground runtime process. Run it under LaunchAgent, systemd, or another service manager when you want Mateway to stay online.

`gateway serve` does not install autostart by itself. `gateway start`, `gateway restart`, `gateway stop`, and `gateway status` are thin adapters for an already-registered OS service: launchd on macOS and `systemctl --user` on Linux. Create the LaunchAgent plist or systemd user unit separately if you want the gateway to start after login or reboot.

## Configuration

Runtime configuration lives under `~/.mateway/config`.

Important files:

```text
~/.mateway/config/
  config.yaml
  mateway.env
  models/
    minimax.yaml
    local-mlx.yaml
  channels/
    feishu.yaml
```

Configuration responsibilities:

- `config.yaml`: app paths, security, search, scheduler, memory, and agent defaults
- `models/*.yaml`: model provider, endpoint, API compatibility, and model name
- `channels/feishu.yaml`: Feishu channel configuration
- `mateway.env`: local secrets, never commit this file

Supported model API modes:

- `api: anthropic`
- `api: openai`

Example model selection:

```yaml
model:
  default: minimax
  fallbacks: []
  roles:
    planning: minimax
    repair: minimax
    synthesis: minimax
    followup: minimax
```

The current runtime primarily uses the default model. Role-specific routing is part of the configuration contract and will become more important as the runtime grows.

## CLI Usage

Common commands:

```bash
mateway init
mateway doctor
mateway ask "Summarize the current repository."
mateway gateway serve
mateway gateway status
mateway trace tail
mateway trace show <trace_id>
```

`gateway serve` runs in the foreground. The service-management commands only control an already-registered OS service; they do not create launchd or systemd autostart files.

Memory commands:

```bash
mateway memory list --area inbox --status proposed
mateway memory show <id-or-path>
mateway memory review --review stale
mateway memory review --review stale --proposal
mateway memory commit --proposal <proposal-id>
mateway memory reject --proposal <proposal-id>
mateway memory lint
mateway memory index
```

Inbox review is designed as a direct workflow: `memory list` and `memory show` print suggested follow-up commands for approve, reject, or skill promotion. `memory commit` and `memory reject` require confirmation before changing durable memory state.

Skill promotion:

```bash
mateway skill promote --proposal <skill-candidate-id-or-path> --name <skill-name>
mateway skill list
```

Scheduled task commands:

```bash
mateway schedule propose --title "AI trends" --prompt "Collect recent AI trend articles with sources." --daily-at 09:00
mateway schedule proposals
mateway schedule commit-proposal <id>
mateway schedule list
mateway schedule due
mateway schedule run-due
```

Natural-language schedule creation is handled by the normal runtime planning loop. The planner should select `schedule.create` with complete fields, or ask for missing information:

```text
Every day at 9:00, collect recent AI trend articles and write a short sourced report.
```

The schedule CLI still supports proposal/review commands for manual workflows, but runtime schedule creation uses tools directly.

## Feishu

Mateway can run as a Feishu WebSocket bot.

The Feishu channel is intentionally simple:

- receive and normalize messages
- reply to the user
- add lightweight reactions
- ignore app/self messages
- avoid noisy intermediate progress messages by default

Runtime work is handled outside the Feishu callback so slow model/tool execution does not block event acknowledgement.

Configure Feishu in:

```text
~/.mateway/config/channels/feishu.yaml
~/.mateway/config/mateway.env
```

Then run:

```bash
mateway gateway serve
```

## Memory

Mateway uses a Markdown-first memory design.

There are two layers:

- **Short memory**: recent session/task state, artifacts, pending confirmations, and follow-up context
- **Long memory**: reviewed Markdown notes under the workspace memory tree

Long memory is proposal-based:

```text
task with evidence -> memory proposal -> user review -> commit/reject -> searchable long memory
```

This keeps durable knowledge inspectable and editable. The JSON memory index is rebuildable from Markdown, so Markdown remains the source of truth.

## Scheduled Tasks And Heartbeat

Mateway has two separate background concepts:

- **Heartbeat**: system maintenance jobs, such as memory lint, daily review, recent compaction, and memory index rebuild
- **User scheduled tasks**: user-created recurring business tasks, such as collecting AI trend articles every morning

User scheduled tasks run through the same runtime path as normal user requests, so tool policy, confirmations, traces, memory, and artifacts remain consistent.

The lifecycle is:

```text
user request -> planner selects schedule tool -> task YAML -> due run -> runtime invocation -> artifact
```

## Skills And External Tools

Skills are local capability packages that describe how the agent should work in a domain. Traditional software can still be reached through built-in tools, workspace skills, shell commands, or lightweight custom tool wrappers.

The current direction is:

```text
skill = instructions + metadata + optional scripts/assets + allowed tools
tool = explicit capability with args, risk, evidence, and confirmation boundary
external command = optional bridge to existing CLI software
```

Mateway does not hard-code business integrations into the core runtime. A separate connector scanning system is no longer the next-stage goal; deeper integrations should grow from the same tool and skill contracts instead.

## Security Model

Mateway is built for explicit, observable tool use:

- file tools are restricted to the project root, Mateway workspace, and configured accessible paths
- file write and patch operations require confirmation
- dangerous shell commands require confirmation
- model-provided `confirmed=true` is ignored for guarded tools
- Feishu replies are sanitized to avoid leaking raw tool call JSON
- all runtime steps can be traced through local event logs

This is still early software. Review configuration, allowed paths, and confirmation behavior before running it on sensitive machines.

## Repository Layout

```text
cmd/mateway              CLI entrypoint
internal/app             application wiring
internal/channel         channel interfaces
internal/channel/feishu  Feishu adapter
internal/config          configuration loading and init templates
internal/gateway         channel orchestration and service management
internal/heartbeat       maintenance scheduler
internal/memory          Markdown memory store, lint, index, search
internal/model           model clients and planning helpers
internal/observer        trace and event inspection
internal/runtime         agent loop, task binding, session flow
internal/schedule        user scheduled task store and runner
internal/session         persisted session/task state
internal/skill           skill discovery and default skills
internal/tool            built-in tools and safety policy
```

## Development

Run tests:

```bash
go test ./...
```

Build:

```bash
go build -o build/mateway ./cmd/mateway
```

## License

Apache License 2.0.
