package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dongping/mateway/internal/agenttemplate"
	"gopkg.in/yaml.v3"
)

type templateFile struct {
	RelPath string
	Content string
}

func EnsureDefaultConfigFiles(home string) error {
	loader := NewLoader(home)
	mainAgentFiles := agenttemplate.CoreFiles(agenttemplate.Profile{ID: "main", Name: "Main Assistant"})
	files := []templateFile{
		{RelPath: "README.md", Content: configReadmeTemplate},
		{RelPath: "config.yaml", Content: configYAMLTemplate},
		{RelPath: "config.sample.yaml", Content: configSampleYAMLTemplate},
		{RelPath: "mateway.env.sample", Content: envSampleTemplate},
		{RelPath: filepath.Join("models", "minimax.yaml"), Content: minimaxYAMLTemplate},
		{RelPath: filepath.Join("models", "minimax.sample.yaml"), Content: minimaxSampleYAMLTemplate},
		{RelPath: filepath.Join("models", "glm-4.7-flash.yaml"), Content: glm47FlashYAMLTemplate},
		{RelPath: filepath.Join("models", "glm-4.7-flash.sample.yaml"), Content: glm47FlashSampleYAMLTemplate},
		{RelPath: filepath.Join("models", "glm-4.6v-flash.yaml"), Content: glm46VFlashYAMLTemplate},
		{RelPath: filepath.Join("models", "glm-4.6v-flash.sample.yaml"), Content: glm46VFlashSampleYAMLTemplate},
		{RelPath: filepath.Join("models", "agnes-2-flash.yaml"), Content: agnes2FlashYAMLTemplate},
		{RelPath: filepath.Join("models", "agnes-2-flash.sample.yaml"), Content: agnes2FlashSampleYAMLTemplate},
		{RelPath: filepath.Join("models", "openai-gpt54-mini.yaml"), Content: openAIGPT54MiniYAMLTemplate},
		{RelPath: filepath.Join("models", "openai-gpt54-mini.sample.yaml"), Content: openAIGPT54MiniSampleYAMLTemplate},
		{RelPath: filepath.Join("models", "local-mlx.yaml"), Content: localMLXYAMLTemplate},
		{RelPath: filepath.Join("models", "local-mlx.sample.yaml"), Content: localMLXSampleYAMLTemplate},
		{RelPath: filepath.Join("channels", "_README.md"), Content: channelsReadmeTemplate},
		{RelPath: filepath.Join("channels", "feishu.yaml"), Content: feishuYAMLTemplate},
		{RelPath: filepath.Join("channels", "feishu.sample.yaml"), Content: feishuSampleYAMLTemplate},
		{RelPath: filepath.Join("channels", "weixin.yaml"), Content: weixinYAMLTemplate},
		{RelPath: filepath.Join("channels", "weixin.sample.yaml"), Content: weixinSampleYAMLTemplate},
		{RelPath: filepath.Join("..", "workspace", "agents", "main", "agent.md"), Content: mainAgentFiles["agent.md"]},
		{RelPath: filepath.Join("..", "workspace", "agents", "main", "soul.md"), Content: mainAgentFiles["soul.md"]},
		{RelPath: filepath.Join("..", "workspace", "agents", "main", "user.md"), Content: mainAgentFiles["user.md"]},
		{RelPath: filepath.Join("..", "workspace", "agents", "main", "tools.md"), Content: mainAgentFiles["tools.md"]},
		{RelPath: filepath.Join("..", "workspace", "agents", "main", "memory.md"), Content: mainAgentFiles["memory.md"]},
		{RelPath: filepath.Join("..", "workspace", "agents", "main", "skills", "README.md"), Content: agentSkillsReadmeTemplate},
		{RelPath: filepath.Join("..", "workspace", "skills", "software-install", "SKILL.md"), Content: skillSoftwareInstallTemplate},
		{RelPath: filepath.Join("..", "workspace", "skills", "fresh-search", "SKILL.md"), Content: skillFreshSearchTemplate},
		{RelPath: filepath.Join("..", "workspace", "skills", "source-evaluation", "SKILL.md"), Content: skillSourceEvaluationTemplate},
		{RelPath: filepath.Join("..", "workspace", "skills", "connector-gap", "SKILL.md"), Content: skillConnectorGapTemplate},
		{RelPath: filepath.Join("..", "workspace", "skills", "skillcreate", "SKILL.md"), Content: skillCreateTemplate},
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
	if err := mergeDefaultYAMLFile(filepath.Join(loader.ConfigDir(), "config.yaml"), []byte(configYAMLTemplate)); err != nil {
		return err
	}
	if err := mergeDefaultYAMLFile(filepath.Join(loader.ConfigDir(), "channels", "feishu.yaml"), []byte(feishuYAMLTemplate)); err != nil {
		return err
	}
	if err := mergeDefaultYAMLFile(filepath.Join(loader.ConfigDir(), "channels", "weixin.yaml"), []byte(weixinYAMLTemplate)); err != nil {
		return err
	}
	return nil
}

func mergeDefaultYAMLFile(path string, defaults []byte) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var currentNode yaml.Node
	if err := yaml.Unmarshal(existing, &currentNode); err != nil {
		return err
	}
	var defaultNode yaml.Node
	if err := yaml.Unmarshal(defaults, &defaultNode); err != nil {
		return err
	}
	if mergeYAMLMapping(documentMapping(&currentNode), documentMapping(&defaultNode)) {
		out, err := yaml.Marshal(&currentNode)
		if err != nil {
			return err
		}
		return os.WriteFile(path, out, 0o644)
	}
	return nil
}

