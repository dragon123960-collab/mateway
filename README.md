# Mateway

Mateway 是一个面向个人助理、多 Agent、工具编排场景的宿主运行时。

它的目标不是只做一个聊天机器人，而是提供一套可部署、可扩展、可追踪的 Agent Host：

- 通过 `mateway` 命令管理模型、通道、workspace、agent、gateway
- 通过 Eino 驱动 Agent 主循环、工具调用、Plan-Execute、多 Agent 编排
- 通过内置 tools、skills、外接 CLI provider、后续 MCP provider 完成任务
- 通过 memory / trace / learn / wiki 提供可追踪、可沉淀的执行过程

当前第一阶段的目标是：先把“最小可用闭环”做稳。

- 能编译和发布
- 能安装和启动
- 能接飞书
- 能调用模型
- 能运行工具
- 能看 `/trace` 和 `/learn`
- 能通过命令管理 gateway

## 1. 当前能力概览

当前已经具备的最小可用能力：

- Eino 驱动的 chat / tool / multi-agent 主链
- Feishu 网关接入
- 内置工具：
  - `web_search`
  - `browser_fetch`
  - `read_file`
  - `write_file`
  - `list_files`
  - `search_text`
  - `search_history`
  - `search_scoped_memory`
  - `read_memory`
  - `read_session_summary`
  - `recall_last_task`
  - `wiki_ingest`
  - `wiki_query`
  - `wiki_lint`
  - `sandbox_exec`
  - `create_workspace`
  - `create_agent`
  - `spawn`
  - `wait_agent`
- skills 目录扫描与注册
- 外接 CLI provider
- run / trace / learn / wiki proposal
- `mateway tui`
- `mateway gateway health|status|restart`

能力清单见：

- [docs/CAPABILITIES.md](/Users/dongping/project/mateway/docs/CAPABILITIES.md)

## 2. 适用平台

Mateway 不绑定某个单一操作系统。

它本质上是一个 Go 二进制程序，因此理论上可以在所有 Go 支持的平台上构建和运行，只要满足这些条件：

- 能运行目标架构的 Go 编译产物
- 能访问你配置的大模型接口
- 能访问你要接入的通道或外部工具
- 如果使用外接 CLI provider，对应二进制在目标机器可用

常见部署形态：

- 本地开发机直接运行
- 云服务器或虚拟机上常驻运行
- 容器中运行
- 配合 systemd / supervisor / launchd / 容器编排系统托管

也就是说：

- 开发阶段可以从源码编译
- 最终面向用户时，可以直接提供预编译好的二进制包

## 3. 从源码编译

### 3.1 环境要求

建议环境：

- Go 1.25 或与项目 `go.mod` 匹配的版本
- Git
- 可以访问你的模型接口和目标通道

### 3.2 本机编译

在项目根目录执行：

```bash
go build -o build/mateway ./cmd/mateway
```

产物位置：

```bash
build/mateway
```

如果你的平台需要可执行后缀或特定签名，可以在发布阶段单独处理。  
Mateway 本身不依赖某个操作系统特有的构建流程。

### 3.3 跨平台编译

Go 可以通过 `GOOS` 和 `GOARCH` 直接做交叉编译。

例如：

```bash
# Linux amd64
GOOS=linux GOARCH=amd64 go build -o dist/mateway-linux-amd64 ./cmd/mateway

# Linux arm64
GOOS=linux GOARCH=arm64 go build -o dist/mateway-linux-arm64 ./cmd/mateway

# macOS arm64
GOOS=darwin GOARCH=arm64 go build -o dist/mateway-darwin-arm64 ./cmd/mateway

# Windows amd64
GOOS=windows GOARCH=amd64 go build -o dist/mateway-windows-amd64.exe ./cmd/mateway
```

建议最终发布时统一输出到 `dist/`：

```text
dist/
├── mateway-linux-amd64
├── mateway-linux-arm64
├── mateway-darwin-arm64
└── mateway-windows-amd64.exe
```

## 4. 预编译二进制如何发布

如果后续你们对外提供预编译好的二进制，建议发布包至少包含：

- `mateway` 主程序
- 一个默认配置模板目录
- 一份最小 README / 快速开始文档

建议发布结构：

```text
mateway-release/
├── bin/
│   └── mateway
├── config.example/
│   ├── config.yaml
│   ├── models/
│   │   └── default.yaml
│   ├── channels/
│   │   └── feishu.yaml
│   └── cli_providers/
└── README.md
```

也可以打成：

- `.tar.gz`
- `.zip`
- 容器镜像

建议产物命名规范：

```text
mateway-v0.1.0-linux-amd64.tar.gz
mateway-v0.1.0-linux-arm64.tar.gz
mateway-v0.1.0-darwin-arm64.tar.gz
mateway-v0.1.0-windows-amd64.zip
```

