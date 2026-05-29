package config

import (
	"fmt"
	"os"
	"path/filepath"
)

type templateFile struct {
	RelPath string
	Content string
}

func EnsureDefaultConfigFiles(home string) error {
	loader := NewLoader(home)
	files := []templateFile{
		{RelPath: "README.md", Content: configReadmeTemplate},
		{RelPath: "config.yaml", Content: configYAMLTemplate},
		{RelPath: "config.sample.yaml", Content: configSampleYAMLTemplate},
		{RelPath: "mateway.env.sample", Content: envSampleTemplate},
		{RelPath: filepath.Join("models", "minimax.yaml"), Content: minimaxYAMLTemplate},
		{RelPath: filepath.Join("models", "minimax.sample.yaml"), Content: minimaxSampleYAMLTemplate},
		{RelPath: filepath.Join("models", "openai-gpt54-mini.yaml"), Content: openAIGPT54MiniYAMLTemplate},
		{RelPath: filepath.Join("models", "openai-gpt54-mini.sample.yaml"), Content: openAIGPT54MiniSampleYAMLTemplate},
		{RelPath: filepath.Join("models", "local-mlx.yaml"), Content: localMLXYAMLTemplate},
		{RelPath: filepath.Join("models", "local-mlx.sample.yaml"), Content: localMLXSampleYAMLTemplate},
		{RelPath: filepath.Join("channels", "_README.md"), Content: channelsReadmeTemplate},
		{RelPath: filepath.Join("channels", "feishu.yaml"), Content: feishuYAMLTemplate},
		{RelPath: filepath.Join("channels", "feishu.sample.yaml"), Content: feishuSampleYAMLTemplate},
		{RelPath: filepath.Join("..", "workspace", "agents", "main", "agent.md"), Content: agentMainTemplate},
		{RelPath: filepath.Join("..", "workspace", "agents", "main", "soul.md"), Content: agentSoulTemplate},
		{RelPath: filepath.Join("..", "workspace", "agents", "main", "user.md"), Content: agentUserTemplate},
		{RelPath: filepath.Join("..", "workspace", "agents", "main", "tools.md"), Content: agentToolsTemplate},
		{RelPath: filepath.Join("..", "workspace", "agents", "main", "memory.md"), Content: agentMemoryTemplate},
		{RelPath: filepath.Join("..", "workspace", "agents", "main", "skills", "README.md"), Content: agentSkillsReadmeTemplate},
		{RelPath: filepath.Join("..", "workspace", "skills", "software-install", "SKILL.md"), Content: skillSoftwareInstallTemplate},
		{RelPath: filepath.Join("..", "workspace", "skills", "fresh-search", "SKILL.md"), Content: skillFreshSearchTemplate},
		{RelPath: filepath.Join("..", "workspace", "skills", "source-evaluation", "SKILL.md"), Content: skillSourceEvaluationTemplate},
		{RelPath: filepath.Join("..", "workspace", "skills", "connector-gap", "SKILL.md"), Content: skillConnectorGapTemplate},
		{RelPath: filepath.Join("..", "workspace", "memory", "README.md"), Content: memoryReadmeTemplate},
		{RelPath: filepath.Join("..", "workspace", "memory", "schema.md"), Content: memorySchemaTemplate},
		{RelPath: filepath.Join("..", "workspace", "memory", "index.md"), Content: memoryIndexTemplate},
		{RelPath: filepath.Join("..", "workspace", "memory", "log.md"), Content: memoryLogTemplate},
		{RelPath: filepath.Join("..", "workspace", "memory", "user", "index.md"), Content: memoryUserIndexTemplate},
		{RelPath: filepath.Join("..", "workspace", "memory", "org", "index.md"), Content: memoryOrgIndexTemplate},
		{RelPath: filepath.Join("..", "workspace", "memory", "agents", "main", "memory.md"), Content: memoryAgentEntryTemplate},
		{RelPath: filepath.Join("..", "workspace", "memory", "agents", "main", "index.md"), Content: memoryAgentIndexTemplate},
	}
	for _, file := range files {
		path := filepath.Join(loader.ConfigDir(), file.RelPath)
		if err := writeFileIfMissing(path, file.Content); err != nil {
			return err
		}
	}
	return nil
}

