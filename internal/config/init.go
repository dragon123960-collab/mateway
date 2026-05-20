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

agents:
  default: main
  profiles:
    - id: main
      name: 主助理
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
  providers:
    tavily:
      enabled: false
      api_key_env: TAVILY_API_KEY
      timeout_seconds: 20
      max_results: 5
      search_depth: basic
      topic: general
    duckduckgo:
      enabled: true
      timeout_seconds: 10
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

const configReadmeTemplate = `# Mateway 配置说明

运行时默认结构：

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

说明：

- ` + "`config.yaml`" + ` 定义 app、security、search、全局 model 默认值和 agent profiles。
- ` + "`models/*.yaml`" + ` 只声明模型端点、API 类型、模型名、密钥来源。
- ` + "`channels/feishu.yaml`" + ` 定义飞书接入配置，默认关闭。
- ` + "`mateway.env`" + ` 用于放本机密钥，不应提交。
- ` + "`*.sample.yaml`" + ` 是用户参考模板，当前 loader 不会读取。
- 顶层 ` + "`model`" + ` 是全局默认模板；具体 agent 的 ` + "`agents.profiles[].model`" + ` 会覆盖它。
- ` + "`roles.planning/repair/synthesis/followup`" + ` 是运行阶段模型预留配置，当前先由默认模型主导。
- ` + "`security.enforce_workspace_paths: true`" + ` 表示文件工具限制在 projectRoot、workspace 和 accessible_paths 下。

新用户可复制 sample 文件：

` + "```bash" + `
cp config.sample.yaml config.yaml
cp mateway.env.sample mateway.env
cp models/minimax.sample.yaml models/minimax.yaml
cp models/local-mlx.sample.yaml models/local-mlx.yaml
cp channels/feishu.sample.yaml channels/feishu.yaml
` + "```" + `
`

const channelsReadmeTemplate = `# Channel Configs

` + "`feishu.sample.yaml`" + ` is safe to keep as a user-facing template.

Copy it to ` + "`feishu.yaml`" + `, then enable Feishu only after app_id/app_secret are configured.

Recommended:

- Keep direct secret fields empty.
- Put real secrets in ` + "`../mateway.env`" + `.
- Use ` + "`*_env`" + ` fields in ` + "`feishu.yaml`" + `.
`
