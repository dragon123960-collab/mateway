# Configuration

Mateway stores local runtime data under `~/.mateway` by default. `mateway init` creates the initial config, workspace, skills, and memory templates from `assets/init`.

## Home Layout

```text
~/.mateway/
  config/
    config.yaml
    mateway.env
    models/
    channels/
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

## Main Config

`config/config.yaml` selects defaults and local runtime behavior:

- `app.home`: Mateway home directory.
- `app.workspace`: workspace root for profiles, skills, and memory.
- `model`: default model, fallback model, and role models.
- `agents`: agent profiles and channel bindings.
- `channels`: Feishu and Weixin configuration.
- `security`: workspace path enforcement, accessible paths, and terminal sandbox settings.
- `search`: web search providers and budgets.
- `scheduler`: local schedule loop settings.

Model definitions live in `config/models/*.yaml`. Channel definitions live in `config/channels/*.yaml`.

## Agents

Agent profiles can set:

- profile id and name
- workspace root and agent directory
- model selection
- heartbeat jobs
- skill allow/deny lists
- tool allow/deny lists
- channel bindings

Agent-specific skills override shared workspace skills when names collide.

## Skills

Default skills are installed as editable workspace assets:

```text
workspace/skills/<skill_name>/SKILL.md
```

They should stay as skills, not be embedded into runtime code. Runtime code owns hard boundaries; skills own user-editable workflow guidance.

Execution prompt context is gated:

- planning can discover skill headers
- contract can select required skills
- execution only receives selected skills or explicit skill/workflow context

## Secrets

Use the local secret store instead of writing credentials into config, skills, scripts, traces, or prompts.

```bash
mateway secret set <secret_id>
mateway secret list
```

`terminal.run` accepts `env_secrets` entries such as:

```json
{"id":"service/token","env":"SERVICE_TOKEN"}
```

Trace and evidence store only the secret id and environment variable name.

## Security Notes

`terminal.run` remains the only command execution tool. Destructive commands are blocked by tool policy. File tools enforce path validation. Secret-like values are redacted from traces, stored transcripts, task steps, and final replies.

Docker sandbox work is tracked separately and is not part of the current non-sandbox plan.
