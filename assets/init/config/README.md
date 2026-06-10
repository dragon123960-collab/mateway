# Mateway Configuration

Default runtime layout:

```text
~/.mateway/
  config/
    config.yaml
    config.sample.yaml
    mateway.env
    mateway.env.sample
    models/
      minimax.yaml
      minimax.sample.yaml
      openai-gpt54-mini.yaml
      openai-gpt54-mini.sample.yaml
      local-mlx.yaml
      local-mlx.sample.yaml
    channels/
      feishu.yaml
      feishu.sample.yaml
  workspace/
    agents/
      main/
        agent.md
        soul.md
        user.md
        tools.md
        memory.md
        skills/
          README.md
          <optional agent-specific skills>
    memory/
      README.md
      schema.md
      index.md
      log.md
      user/index.md
      org/index.md
      agents/main/index.md
      agents/main/memory.md
    skills/
      <optional shared skills>
  sessions/
    <runtime session state json>
  trace/
    <runtime trace jsonl>
  run/
    mateway.lock
```

Config files only:

```text
~/.mateway/config/
  config.yaml
  config.sample.yaml
  mateway.env
  mateway.env.sample
  models/
    minimax.yaml
    minimax.sample.yaml
    openai-gpt54-mini.yaml
    openai-gpt54-mini.sample.yaml
    local-mlx.yaml
    local-mlx.sample.yaml
  channels/
    feishu.yaml
    feishu.sample.yaml
```

Notes:

- `config.yaml` defines app paths, security, search, global model defaults, and agent profiles.
- `models/*.yaml` declares model endpoints, API compatibility, model names, and secret sources.
- `channels/feishu.yaml` configures Feishu and is disabled by default.
- `mateway.env` stores local secrets and should not be committed.
- `*.sample.yaml` files are user templates and are ignored by the runtime loader.
- Top-level `model` is the global default template; `agents.profiles[].model` overrides it for a specific agent.
- `security.enforce_workspace_paths: true` restricts file tools to projectRoot, workspace, and accessible_paths.
- `workspace/agents/main/*.md` stores editable prompt-facing profile context.
- `workspace/memory` is reserved for Markdown/Obsidian-compatible memory.
- `workspace/agents/main/skills` is reserved for agent-specific skills.
- `workspace/skills` contains editable shared skills. Init seeds a small default set that users may modify.
- `sessions` and `trace` are runtime state directories, created when tasks run.
- Old directories such as `skills`, `schedules`, `workspace/scheduled`, or `workspace/web-cache` may exist from older Mateway builds; the current minimal runtime does not require them.

New users may copy sample files:

```bash
cp config.sample.yaml config.yaml
cp mateway.env.sample mateway.env
cp models/minimax.sample.yaml models/minimax.yaml
cp models/openai-gpt54-mini.sample.yaml models/openai-gpt54-mini.yaml
cp models/local-mlx.sample.yaml models/local-mlx.yaml
cp channels/feishu.sample.yaml channels/feishu.yaml
```
