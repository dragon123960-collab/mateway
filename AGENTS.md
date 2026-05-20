# AGENTS

## Current Line

This is the clean Mateway rewrite. The current goal is a small, useful tool agent that can run from CLI and Feishu.

The architecture direction is now fixed by `docs/总规划.md`. Do not keep expanding or repeatedly overturning the main design. From here, execution should follow a dedicated TODO breakdown and land one item at a time.

## Rules

- Do not copy the old `runtimev2`, `agentv2`, `toolv2`, or `workflowv2` code from `mateway1`.
- Hermes Agent is a reference for ideas only: tool registry, tool loop, dangerous command guards, output budgets, and tool error accounting.
- Eino is not part of the runtime loop. If a future adapter is needed, keep it behind `internal/model` or `internal/adapter`.
- Keep the main loop simple: receive -> plan -> policy -> act -> observe -> synthesize -> reply.
- Channel packages are I/O only: receive, normalize, send, react. Runtime calls, session routing, and reaction policy belong in `internal/gateway`.
- Every channel must have an isolated session key namespace, such as `feishu:<thread_id>`, to avoid cross-channel context bleed.
- `gateway` owns channel serving and orchestration. `gateway start/restart/stop/status` may exist only as thin OS service-management adapters, not as channel-specific business logic.
- `gateway serve` must keep the single-instance process lock. OS service commands can only affect one registered service and cannot prevent another binary from being started elsewhere.
- Feishu handlers should acknowledge quickly and run work asynchronously with inbound `message_id` dedupe. Do not let slow runtime work block the WebSocket event callback.
- Feishu should prefer a clean final reply. Do not emit noisy intermediate progress messages by default.
- Feishu app/self messages must be ignored, including progress messages emitted by Mateway itself.
- Use `~/.mateway` for runtime configuration and local data. Do not commit `~/.mateway/config`, `~/.mateway/docker`, secrets, or generated runtime state.
- File tools default to `~/.mateway` for relative paths unless the user explicitly provides a project path or another allowed root.
- Treat `docs/总规划.md` as the architecture contract for this phase. It answers “the system should look like what”.
- Put execution work into `docs/开发TODO.md`. It answers “what to build next, where it lives, and how to accept it”.
- Do not bloat `docs/总规划.md` into an endless idea list. New work should normally become TODO items, not architecture churn.
- Code changes should follow the current TODO order unless the user explicitly changes priority.
- Maintain `docs/进度.md` in Chinese. Whenever meaningful work is completed, blocked, or deferred, update it in the same change so the user can see current completed and unfinished items.
- New capabilities must state their tool name, risk, arguments, evidence, and confirmation boundary.

## Fixed Architecture

The current target shape is:

```text
Minimal Agent Runtime
+ AgentLoop
+ SessionState
+ FollowupResolver
+ SkillDiscovery
+ ToolRegistry
+ ResponseSanitizer
+ Multi-Agent Profiles
```

Execution principle:

```text
总纲定方向；
TODO 定执行；
代码按 TODO 一项项落地。
```

The near-term build order is:

1. AgentLoop 工具调用闭环
2. ResponseSanitizer
3. SessionState / TaskState
4. FollowupResolver
5. SkillDiscovery
6. `fresh-search` / `chinese-summary` / `source-evaluation` / `project-review` skills
7. `project.index` / `file.summary` tools
8. AgentProfile / AgentRegistry / GatewayBinding

## Today Scope

- MiniMax Anthropic-compatible model client
- CLI ask/doctor commands
- Feishu WebSocket receive/reply/reaction
- Basic tools: time, config summary, web search, file read/write/patch, shell run, user ask
- Dangerous command and path guards
- Output truncation

## Not Today

- Long-term memory
- Multi-agent supervisor
- Plugin marketplace
- Full workflow engine
- Eino integration