func documentMapping(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		return node.Content[0]
	}
	if node.Kind == yaml.MappingNode {
		return node
	}
	return nil
}

func mergeYAMLMapping(current, defaults *yaml.Node) bool {
	if current == nil || defaults == nil || current.Kind != yaml.MappingNode || defaults.Kind != yaml.MappingNode {
		return false
	}
	changed := false
	for i := 0; i+1 < len(defaults.Content); i += 2 {
		key := defaults.Content[i]
		value := defaults.Content[i+1]
		existing := mappingValue(current, key.Value)
		if existing == nil {
			current.Content = append(current.Content, cloneYAMLNode(key), cloneYAMLNode(value))
			changed = true
			continue
		}
		if existing.Kind == yaml.MappingNode && value.Kind == yaml.MappingNode {
			if mergeYAMLMapping(existing, value) {
				changed = true
			}
		}
	}
	return changed
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func cloneYAMLNode(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	clone := *node
	clone.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		clone.Content[i] = cloneYAMLNode(child)
	}
	return &clone
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
  locale: auto
  message_catalog_dir: ""

model:
  default: glm-4.7-flash
  fallbacks:
    - minimax
  roles:
    vision:
      - glm-4.6v-flash
      - minimax
    strong: minimax
    followup: glm-4.7-flash
    review: glm-4.7-flash

execution:
  max_parallel_tools: 4

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
  proposal_nudge:
    enabled: true
    interval: 24h
    channels:
      - cli
    max_proposals: 3

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
      install_url: ""
      trust_level: high
    - name: skillhub.cn
      enabled: false
      base_url: https://skillhub.cn
      search_url: "https://skillhub.cn/search?q={query}"
      install_url: ""
      trust_level: unknown
    - name: clawhub.ai
      enabled: false
      base_url: https://clawhub.ai
      search_url: "https://clawhub.ai/search?q={query}"
      install_url: ""
      trust_level: medium

scripts:
  dirs: []

scheduler:
  enabled: false
  timezone: Asia/Shanghai
  state_dir: ""
  interval: 30s

agents:
  default: main
  profiles:
    - id: main
      name: Main Assistant
      default: true
      session_namespace: main
      heartbeat:
        enabled: false
        interval: 30m
        schedule:
          daily_at: "03:30"
        jobs:
          - memory_lint
          - memory_index_rebuild
          - memory_distill
          - learning_distill
          - skill_learning
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
  terminal_sandbox:
    enabled: false
    mode: restricted
    workdir: ""
    timeout_seconds: 20
    command_prefix: []

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
model: MiniMax-M3
api_base: https://api.minimaxi.com/anthropic
api_key: ""
api_key_env: MINIMAX_API_KEY
modalities: [text, image]
context_window: 1000000
max_tokens: 8192
strip_reasoning: true
enabled: true
description: Default remote model for planning, repair, synthesis, and high-reliability tasks.
`

const minimaxSampleYAMLTemplate = `# Copy this file to minimax.yaml.
# Put the real key in mateway.env as MINIMAX_API_KEY.

` + minimaxYAMLTemplate

const glm47FlashYAMLTemplate = `name: glm-4.7-flash
provider: glm
api: openai_chat
model: GLM-4.7-Flash
api_base: https://open.bigmodel.cn/api/paas/v4
api_key: ""
api_key_env: GLM_API_KEY
modalities: [text]
context_window: 128000
max_tokens: 4096
strip_reasoning: false
enabled: false
description: GLM free/flash text model for low-cost reasoning tasks.
`

const glm47FlashSampleYAMLTemplate = `# Copy this file to glm-4.7-flash.yaml.
# Put the real key in mateway.env as GLM_API_KEY.

` + glm47FlashYAMLTemplate

const glm46VFlashYAMLTemplate = `name: glm-4.6v-flash
provider: glm
api: openai_chat
model: GLM-4.6V-Flash
api_base: https://open.bigmodel.cn/api/paas/v4
api_key: ""
api_key_env: GLM_API_KEY
modalities: [text, image]
context_window: 128000
max_tokens: 4096
strip_reasoning: false
enabled: false
description: GLM vision-capable flash model for image understanding and multimodal fallback.
`

const glm46VFlashSampleYAMLTemplate = `# Copy this file to glm-4.6v-flash.yaml.
# Put the real key in mateway.env as GLM_API_KEY.

` + glm46VFlashYAMLTemplate

const agnes2FlashYAMLTemplate = `name: agnes-2-flash
provider: agnes
api: openai_chat
model: agnes-2.0-flash
api_base: https://apihub.agnes-ai.com/v1
api_key: ""
api_key_env: AGNES_API_KEY
modalities: [text]
context_window: 128000
max_tokens: 8192
strip_reasoning: false
enabled: true
description: agnes-2.0-flash OpenAI-compatible fast agent model for tool workflows, coding, reasoning, and multi-turn production tasks.
`

const agnes2FlashSampleYAMLTemplate = `# Copy this file to agnes-2-flash.yaml.
# If the provider requires a key, put it in mateway.env as AGNES_API_KEY.

` + agnes2FlashYAMLTemplate

const openAIGPT54MiniYAMLTemplate = `name: openai-gpt54-mini
provider: openai
api: openai
model: gpt-5.4-mini
api_base: https://api.openai.com/v1
api_key: ""
api_key_env: OPENAI_API_KEY
modalities: [text]
context_window: 128000
max_tokens: 4096
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
modalities: [text]
context_window: 32768
max_tokens: 4096
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
MATEWAY_WEIXIN_BASE_URL=
MATEWAY_WEIXIN_ACCOUNT_ID=
MATEWAY_WEIXIN_TOKEN=
`

const weixinYAMLTemplate = `weixin:
  enabled: false
  base_url: ""
  base_url_env: MATEWAY_WEIXIN_BASE_URL
  account_id: ""
  account_id_env: MATEWAY_WEIXIN_ACCOUNT_ID
  token: ""
  token_env: MATEWAY_WEIXIN_TOKEN
  account_dir: ""
  media_dir: ""
  bot_agent: Mateway/0.1
  poll_timeout_ms: 35000
  retry_interval: 3s
  mention_required_in_group: true
`

const weixinSampleYAMLTemplate = `# Copy this file to channels/weixin.yaml.
# Put real iLink credentials in mateway.env and keep direct token fields empty.

` + weixinYAMLTemplate

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

3. Before installing, first check whether the executable already exists locally.
   Prefer command -v <executable> followed by a version/help command when safe.
   If it is already installed and verified, stop there and report the evidence instead of reinstalling.

4. Before running an install command, summarize:
   - official_source
   - install_method
   - install_command
   - verify_command
   - executable_name
   - why this method fits the current machine

5. If the command is risky or mutates the machine, wait for confirmation through Mateway's guarded tool flow.

6. Verify after installation.
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

const skillCreateTemplate = `---
name: skillcreate
description: Use when creating or updating a Mateway skill, especially when scripts, connectors, credentials, or secrets are involved.
stage: execution
priority: 90
aliases: skill create, create skill, skill creation
when_to_use: creating Mateway skills, updating Mateway skills, adding scripts to a skill, handling skill secrets
---

# skillcreate

Use this skill before creating or updating any Mateway skill.

Default behavior: create or update the requested skill files, make scripts executable, then verify discovery and at least one safe execution path. Do not stop after a plan unless required information is missing or a guarded tool requests confirmation.

## Directory rules

Mateway skills live under:

` + "```text" + `
workspace/agents/<agent_id>/skills/<skill_name>/
workspace/skills/<skill_name>/
` + "```" + `

Preferred layout:

` + "```text" + `
<skill_name>/
  SKILL.md
  scripts/
  references/
  assets/
` + "```" + `

- Put skill-specific executable scripts in <skill_name>/scripts/.
- Use workspace/scripts/, ~/.mateway/scripts/, or configured scripts.dirs only for reusable cross-skill scripts.
- If script names collide, agent-specific skill scripts win over shared skill scripts, which win over global scripts.
- Keep SKILL.md concise: trigger description, workflow, script names, required inputs, safety boundaries, and verification steps.

## Secret rules

- Never put plaintext secrets, passwords, tokens, authorization codes, or API keys in SKILL.md.
- Never hard-code plaintext secrets in scripts/.
- If the user has provided a concrete secret value in the current task, store it immediately with the secret.set tool. Do not ask the user to run mateway secret manually.
- Use mateway secret set <secret_id> only as a CLI fallback outside the agent loop; it is not the preferred answer to the user.
- If the value visible to tools is [REDACTED_SECRET] or any placeholder, do not store it; ask the user to provide the real value again.
- If the user has not provided a concrete secret value, write only required-secret references and report the missing secret ids.
- After secret.set succeeds, scripts receive secrets only through environment variables injected by script.run from mateway.required_secret headers.
- Script headers declare required secrets in the first 30 lines. The format must include both ` + "`id=`" + ` and ` + "`env=`" + ` exactly:

` + "```text" + `
# mateway.required_secret: id=<secret_id> env=<ENV_NAME>
` + "```" + `

- Inside scripts, read only the environment variable:

` + "```python" + `
password = os.environ.get("ENV_NAME")
if not password:
    sys.exit("missing required env ENV_NAME")
` + "```" + `

- Direct local execution may pass env manually; Mateway execution must use script.run, which injects env from secret store.
- Do not use terminal.run for credentialed endpoint tests. Credentialed tests must go through script.run so required_secret injection is the only credential path.
- Skill creation can complete without a working credential. Missing or rejected credentials only block the optional credentialed endpoint test, not the structure/install verification.
- Final answers must never repeat concrete secret values. Refer only to secret ids and env names.

## Script rules

Each executable skill script should include headers:

` + "```text" + `
# mateway.name: <skill_name>.<action>
# mateway.description: <short purpose>
# mateway.risk: safe_read | guarded_mutation
# mateway.required_secret: id=<secret_id> env=<ENV_NAME>
` + "```" + `

- Use namespaced script names such as email.receive or email.send.
- Put scripts under the skill-local scripts directory and run ` + "`chmod +x <script_path>`" + ` after writing each script.
- Read credentials from environment variables injected by script.run.
- Validate missing required environment variables before connecting to external services.
- Use CLI argv arguments. For Mateway calls, script.run args is an argument array, not JSON:

` + "```text" + `
script.run name=email.receive args=["--limit","10"]
` + "```" + `

- Scripts should tolerate a leading ` + "`--`" + ` in argv before script-specific flags so manual CLI checks like ` + "`mateway script run name -- --help`" + ` do not become false failures.
- Print concise machine-readable or clearly structured output.
- Do not claim external actions succeeded unless the script exits successfully and prints evidence.

## Verification policy

Separate verification into two layers:

1. Structure verification, required for skill creation:
   - chmod +x every script.
   - syntax or --help check works without credentials.
   - mateway script list discovers the expected script names.
   - script.run can execute a no-secret path such as --help.
2. Credentialed endpoint verification, optional:
   - Run only when the real secret is present.
   - Use script.run, never terminal.run.
   - If the provider rejects login or the secret is missing, report that credentialed verification is blocked while the skill structure remains installed.

## Creation workflow

1. Determine the smallest useful skill surface from the user's request.
2. Store any concrete secrets provided in the current task with secret.set.
3. Create or update SKILL.md.
4. Add skill-local scripts under scripts/ when deterministic execution is needed.
5. Add mateway.required_secret headers for each required credential.
6. Run chmod +x for every script.
7. Run python/go/node/shell syntax or --help checks.
8. Run mateway script list to confirm scripts are discovered.
9. Run script.run with a no-secret safe path such as --help.
10. If credentials are present, optionally run credentialed endpoint verification through script.run.
11. Final answer with created files, commands, structure verification evidence, and credentialed verification status, without repeating secret values.

If provider settings are stable and commonly known, encode them directly in the script with comments or references when helpful. Use web search only when the task needs current or uncertain facts; do not spend the whole turn searching before writing a small, testable script.
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

` + "`feishu.sample.yaml`" + ` and ` + "`weixin.sample.yaml`" + ` are safe to keep as user-facing templates.

Copy a sample to its runtime YAML, then enable the channel only after credentials or tokens are configured.

Recommended:

- Keep direct secret fields empty.
- Put real secrets in ` + "`../mateway.env`" + `.
- Use ` + "`*_env`" + ` fields in channel YAML files.
`
