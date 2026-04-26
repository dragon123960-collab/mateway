# Mateway Capabilities

## Default Builtin Capabilities

These are the capabilities the platform can expose without any third-party CLI installation:

- `web_search`
- `browser_fetch`
- `read_file`
- `read_skill_resource`
- `list_files`
- `search_text`
- `search_history`
- `search_scoped_memory`
- `read_memory`
- `read_session_summary`
- `recall_last_task`
- `write_file`
- `write_memory_note`
- `wiki_ingest`
- `wiki_query`
- `wiki_lint`
- `sandbox_exec`
- `create_workspace`
- `create_agent`
- `schedule_create`
- `schedule_list`
- `schedule_get`
- `schedule_enable`
- `schedule_disable`
- `schedule_remove`
- `spawn`
- `wait_agent`

## Default Search / Summary / Execution Shape

- Search first checks `wiki_query` / memory when available.
- External search prefers `web_search`, then `browser_fetch` for page reading.
- Execution prefers `sandbox_exec` over unconstrained shell behavior.
- Learning output is visible through Feishu `/trace` and `/learn`.
- High-value completed chat runs generate a wiki learning proposal under `workspace/memory/wiki/notes/`.

## Optional External Capabilities

These are not core runtime assumptions. They are attached through registry providers:

- local skills discovered from `SKILL.md` entries in configured skills roots
- external CLI providers from `~/.mateway/config/cli_providers/*.yaml`
- future MCP providers
- future API integration manifests

Examples:

- `opencli_*` tools when an `opencli` CLI provider is configured
- skill-backed tools discovered from workspace or home skills directories
- executable skills currently use `SKILL.md` plus optional `_meta.json` runtime binding

## Progressive Disclosure

The runtime does not need to expose the full tool universe to every task.

Current first-pass progressive disclosure now has two layers:

### 1. Tool surface reduction

- always keeps memory/history lookup tools available
- research-like goals expose search + reading + writeback tools
- file/code/workspace goals expose file tools
- execution goals expose `sandbox_exec`
- multi-agent goals expose `spawn` / `wait_agent`
- external CLI tools are only exposed when the task or tool name suggests a match
- dynamic scoring now also considers tool kind, tags, descriptions, CLI allowed commands, and skill metadata tags/keywords
- the selector tries to keep a small but diverse capability surface instead of exposing every tool at once

This is still implemented in the Eino tool binding layer.

### 1.5. Dynamic tool expansion

- when the capability surface is still large after first-pass reduction, `chatmodel` agents now keep a smaller default tool set and expose the rest behind Eino `tool_search`
- `tool_search` is recorded in run trace when used, so `/trace` and `/learn` can explain how the agent expanded its tool surface
- `plan_execute` now uses the same middleware-enabled executor path, so executor steps can also use `tool_search`

### 2. Skill activation reduction

- a dedicated `skill-picker` chooses 0-3 skills from the visible catalog for the current goal
- the picker uses visible skill discovery metadata only
- if model-based picking fails, the runtime falls back to heuristic selection
- selected skills are now connected to the Eino `skill` activation tool, so full `SKILL.md` content is loaded on demand instead of being inlined by default
- selected skills now expose standard resource inventories from `scripts/`, `references/`, and `assets/`
- non-standard resource directories can also be declared through skill frontmatter `resource_dirs`
- `read_skill_resource` provides a constrained lazy-load path for selected skill resources during execution
- both `chatmodel` and `plan_execute` routes now receive the same skill disclosure behavior

## Context Reduction

- Eino `Summarization` middleware is enabled for chat-style agents and the `plan_execute` executor
- summary artifacts are persisted under `workspace/memory/summaries/`
- large tool outputs now go through Eino `ToolReduction` middleware:
  - when `read_file` is available, large outputs are offloaded into workspace files and can be re-opened on demand
  - when `read_file` is not available, old tool outputs are still cleared to protect context without creating unreadable file references
- summarization / reduction / offload events are promoted into run trace and feed learn proposal generation

## What Is Not Assumed By Default

- `opencli` is not a builtin product concept
- Obsidian is not hardcoded into the runtime
- MCP providers are not hardcoded into the runtime
- browser automation / computer use is not part of phase 1

Those should remain external providers, skills, or CLI integrations.
