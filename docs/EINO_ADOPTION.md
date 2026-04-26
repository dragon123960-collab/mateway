# Mateway x Eino Adoption Notes

Last updated: 2026-04-24

## Why This Document Exists

This note records the current design decisions around adopting Eino as the main agent runtime for Mateway, so we do not lose discussion context while refactoring.

The goal is not to replace the whole Mateway platform shell. The goal is to let Eino own agent execution, while Mateway keeps platform concerns such as channel adapters, workspace layout, approval UI, run store, and memory persistence.

## Official Eino Findings

The following conclusions are based on official CloudWeGo Eino docs reviewed on 2026-04-24:

- Eino ADK `Runner` is the core runtime engine for agent execution, lifecycle management, interrupt, callback, and checkpoint.
- Eino `ChatModelAgent` is the default agent loop abstraction for tool-calling agents.
- Eino `Supervisor` is the built-in centralized multi-agent collaboration pattern.
- Eino `Plan-Execute Agent` already provides a structured "plan -> execute -> replan" pattern and should be preferred over inventing our own planner loop from scratch.
- Eino `Skill` middleware already supports progressive disclosure:
  - discovery only loads skill name and description
  - activation loads full `SKILL.md`
  - execution can continue by loading scripts, references, and assets on demand
- Eino `Summarization` middleware can compress long conversations and should be reused for context reduction instead of building a second summarizer first.
- Eino `Callback` is the official extension point for tracing, logging, metrics, and intermediate execution visibility.
- Eino `Workflow` is not a replacement for agents; it is a peer orchestration layer to Graph, best used for deterministic business flows.
- Eino docs explicitly state that `Memory`, `Session`, and `Store` are business-layer concepts, not core framework-owned persistence.

## Key Official References

- Overview: https://www.cloudwego.io/docs/eino/overview/
- ADK Runner and Extension: https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_extension/
- ChatModelAgent: https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_implementation/chat_model/
- Supervisor Agent: https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_implementation/supervisor/
- Plan-Execute Agent: https://www.cloudwego.io/zh/docs/eino/core_modules/eino_adk/agent_implementation/plan_execute/
- Skill middleware: https://www.cloudwego.io/zh/docs/eino/core_modules/eino_adk/eino_adk_chatmodelagentmiddleware/middleware_skill/
- Summarization middleware: https://www.cloudwego.io/zh/docs/eino/core_modules/eino_adk/eino_adk_chatmodelagentmiddleware/middleware_summarization/
- Memory and Session note: https://www.cloudwego.io/docs/eino/quick_start/chapter_03_memory_and_session/
- Callback manual: https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/callback_manual/
- Workflow orchestration: https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/workflow_orchestration_framework/

## What We Should Reuse From Eino

### 1. Agent execution core

Use Eino for:

- `ChatModelAgent`
- `Runner`
- `Interrupt / Resume`
- `Supervisor`
- `Plan-Execute`
- `Callback`
- `Summarization`
- `Skill` progressive disclosure

This means Mateway should stop growing its own hand-written ReAct loop.

### 2. Progressive disclosure for skills

This is directly aligned with our goals.

Preferred direction:

- keep current Mateway skill discovery and provider registry
- map local skills into Eino Skill-compatible discovery metadata
- load only skill name + description into the initial agent context
- load `SKILL.md` only when the agent selects that skill
- use `SKILL.md` as the formal skill entry instead of inventing a separate primary skill manifest
- keep any runtime-specific executable binding in an optional compatibility layer such as `_meta.json`, not as a new ecosystem-wide standard

### 3. Planning / task decomposition

For complex tasks we should prefer:

- Eino `Plan-Execute Agent`
- optional `SequentialThinking` tool for hard reasoning cases

Instead of relying only on an implicit "model thinks and calls tools" loop, we should expose a first-class planning stage for complex requests.

### 4. Context reduction

For long running sessions, we should not maintain only our own rolling summary path.

Preferred direction:

- keep Mateway summaries and persistent notes as platform memory
- add Eino `Summarization` middleware inside runtime execution
- let Eino compress active context
- let Mateway persist the resulting summary / reflection artifacts

