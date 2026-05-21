# AGENTS

## Current Line

This is the clean Mateway rewrite. The current goal is a small, useful tool agent that can run from CLI and Feishu.

The architecture direction is fixed by `docs/总规划.md`. Do not keep expanding or repeatedly overturning the main design. From here, execution should follow `docs/开发TODO.md` and land one item at a time.

The next development stage is memory, not multi-agent runtime routing. Multi-agent profile configuration may exist as a contract, but do not implement supervisor, spawn/subagent, DAG routing, or gateway multi-agent business routing until the single-agent memory path is stable.

## Project Rules

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
- Treat `docs/总规划.md` as the architecture contract for this phase. It answers "the system should look like what".
- Put execution work into `docs/开发TODO.md`. It answers "what to build next, where it lives, and how to accept it".
- Do not bloat `docs/总规划.md` into an endless idea list. New work should normally become TODO items, not architecture churn.
- Code changes should follow the current TODO order unless the user explicitly changes priority.
- Maintain `docs/进度.md` in Chinese. Whenever meaningful work is completed, blocked, or deferred, update it in the same change so the user can see current completed and unfinished items.
- New capabilities must state their tool name, risk, arguments, evidence, and confirmation boundary.
- Except for tests and data-only lexicon/resource files, do not add Chinese text to code. Production Go code, comments, errors, CLI/help text, prompt text, and user-facing strings added in code should be English. Chinese matching terms may live in dedicated resource files when needed to preserve Chinese-language behavior.

## Connector Direction

- Do not hard-code `larkcli` or other traditional software integrations into the runtime.
- If `larkcli`, API integrations, or enterprise CLI tools are added later, expose them through scanned connector/skill configuration.
- A connector should declare tool name, risk, arguments, evidence, auth requirements, and confirmation boundary.
- Runtime may scan connector config and register tools, but specific business integrations should remain optional and user-enabled.

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
+ Memory
+ later: Multi-Agent Profiles
```

Execution principle:

```text
总纲定方向；
TODO 定执行；
代码按 TODO 一项项落地。
```

## General Coding Guidelines

Behavioral guidelines to reduce common LLM coding mistakes.

**Tradeoff:** These guidelines bias toward caution over speed. For trivial tasks, use judgment.

## 1. Think Before Coding

**Don't assume. Don't hide confusion. Surface tradeoffs.**

Before implementing:
- State your assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them - don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.

## 2. Simplicity First

**Minimum code that solves the problem. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

## 3. Surgical Changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:
- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match existing style, even if you'd do it differently.
- If you notice unrelated dead code, mention it - don't delete it.

When your changes create orphans:
- Remove imports/variables/functions that YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

The test: Every changed line should trace directly to the user's request.

## 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:
- "Add validation" → "Write tests for invalid inputs, then make them pass"
- "Fix the bug" → "Write a test that reproduces it, then make it pass"
- "Refactor X" → "Ensure tests pass before and after"

For multi-step tasks, state a brief plan:
```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
```

Strong success criteria let you loop independently. Weak criteria ("make it work") require constant clarification.

---

**These guidelines are working if:** fewer unnecessary changes in diffs, fewer rewrites due to overcomplication, and clarifying questions come before implementation rather than after mistakes.