func writeFileIfMissing(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir for %s: %w", path, err)
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

const configYAMLTemplate = `app:
  name: mateway
  home: ""
  workspace: ""

model:
  default: minimax
  fallbacks: []

memory:
  enabled: true
  root: ""
  recent_days: 3
  auto_propose: true
  auto_commit_low_risk: false
  require_confirm_for:
    - user_preference
    - org_knowledge
    - long_memory
    - skill_candidate

learning:
  enabled: true
  skill_crystallization:
    enabled: true
    success_threshold: 3
    min_confidence: medium
    require_user_confirm: true
    ask_timing: next_interaction

skills:
  catalogs:
    - name: skills.sh
      enabled: true
      base_url: https://skills.sh
      search_url: "https://skills.sh/?q={query}"
      trust_level: high
    - name: skillhub.cn
      enabled: false
      base_url: https://skillhub.cn
      search_url: "https://skillhub.cn/search?q={query}"
      trust_level: unknown
    - name: clawhub.ai
      enabled: false
      base_url: https://clawhub.ai
      search_url: "https://clawhub.ai/search?q={query}"
      trust_level: medium

scheduler:
  enabled: false
  timezone: Asia/Shanghai
  state_dir: ""

agents:
  default: main
  profiles:
    - id: main
      name: Main Assistant
      default: true
      session_namespace: main
      model:
        default: minimax
        fallbacks: []
      heartbeat:
        enabled: false
        interval: 30m
        schedule:
          daily_at: "03:30"
        jobs:
          - memory_daily_review
          - memory_recent_compact
          - memory_lint
        auto_send_summary: false
        quiet_hours:
          start: "23:00"
          end: "08:00"
      skills:
        allow: []
        deny: []
      tools:
        allow: []
        deny: []
  bindings:
    - channel: cli
      agent_id: main
    - channel: feishu
      agent_id: main

security:
  enforce_workspace_paths: true
  require_approval_for_risky_tools: true
  accessible_paths: []

search:
  default_tool: tavily
  provider_order: [tavily, searxng, duckduckgo]
  cache_enabled: true
  cache_ttl_hours: 168
  fresh_cache_ttl_hours: 6
  providers:
    tavily:
      enabled: false
      base_url: https://api.tavily.com/search
      api_key_env: TAVILY_API_KEY
      timeout_seconds: 8
      max_results: 5
      daily_budget: 20
      monthly_budget: 900
      search_depth: basic
      topic: general
    searxng:
      enabled: false
      base_url: http://127.0.0.1:8088
      timeout_seconds: 8
      max_results: 5
    duckduckgo:
      enabled: true
      timeout_seconds: 4
      max_results: 5
      region: cn-zh
`

const configSampleYAMLTemplate = `# Copy this file to config.yaml, then adjust model defaults, agents, and bindings.
# Model endpoint details live in models/*.yaml.
` + configYAMLTemplate

const minimaxYAMLTemplate = `name: minimax
provider: minimax
api: anthropic
model: MiniMax-M2.7
api_base: https://api.minimaxi.com/anthropic
api_key: ""
api_key_env: MINIMAX_API_KEY
strip_reasoning: true
enabled: true
description: Default remote model for planning, repair, synthesis, and high-reliability tasks.
`

const minimaxSampleYAMLTemplate = `# Copy this file to minimax.yaml.
# Put the real key in mateway.env as MINIMAX_API_KEY.

` + minimaxYAMLTemplate

const openAIGPT54MiniYAMLTemplate = `name: openai-gpt54-mini
provider: openai
api: openai
model: gpt-5.4-mini
api_base: https://api.openai.com/v1
api_key: ""
api_key_env: OPENAI_API_KEY
strip_reasoning: false
enabled: false
description: OpenAI GPT-5.4 mini model for optional planning/repair/synthesis experiments.
`

const openAIGPT54MiniSampleYAMLTemplate = `# Copy this file to openai-gpt54-mini.yaml.
# Put the real key in mateway.env as OPENAI_API_KEY.

` + openAIGPT54MiniYAMLTemplate

const localMLXYAMLTemplate = `name: local-mlx
provider: mlx_lm
api: openai
model: Qwen2.5-14B-Instruct-4bit
api_base: http://127.0.0.1:8080/v1
api_key: local
api_key_env: ""
strip_reasoning: false
enabled: false
description: "Local mlx_lm.server model on 127.0.0.1:8080. Set enabled: true after the server is running."
`

const localMLXSampleYAMLTemplate = `# Copy this file to local-mlx.yaml.
# Start mlx_lm.server locally before enabling this model.

` + localMLXYAMLTemplate

const feishuYAMLTemplate = `feishu:
  enabled: false
  app_id: ""
  app_id_env: MATEWAY_FEISHU_APP_ID
  app_secret: ""
  app_secret_env: MATEWAY_FEISHU_APP_SECRET
  verification_token: ""
  verification_token_env: MATEWAY_FEISHU_VERIFICATION_TOKEN
  encrypt_key: ""
  encrypt_key_env: MATEWAY_FEISHU_ENCRYPT_KEY
  base_url: https://open.feishu.cn
  bot_name: mateway
  auto_reply: true
  mention_required_in_group: true
  webhook:
    enabled: false
    addr: 127.0.0.1:8788
    path: /feishu/events
  websocket:
    enabled: true
`

const feishuSampleYAMLTemplate = `# Copy this file to channels/feishu.yaml.
# Put real secrets in mateway.env and keep direct secret fields empty.

` + feishuYAMLTemplate

const envSampleTemplate = `# Copy this file to mateway.env.
# Keep mateway.env private and do not commit it.

MINIMAX_API_KEY=
OPENAI_API_KEY=
TAVILY_API_KEY=

MATEWAY_FEISHU_APP_ID=
MATEWAY_FEISHU_APP_SECRET=
MATEWAY_FEISHU_VERIFICATION_TOKEN=
MATEWAY_FEISHU_ENCRYPT_KEY=
`

const configReadmeTemplate = `# Mateway Configuration

Default runtime layout:

` + "```text" + `
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
` + "```" + `

Config files only:

` + "```text" + `
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
` + "```" + `

Notes:

- ` + "`config.yaml`" + ` defines app paths, security, search, global model defaults, and agent profiles.
- ` + "`models/*.yaml`" + ` declares model endpoints, API compatibility, model names, and secret sources.
- ` + "`channels/feishu.yaml`" + ` configures Feishu and is disabled by default.
- ` + "`mateway.env`" + ` stores local secrets and should not be committed.
- ` + "`*.sample.yaml`" + ` files are user templates and are ignored by the runtime loader.
- Top-level ` + "`model`" + ` is the global default template; ` + "`agents.profiles[].model`" + ` overrides it for a specific agent.
- ` + "`security.enforce_workspace_paths: true`" + ` restricts file tools to projectRoot, workspace, and accessible_paths.
- ` + "`workspace/agents/main/*.md`" + ` stores editable prompt-facing profile context.
- ` + "`workspace/memory`" + ` is reserved for Markdown/Obsidian-compatible memory.
- ` + "`workspace/agents/main/skills`" + ` is reserved for agent-specific skills.
- ` + "`workspace/skills`" + ` contains editable shared skills. Init seeds a small default set that users may modify.
- ` + "`sessions`" + ` and ` + "`trace`" + ` are runtime state directories, created when tasks run.
- Old directories such as ` + "`skills`" + `, ` + "`schedules`" + `, ` + "`workspace/scheduled`" + `, or ` + "`workspace/web-cache`" + ` may exist from older Mateway builds; the current minimal runtime does not require them.

New users may copy sample files:

` + "```bash" + `
cp config.sample.yaml config.yaml
cp mateway.env.sample mateway.env
cp models/minimax.sample.yaml models/minimax.yaml
cp models/openai-gpt54-mini.sample.yaml models/openai-gpt54-mini.yaml
cp models/local-mlx.sample.yaml models/local-mlx.yaml
cp channels/feishu.sample.yaml channels/feishu.yaml
` + "```" + `
`

const agentMainTemplate = `# main agent

Default behavior:

- Use the user's language unless they request another language.
- Do not dump raw information without synthesis.
- Help the user filter, summarize, compare, and decide.
- When current information matters, prefer official and up-to-date sources.
- If information may be stale, say so clearly.
- If a tool call fails, do not invent the result.
`

const agentSoulTemplate = `# main soul

You are Mateway, a practical personal work assistant agent.

Core objectives:

- Help the user complete concrete work.
- Organize information into clear, useful conclusions.
- Use tools safely and only when they materially help.
- Answer in the user's language unless the user requests another language.
`

const agentUserTemplate = `# main user

User profile:

- No stable user preferences have been recorded yet.
- Record preferences only when they are explicit, durable, and useful for future tasks.
- Keep user preferences human-readable and easy to edit.
`

const agentToolsTemplate = `# main tools

Tool-use rules:

1. Plan before using tools.
2. Do not expose raw tool calls, internal arguments, or implementation traces to the user.
3. Tool results will be supplied by the system.
4. Final answers must be structured, readable, and written in the user's language unless requested otherwise.
`

const agentMemoryTemplate = `# main memory

Prompt-facing memory summary for this agent.

Keep this file short because it may be injected into model context.

Use it only for stable, user-approved facts, compact preferences, or links to curated memory wiki pages.

Detailed long-term memory belongs under workspace/memory/agents/main/.
`

const agentSkillsReadmeTemplate = `# main agent skills

Put agent-specific skills here as:

` + "```text" + `
skills/<skill-name>/SKILL.md
` + "```" + `

Shared skills can live under ` + "`workspace/skills`" + `. Agent-specific skills win when names collide.

Mateway discovers skills from both locations:

1. ` + "`workspace/agents/main/skills/<skill-name>/SKILL.md`" + ` for agent-specific overrides.
2. ` + "`workspace/skills/<skill-name>/SKILL.md`" + ` for shared installed skills.

You do not need to copy or symlink a shared skill into this directory unless you want an agent-specific override.
`

const skillSoftwareInstallTemplate = `---
name: software-install
description: Use when the user asks to install, configure, or verify CLI software or developer tools.
stage: planning
priority: 90
---

# software-install

Goal: complete software installation tasks using the smallest safe path.

Workflow:

1. Identify the official source first.
   Prefer official docs, GitHub repositories, package manager pages, or release pages.
   Do not guess repository owners, binary names, package names, or download URLs.

2. Read install instructions before proposing commands.
   Use web.fetch for README/docs/package pages.
   Use terminal.run only for local environment checks, guarded installation, verification, and PATH diagnosis.

3. Before installing, summarize:
   - official_source
   - install_method
   - install_command
   - verify_command
   - executable_name
   - why this method fits the current machine

4. If the command is risky or mutates the machine, wait for confirmation through Mateway's guarded tool flow.

5. Verify after installation.
   Prefer command -v, --version, --help, or a documented quick-start command.
   If install succeeds but verification fails, diagnose PATH and executable location before switching methods.

Never claim installation succeeded unless a tool command or file write actually completed and verification evidence exists.
`

const skillFreshSearchTemplate = `---
name: fresh-search
description: Use when the user asks for today, latest, current, real-time, prices, weather, releases, or news.
stage: planning
priority: 80
---

# fresh-search

Goal: avoid stale answers.

Rules:

1. Use the runtime current date exactly when building search queries.
2. Prefer official/primary sources, then reputable secondary sources.
3. For "today" claims, require a date/time clue from the source or explicitly state that freshness could not be verified.
4. If a direct source times out, try an official API, mirror, search result, or alternate source before giving up.
5. Do not silently downgrade "today" into "recent"; say so when only recent evidence is available.
`

const skillSourceEvaluationTemplate = `---
name: source-evaluation
description: Use to rank sources by officialness, freshness, reliability, and actionability.
stage: synthesis
priority: 70
---

# source-evaluation

When comparing sources, score them by:

1. Official or primary source status.
2. Date freshness and whether the date matches the user request.
3. Specific evidence: URLs, versions, timestamps, authors, repository activity, or command output.
4. Risk: whether following the source could mutate local state, leak secrets, or install untrusted code.

If sources disagree, explain the disagreement and choose the safer conclusion.
`

const skillConnectorGapTemplate = `---
name: connector-gap
description: Use when a task needs missing mail, SSH, publishing, calendar, SaaS, or other external connectors.
stage: planning
priority: 85
---

# connector-gap

Goal: still help complete the user's real task when a direct connector is missing.

Workflow:

1. Do not stop at "not supported".
2. Check safe local capabilities first:
   - available CLIs with command -v
   - local app configuration
   - documented config files
   - existing scripts under the workspace or ~/.mateway
3. If a script can bridge the gap, propose or create a small script with:
   - required inputs
   - environment variables
   - safety boundaries
   - verification command
4. Before creating a script, verify the target runtime exists.
   Examples:
   - Python script: command -v python3 && python3 --version
   - Node script: command -v node && node --version
   - Shell script with external tools: command -v for each required executable
   If the runtime is missing, choose an available runtime or stop with setup instructions.
5. If real credentials, server hostnames, recipients, or platform choices are missing, ask only for those concrete fields.
6. Never claim that email was sent, a server was checked, or content was published unless a tool/script/action actually did it.
`

const memoryReadmeTemplate = `# Mateway Memory Wiki

This directory is the local Markdown/Obsidian-compatible memory wiki.

- Markdown files are the source of truth.
- SQLite indexes, if added later, must be rebuildable from Markdown.
- Agent-private memory lives under ` + "`agents/<agent_id>/`" + `.
- Shared user memory lives under ` + "`user/`" + `.
- Shared organization memory lives under ` + "`org/`" + `.
- High-impact memories and skill candidates should start in an inbox as proposals.
`

const memorySchemaTemplate = `# Memory Schema

Every durable memory page should use YAML frontmatter:

` + "```yaml" + `
---
type: preference | decision | experience | skill | pattern | wiki | diary | reflection | proposal
scope: global | user | org | agent | project
owner_agent: main
project_id:
visibility: private | shared-user | shared-org
status: proposed | active | rejected | deprecated | archived
tags: []
aliases: []
op_fingerprint:
sources:
  - trace:<trace_id>
confidence: high | medium | low
created_at: 2026-05-29
updated_at: 2026-05-29
review_after:
schema_version: 1
---
` + "```" + `

Use Obsidian-style ` + "`[[wikilinks]]`" + ` for graph connections.
`

const memoryIndexTemplate = `# Memory Index

- [[schema]]
- [[log]]
- [[user/index]]
- [[org/index]]
- [[agents/main/index]]
`

const memoryLogTemplate = `# Memory Log

Append memory operations here: ingest, query, lint, commit, and skill-candidate promotion.
`

const memoryUserIndexTemplate = `# Shared User Memory

Use this area for stable user preferences and cross-agent user facts.
`

const memoryOrgIndexTemplate = `# Shared Organization Memory

Use this area for organization systems, terminology, workflows, and playbooks.
`

const memoryAgentEntryTemplate = `---
type: wiki
scope: agent
owner_agent: main
project_id:
visibility: private
status: proposed
tags: []
aliases: []
op_fingerprint:
sources: []
confidence: low
created_at: 2026-05-29
updated_at: 2026-05-29
review_after:
schema_version: 1
---

# Agent Memory Entry

This is the long-term memory wiki entry for the agent.

It can link to detailed pages under this memory wiki. Do not assume this whole file is injected into every model prompt.

- Agent memory index: [[index]]
`

const memoryAgentIndexTemplate = `# Main Agent Memory

## Recent

## Long-Term

## Inbox

## Learning
`

const channelsReadmeTemplate = `# Channel Configs

` + "`feishu.sample.yaml`" + ` is safe to keep as a user-facing template.

Copy it to ` + "`feishu.yaml`" + `, then enable Feishu only after app_id/app_secret are configured.

Recommended:

- Keep direct secret fields empty.
- Put real secrets in ` + "`../mateway.env`" + `.
- Use ` + "`*_env`" + ` fields in ` + "`feishu.yaml`" + `.
`