## 5. 二进制如何安装

最终用户如果拿到的是预编译好的二进制，通常不需要 Go 环境。

### 5.1 最简单方式

把二进制放到某个目录，然后直接运行：

```bash
./mateway version
```

### 5.2 放入 PATH

为了让用户能直接执行 `mateway`，应该把二进制放入 PATH 中的某个目录。

常见做法：

- Linux / macOS：
  - `/usr/local/bin`
  - `~/.local/bin`
- Windows：
  - 某个已经加入 PATH 的目录

推荐做法是由发布脚本、包管理器、安装器，或者用户自己把二进制复制/软链接到 PATH 目录。


### 5.3 验证安装

```bash
mateway version
mateway help
```

## 6. 初始化运行目录

第一次使用执行：

```bash
mateway init
```

它会创建默认目录：

```text
~/.mateway/
├── config/
│   ├── config.yaml
│   ├── models/
│   ├── channels/
│   └── cli_providers/
└── workspace/
    ├── agents/
    └── memory/
```

如果你希望使用自定义目录，也可以在部署时自行准备配置目录和 workspace。

## 7. 配置文件结构

Mateway 当前默认把运行配置放在：

```bash
~/.mateway/config/
```

结构如下：

```text
~/.mateway/config/
├── config.yaml
├── models/
│   └── default.yaml
├── channels/
│   └── feishu.yaml
└── cli_providers/
```

说明：

- `config.yaml`
  - 宿主级默认配置
- `models/*.yaml`
  - 模型分片配置
- `channels/*.yaml`
  - 通道配置
- `cli_providers/*.yaml`
  - 外接 CLI provider 配置

## 8. 配置模型

### 8.1 主配置

主配置文件位置：

```bash
~/.mateway/config/config.yaml
```

一个最小示例：

```yaml
app:
  name: mateway
  home: "/home/yourname/.mateway"
  workspace: "/home/yourname/.mateway/workspace"

gateway:
  host: "127.0.0.1"
  port: 8787

security:
  enforce_workspace_paths: false
  require_approval_for_risky_tools: false

integrations:
  web_search:
    enabled: true
    provider: "tavily"
    duckduckgo:
      base_url: "https://api.duckduckgo.com/"
    tavily:
      base_url: "https://api.tavily.com/search"
      api_key: "tvly-xxx"
  browser:
    enabled: true

models:
  default: "aliyun-qwen"
  fallbacks:
    - qwen3.5-plus
    - glm-5
    - kimi-k2.5
  temperature: 0.2
  system_prompt: "You are Mateway, a concise, capable assistant."
  max_tokens: 8192
  request_timeout_seconds: 120
  limits:
    requests_per_minute: 12
    cooldown_on_429_seconds: 60
    transient_retry_max: 1

sessions:
  history_limit: 12
```

### 8.2 模型分片配置

模型配置放在：

```bash
~/.mateway/config/models/
```

例如：

```yaml
name: aliyun-qwen
provider: openai_compat
model: qwen3.6-plus
api_base: https://coding.dashscope.aliyuncs.com/v1
api_key: sk-xxx
enabled: true
```

常用命令：

```bash
mateway model current
mateway model list
mateway model set-default aliyun-qwen
```

### 8.3 模型回退链

当前支持 fallback model chain。

例如：

```yaml
models:
  default: aliyun-qwen
  fallbacks:
    - qwen3.5-plus
    - glm-5
    - kimi-k2.5
```

当主模型触发：

- provider quota exhausted
- provider rate limiting
- transient upstream error

系统会按顺序切到下一个可用模型。

## 9. 配置飞书

飞书配置文件位置：

```bash
~/.mateway/config/channels/feishu.yaml
```

示例：

```yaml
feishu:
  enabled: true
  app_id: "cli_xxx"
  app_secret: "xxx"
  verification_token: "xxx"
  ack_text_enabled: true
  ack_reaction_enabled: true
  allow_from:
    - "ou_xxx"
  group_trigger:
    mention_only: true
  base_url: "https://open.feishu.cn"
  bot_name: "Mateway"
```

常用命令：

```bash
mateway channel list
mateway channel enable feishu
mateway channel disable feishu
```

## 10. 启动方式

Mateway 不强制某一种托管方式。

你可以根据自己的环境选择：

- 前台运行
- systemd
- supervisor
- launchd
- Docker / 容器编排
- 任何你熟悉的进程守护方案

### 10.1 前台运行

```bash
mateway gateway
```

### 10.2 健康检查

```bash
mateway gateway health
mateway gateway status
```

### 10.3 重启

```bash
mateway gateway restart
```

注意：

- `mateway gateway restart` 当前优先适配本机托管场景
- 真正线上部署时，建议由你的进程管理器统一负责重启策略

