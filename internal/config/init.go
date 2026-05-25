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
		{RelPath: filepath.Join("models", "local-mlx.yaml"), Content: localMLXYAMLTemplate},
		{RelPath: filepath.Join("models", "local-mlx.sample.yaml"), Content: localMLXSampleYAMLTemplate},
		{RelPath: filepath.Join("channels", "_README.md"), Content: channelsReadmeTemplate},
		{RelPath: filepath.Join("channels", "feishu.yaml"), Content: feishuYAMLTemplate},
		{RelPath: filepath.Join("channels", "feishu.sample.yaml"), Content: feishuSampleYAMLTemplate},
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
  roles:
    planning: minimax
    repair: minimax
    synthesis: minimax
    followup: minimax

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
        roles:
          planning: minimax
          repair: minimax
          synthesis: minimax
          followup: minimax
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
  provider_order: [cache, duckduckgo, tavily]
  cache_enabled: true
  cache_ttl_hours: 168
  fresh_cache_ttl_hours: 6
  providers:
    tavily:
      enabled: false
      api_key_env: TAVILY_API_KEY
      timeout_seconds: 8
      max_results: 5
      daily_budget: 20
      monthly_budget: 900
      search_depth: basic
      topic: general
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
TAVILY_API_KEY=

MATEWAY_FEISHU_APP_ID=
MATEWAY_FEISHU_APP_SECRET=
MATEWAY_FEISHU_VERIFICATION_TOKEN=
MATEWAY_FEISHU_ENCRYPT_KEY=
`

const configReadmeTemplate = `# Mateway Configuration

Default runtime layout:

` + "```text" + `
~/.mateway/config/
  config.yaml
  config.sample.yaml
  mateway.env
  mateway.env.sample
  models/
    minimax.yaml
    minimax.sample.yaml
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
- ` + "`roles.planning/repair/synthesis/followup`" + ` reserves model choices for runtime phases. The default model is used until role routing is implemented.
- ` + "`security.enforce_workspace_paths: true`" + ` restricts file tools to projectRoot, workspace, and accessible_paths.
- ` + "`memory`" + ` configures the Markdown/Obsidian-compatible memory wiki and proposal policy.
- ` + "`learning.skill_crystallization`" + ` controls event-driven skill candidate generation after repeated successful patterns.
- ` + "`scheduler`" + ` is reserved for best-effort background maintenance; it is not a strict cron.

New users may copy sample files:

` + "```bash" + `
cp config.sample.yaml config.yaml
cp mateway.env.sample mateway.env
cp models/minimax.sample.yaml models/minimax.yaml
cp models/local-mlx.sample.yaml models/local-mlx.yaml
cp channels/feishu.sample.yaml channels/feishu.yaml
` + "```" + `
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
type: project | system | preference | playbook | decision | source | skill_candidate
scope: agent | user | org
owner_agent: main
visibility: private | shared-user | shared-org
status: active | proposed | deprecated
tags: []
aliases: []
sources: []
confidence: high | medium | low
created_at: 2026-05-20
updated_at: 2026-05-20
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

const memoryAgentEntryTemplate = `# Agent Memory Entry

This is the prompt-facing memory entry for the agent.

Keep it short. Link to detailed wiki pages instead of copying everything here.

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