### 5. Trace / learning visibility

We should use Eino `Callback` as the primary execution observation hook and feed its events into Mateway run trace, reflection, and learning summary views.

## What Should Remain Mateway-Owned

The following should remain in the Mateway platform shell:

- Feishu / CLI / future channel adapters
- config loading and compatibility migration
- workspace structure and bootstrap
- run persistence in `workspace/memory/runs`
- approval command surface:
  - `/approvals`
  - `/approve`
  - `/deny`
- persistent session transcript and memory stores
- long-term memory / reflection / knowledge sinks
- capability compiler
- provider registry for builtin tools, skills, CLI, MCP, API adapters
- scheduling / recurring jobs

Reason: Eino provides runtime execution primitives, but not our product-specific host shell.

## Memory Decision

Avoid duplicate wheel-building here:

- Do **not** expect Eino to own our persistent memory store.
- Do **not** remove Mateway memory just because Eino has session examples.
- Do reuse Eino middleware where it helps runtime context handling.

Practical split:

- Eino owns in-run context handling:
  - active messages
  - tool loop state
  - interrupt checkpoint
  - summarization middleware
- Mateway owns durable memory:
  - session transcript
  - run store
  - approval records
  - reflection
  - long-term knowledge
  - user preferences
  - task recall

## Core Runtime Flow We Want

Target flow for a non-trivial task:

1. User sends a task
2. Mateway creates a `run`
3. Runtime chooses execution mode:
   - direct `ChatModelAgent`
   - `Plan-Execute Agent` for complex tasks
   - `Supervisor` for multi-agent work
4. Runtime discovers visible tools / skills / MCP / CLI / API based on capability policy
5. Runtime runs plan or direct execution
6. Tools and subagents execute
7. If a step fails, runtime retries by alternate tool or replans
8. If a risky operation appears, runtime interrupts for approval
9. Runtime completes and writes:
   - final answer
   - run trace
   - learning summary
   - reflection / memory artifacts

## Development-Time Visibility Requirement

During development we want the agent to expose more than the final answer.

Every run should eventually be able to show:

- task goal
- chosen execution mode
- first-pass plan or decomposition
- visible tools / skills at start
- tool selection rationale
- step-by-step execution trace
- failures and fallback choices
- final result
- post-task learning summary

This should be developer-visible first, and optionally hidden later in production mode.

## Mateway Work Items On Top Of Eino

These are the main Mateway-specific tasks still needed even after adopting Eino:

### Platform integration

- stabilize `internal/einoharness` or equivalent runtime bridge
- keep `Harness.Start / Resume / GetRun / ListVisibleTools` stable
- map Mateway `Tool` into Eino tools
- map approval store into Eino interrupt / resume
- map run steps and reflection into Eino callbacks

### Skills / tools / provider surface

- connect Mateway skill catalog to Eino Skill middleware semantics
- keep CLI / MCP / API tools as provider-driven integrations
- add dynamic tool reduction before agent invocation
- support gradual loading of skills and tools based on task type

### Memory

- keep current persistent memory stores
- add markdown-based compiled wiki memory for durable synthesized knowledge
- add Eino summarization middleware to runtime
- connect summary outputs into `workspace/memory/summaries`
- add reflection-to-knowledge promotion flow

### Planning and decomposition

- add a complex-task gate that routes to Plan-Execute
- store plan artifacts in run steps
- expose plan and replans via `/trace` and `/learn`

### Observability

- build callback bridge from Eino to Mateway trace
- add developer-facing execution summary view
- distinguish `llm failure`, `tool failure`, `approval wait`, and `runtime failure`

## Explicit Non-Goals

These are not first-stage priorities:

- replacing all persistent memory with an Eino-native mechanism
- building a second custom planner before evaluating Eino Plan-Execute
- moving channel logic into Eino
- hiding all traces during development

## Current Decision

The current direction is:

- Eino for runtime core
- Mateway for host shell
- reuse Eino middleware aggressively where it already solves the problem
- only build custom platform logic where Eino intentionally leaves business ownership to the application