## 11. 推荐部署方式

### 11.1 方式 A：直接部署二进制

适合：

- 单机
- 测试环境
- 小规模服务

步骤：

1. 准备目录

```text
/opt/mateway/
├── bin/
│   └── mateway
├── config/
└── workspace/
```

2. 把二进制放到：

```text
/opt/mateway/bin/mateway
```

3. 准备配置目录

4. 启动：

```bash
/opt/mateway/bin/mateway gateway
```

5. 用 systemd / supervisor / 其他守护进程工具托管

### 11.2 方式 B：容器部署

适合：

- 云环境
- 统一镜像分发
- CI/CD 自动部署

基本思路：

1. 构建二进制
2. 复制到镜像
3. 把配置和 workspace 作为卷挂载
4. 容器入口执行：

```bash
mateway gateway
```

### 11.3 方式 C：开发模式本地运行

适合：

- 本地调试
- 联调飞书
- 调试 tool / skill / trace / learn

方式：

```bash
mateway tui
mateway gateway
```

## 12. 本地终端交互

```bash
mateway tui
```

`mateway tui` 现在不再是状态面板，而是本地交互式终端会话入口。

它的定位更接近：

- 不通过飞书
- 不通过其他 channel
- 直接在终端里和 Mateway 对话
- 同时还能使用本地 slash commands

当前建议使用这些命令：

- `/help`
- `/exit`
- `/skills`
- `/runs`
- `/trace [run_id]`
- `/learn [run_id]`
- `/agent <name>`
- `/run <tool_name> [json]`

## 13. Skills、MCP、CLI 与 API

### 13.1 Skills

skills 通过目录扫描发现，默认扫描：

- `~/.mateway/skills`
- `~/.mateway/workspace/skills`

Mateway 现在的方向是：

- `SKILL.md` 是 skill 的正式主入口
- 我们优先兼容现有的通用 skill 生态，而不是再发明一套新的主标准
- 我们自己新增 skill，也优先用 `SKILL.md`

对于“可执行 skill”，当前采用：

- `SKILL.md`
- 可选 `_meta.json`

其中：

- `SKILL.md`
  - 负责技能名称、描述、使用说明、工作流说明
- `_meta.json`
  - 负责可执行绑定
  - 例如把一个 skill 绑定成 CLI skill 或 API skill

也就是说：

- `SKILL.md` 是主格式
- `_meta.json` 是可选兼容层，用来承载 CLI / API 运行时信息
- `skill.yaml` 不再作为主标准

如果某个目录只有 `SKILL.md`，当前 runtime 仍会把它加载成 skill，但会标记为“说明型 skill”，不会错误地当成坏掉的 runnable skill。

### 13.2 可执行 skill 绑定

由于 CLI / API 这两类目前没有统一的主流 skill 运行时标准，Mateway 当前采用：

- 通用 `SKILL.md`
- 可选 `_meta.json`

来承载可执行绑定。

例如 CLI skill：

```text
my-skill/
├── SKILL.md
├── _meta.json
└── run.sh
```

例如 API skill：

```text
my-api-skill/
├── SKILL.md
└── _meta.json
```

你可以直接通过命令生成模板：

```bash
mateway skill create cli my-cli-skill
mateway skill create api my-api-skill
```

这样用户不需要从零手写标准文件。

### 13.3 MCP

MCP 不应由 Mateway 自己发明协议，而应遵循官方规范。

参考：

- [Model Context Protocol Specification](https://modelcontextprotocol.io/specification/draft)

Mateway 对 MCP 的方向是：

- tools
- resources
- prompts

全部优先对齐官方协议，而不是自定义变体。

### 13.4 外接 CLI provider

CLI provider 配置目录：

```bash
~/.mateway/config/cli_providers/
```

例如：

```yaml
name: opencli
binary: /path/to/opencli
description: External web and app CLI adapter
list_args:
  - list
allowed_commands:
  - web
  - hackernews
  - reddit
  - arxiv
blocked_commands:
  - install
  - register
  - plugin
risk_level: medium
```

注意：

- `opencli` 不是系统内置概念
- 它只是一个外接 CLI provider 示例
- Obsidian、gh、其他 CLI 工具也应该通过同样方式接入

## 14. 飞书里当前能怎么用

飞书侧当前第一阶段可用行为包括：

- 收到消息先尝试加 reaction
- 先发占位消息：`👀 已收到，处理中...`
- 再发最终结果
- 支持普通对话
- 支持：
  - `/skills`
  - `/tools`
  - `/run <tool-or-skill>`
  - `/approvals`
  - `/approve <id>`
  - `/deny <id>`
  - `/runs`
  - `/run_status <id>`
  - `/trace [run-id]`
  - `/learn [run-id]`
  - `/summary`
  - `/last`
  - `/memory <session|thread|task|agent> <scope> [query]`

其中：

- `/skills` 侧重列出 `skills/` 目录中的技能
- `/tools` 侧重列出当前 session / agent 可见的能力面
- `/trace` 侧重执行过程
- `/learn` 侧重开发态学习摘要

## 15. 常用命令

```bash
mateway init
mateway tui
mateway gateway
mateway gateway health
mateway gateway status
mateway gateway restart
mateway logs show
mateway logs follow
mateway logs path
mateway doctor
mateway model current
mateway model list
mateway model set-default <name>
mateway channel list
mateway channel enable feishu
mateway channel disable feishu
mateway workspace create <name>
mateway workspace list
mateway skill create <cli|api> <name>
mateway agent create <workspace-path> <name>
mateway agent list <workspace-path>
mateway schedule create <name> <interval-minutes> <prompt>
mateway schedule create interval <name> <interval-minutes> <prompt>
mateway schedule create cron <name> "<expr>" <tz> <prompt>
mateway schedule list
mateway schedule get <name>
mateway schedule enable <name>
mateway schedule disable <name>
mateway schedule run <name>
mateway schedule runs <name>
mateway run list [session-key]
mateway run get <run-id>
mateway version
```

说明：

- `schedule create <name> <interval-minutes> <prompt>` 是兼容写法，等价于 interval 任务
- `schedule create cron ...` 支持真正的 cron 表达式与时区，例如：
- schedule 变更默认会走审批；`schedule_create` 同名创建会按幂等 upsert 处理而不是重复新增
- `schedule_create` 的 target 语义已明确：
  - 有 active session/agent 时，默认指向 `current session + current agent`
  - 没有 active session 时，默认回退到 `isolated session + default agent`
  - 也可以显式指定 `target_session_mode` / `target_agent_mode`

```bash
mateway schedule create cron ai-course-resource-daily "0 3 * * *" Asia/Shanghai "请执行 AI 课程资源搜集任务..."
```

## 16. 安全与执行策略

### 16.1 路径限制

`security.enforce_workspace_paths` 默认是 `false`。

这表示：

- 默认不强制把普通读写限制在 workspace 内
- 但明显危险的破坏性命令仍会被拒绝

### 16.2 风险工具审批

`security.require_approval_for_risky_tools` 默认是 `false`。

如果打开：

- 风险工具不会立刻执行
- 会进入 pending approval
- 然后通过飞书 `/approve <id>` 或 `/deny <id>` 处理

### 16.3 执行能力

目前推荐优先使用：

- `sandbox_exec`

而不是直接依赖无限制 shell。

## 17. 排错

### 17.1 `command not found: mateway`

先确认二进制是否已经安装到 PATH：

```bash
mateway version
```

如果还不行，请手动把二进制复制或软链接到 PATH 中的目录，或者通过你自己的安装脚本处理。

### 17.2 gateway 起不来

先看：

```bash
mateway gateway status
mateway gateway health
```

如果你是自己托管进程，请优先检查：

- 配置路径
- 模型连通性
- 通道配置
- 进程管理器日志

### 17.3 模型 429 / quota exceeded

先检查：

```bash
mateway model current
```

再检查 `config.yaml` 是否配置了 `models.fallbacks`。

### 17.4 工具调用失败

去飞书里看：

- `/trace`
- `/learn`

如果是 CLI provider 的 allowlist 拦截，通常会在 run error 里直接看到 `provider policy deny`。

## 18. 当前架构方向

当前运行时方向是：

- Eino 负责：
  - Agent runtime loop
  - interrupt / resume
  - supervisor
  - plan-execute
  - callback
  - summarization / tool reduction / tool search
- Mateway 负责：
  - channel
  - config
  - workspace
  - provider registry
  - approval UX
  - persistent memory
  - run store
  - learn / trace / wiki

更多设计说明见：

- [docs/EINO_ADOPTION.md](/Users/dongping/project/mateway/docs/EINO_ADOPTION.md)
- [docs/CAPABILITIES.md](/Users/dongping/project/mateway/docs/CAPABILITIES.md)

## 19. 当前阶段说明

现在这一版更适合理解成：

- 第一阶段最小可用版

已经能用于：

- 本地开发和联调
- 飞书接入测试
- 多工具 / skills / CLI provider 实验
- run / trace / learn 观察
- memory / wiki 沉淀
- tool_search + progressive disclosure 的大工具面运行
- tool output offload / summarization 的长上下文缓解

还会继续增强的方向包括：

- 更强的 TUI
- 更稳的 gateway 托管
- 更细的 failure taxonomy / token 与成本观测
- 更完整的多 Agent 协作
- 更厚的长期记忆与学习沉淀
