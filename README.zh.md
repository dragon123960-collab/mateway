# Mateway
<p align="center">
  <img src="banner.png" alt="Mateway — memory-native local agent runtime" width="100%" />
</p>

[English](./README.md) | [中文](./README.zh.md)

**Mateway 是一个面向真实工作区的本地优先 Agent Runtime，围绕白盒记忆、自我学习和可审计工具使用构建。**

它不是重型工作流平台，也不是只能演示的聊天机器人。Mateway 是一个围绕小型 AgentCore 主循环构建的 Go 运行时，同时已经预留多 agent profile 和 binding 基础，用于不同工作身份、渠道、skills 和记忆作用域。

> **一句话：Mateway = 小型 AgentCore + 多 Agent Profile + Hook Runtime + 工具边界 + 类 Git 记忆 + 自学习 Proposal + Trace Ledger + 实时 Office Watch。**

```text
receive -> followup_hook -> context_hook -> model/tool loop
        -> tool_policy_hook -> observe_hook -> response_hook -> reply
```

## 为什么需要 Mateway？

大多数 Agent 框架都能做出惊艳的第一版 demo。真正困难的是：当一个 Agent 和你一起工作几周之后，它还能不能可信。

- 它应该记住有价值的经验，但不能悄悄改写自己的长期认知。
- 它应该从完成的任务中学习，但在提交长期记忆之前仍然让用户确认。
- 它应该解释自己用了哪些工具、看到了什么证据、结果从哪里来。
- 它应该在 CLI、飞书、测试和未来的定时任务中使用同一套运行时。
- 它应该诚实说明 connector 缺口，而不是假装已经发了邮件或登录了服务器。

Mateway 选择保守路径：先保持 AgentCore 主循环足够小，再通过 profiles、hooks、skills、tools 和 Markdown memory 自然增长能力。

## 独特之处

### 1. 类 Git 记忆机制

Mateway 把记忆当成一个可 review 的工作区，而不是黑箱向量库。

```text
task / trace / tool evidence
  -> diary
  -> proposal
  -> save or ignore
  -> Markdown long-term memory
  -> rebuildable index
  -> safe-read injection
```

Agent 在有价值的任务完成后可以提出长期记忆候选。用户可以回复 `保存` 提交，也可以回复 `忽略` 拒绝。底层流程接近一个轻量 Git 工作流：

| 记忆步骤 | 类 Git 含义 |
|---|---|
| diary | 工作笔记 |
| proposal | 暂存候选 |
| save / commit | 持久长期记忆 |
| reject | 丢弃候选 |
| audit log | 提交历史 |
| index rebuild | 派生索引，不是事实源 |

长期记忆以带 YAML frontmatter 的 Markdown 存在 `~/.mateway/workspace/memory/` 下，可以用 Obsidian 打开、手工编辑、lint、search、rebuild 和 audit。

### 2. 自我学习，但不静默篡改

任务完成后，`observe_hook` 会记录 task steps，并可能生成 memory proposal。候选会出现在最终回答里，让人做决定：

```text
保存到长期记忆:
mateway memory proposal commit <proposal_id>

忽略这条候选:
mateway memory proposal reject <proposal_id>
```

在聊天入口里，用户也可以直接回复 `保存` 或 `忽略`。Mateway 会把它存成 `memory_proposal_review` pending，所以这种短回复由运行时状态解释，而不是让模型猜。

英文别名同样可用：memory review 可以回复 `save` / `ignore`，工具确认可以回复 `confirm` / `cancel`，定时任务试运行可以回复 `run` / `cancel`。这些别名不依赖当前界面语言。

### 3. Hook-first Runtime

核心循环保持小而清晰，扩展点显式存在：

| Hook | 作用 |
|---|---|
| `followup_hook` | 把“继续”“重试”“天津呢？”绑定到正确任务，或要求澄清 |
| `context_hook` | 注入 runtime context、workspace profile、已发现 skills 和相关 memory snippets |
| `tool_policy_hook` | 执行工具风险、确认边界和危险命令检查 |
| `observe_hook` | 记录已接受工具步骤、任务证据、diary 和 memory proposals |
| `response_hook` | 清理最终回复，并附加 memory review 提示 |

### 4. 带 Secret 脱敏的 Trace Ledger

每次运行都会写入 JSONL trace：

- request 和 channel
- model turns
- provider 返回时的模型请求数和 token usage
- tool calls 和 tool results
- hook events
- pending confirmations
- final reply
- runtime timings

持久化 trace、session transcript 和 task step summary 会脱敏明显的 secret 字段，例如 `api_key`、`token`、`password`、`smtp_pass`、`imap_pass` 和 bearer token。模型在当前任务中仍能看到实时工具输出；持久化日志避免保存明显凭证。

### 5. 实时 Runtime 可视化

`gateway serve` 可以在同一个进程里同时运行本地 Web Console、Office Watch、飞书、微信、定时任务和未来 channel。Runtime 写 trace 的同时会通过进程内 WebSocket event bus 发布事件，所以从飞书发布的任务，只要由同一个 gateway 进程执行，也可以在浏览器里实时看到进度。

Office Watch 会展示任务发布、上下文组装、模型轮次、工具执行、usage 增量、回复、等待状态和完成状态，也支持从 JSONL trace 做历史回放。标记为 `est` 的 context 数字是基于字符数的估算；只有模型 provider 返回的 usage 才是真实 token。

### 6. 有边界的 Session Context

Session 是运行时状态，不是无限增长的原始聊天记录。每次调用模型前，Mateway 会从这些来源临时组装 context：

- `context_hook` 每轮重新生成的 system/runtime context
- 当前 agent profile 的 Markdown 文件
- 已发现 skill 的精简 guidance
- `memory_safe_read` 命中时的相关长期记忆片段
- 压缩后的最近 session transcript
- 当前用户消息

System context 每轮重新生成，不会写回 session transcript。持久化 session 消息会被压缩：system 消息会丢弃，大型 tool result 会截断，只保留最近的对话消息。Task node 会保存短摘要、trace id 和工具步骤证据，所以旧工作仍可审计，但不会把完整历史 transcript 强行塞进下一次 prompt。

飞书图片消息会下载到 `~/.mateway/media`；微信 channel 如果收到带 URL 或本地路径的媒体 item，也会归一化到同一套 message part。Session transcript 只保存媒体引用，不内联图片字节。模型详情、启用开关、`context_window` 和 `max_tokens` 放在 `~/.mateway/config/models/*.yaml`；`config.yaml` 只选择默认模型、fallback 和角色。如果当前模型声明了 `modalities: [text, image]`，用户文字和图片会作为同一个 user turn 一起发送给多模态模型；否则 Mateway 会优先使用 `model.roles.vision`，它可以是单个模型，也可以是 `vision: [glm-4.6v-flash, minimax]` 这样的有序列表，然后再尝试其他支持图片的 fallback 模型。音频、视频和文件 part 已在消息结构中预留，后续再接渠道下载和模型发送。

发送 `/new`、`/新会话` 或 `新会话` 会归档当前 session，并在同一个 `session_key` 下清空 active state。飞书长 thread 仍然保持稳定 session key，但 agent 可以从干净上下文重新开始。

### 7. Skills 是可编辑行为，不是魔法工具

Mateway 会发现本地 `SKILL.md` 文件，并把精简 guidance 注入 runtime context。当前默认 skills 包括：

- `software-install`
- `fresh-search`
- `source-evaluation`
- `connector-gap`
- `skillcreate`

Skills 是行为指导，本身不是可执行能力。如果任务需要真实动作，Agent 仍必须调用真实工具或脚本，并给出证据。

## 当前能做什么

Mateway 目前支持：

- CLI 任务入口：`mateway ask`
- 飞书 WebSocket gateway
- 原生微信 iLink Bot channel：`mateway weixin login`、`mateway weixin enable`
- 从本地 channel 配置文件发现 channel id：`mateway channel list`
- 真实 runtime 测试：`mateway test`
- trace 回看：`mateway trace`
- session 查看和归档命令：`mateway session list`、`mateway session show`、`mateway session archive list/show`
- task tree 和 follow-up 绑定
- 风险工具 pending confirmation
- 安全内置工具：`file.read`、`file.write`、`project.index`、`terminal.run`、`web.search`、`web.fetch`
- Anthropic-compatible 和 OpenAI Chat-compatible 模型优先使用原生 tool/function calling，不支持时才退回文本协议
- 同一轮 safe-read 工具批次可并行执行，由 `execution.max_parallel_tools` 控制
- 本地 secret store：`mateway secret set/get/list/delete`
- trace 中可见 hook events
- workspace profile 注入
- 从 `workspace/skills` 和 agent-specific overrides 发现 skills
- Markdown memory lint/search/index
- memory proposal create/list/show/commit/reject
- 有价值任务完成后的自动 diary/proposal 生成
- 可按 channel、间隔和展示数量配置的候选记忆提醒
- memory proposal 的聊天回复处理：`保存` / `忽略`
- 通过 `context_hook` 做 memory safe-read 注入
- session 和 project distill 命令
- 手动 memory heartbeat：lint + index rebuild
- 持久化 runtime 记录中的 secret 脱敏
- 多 agent profile 基础：`config.agents.profiles[]`、channel bindings、agent-specific skills 和 agent-scoped memory directories

## 快速开始

### 构建

```bash
git clone https://github.com/dragon123960-collab/mateway.git
cd mateway

go test ./...
go build -o build/mateway ./cmd/mateway
```

### 初始化

```bash
./build/mateway init
```

会创建：

```text
~/.mateway/
  config/
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

### 配置

```bash
cp ~/.mateway/config/mateway.env.sample ~/.mateway/config/mateway.env
vim ~/.mateway/config/mateway.env
vim ~/.mateway/config/config.yaml
```

校验配置：

```bash
./build/mateway doctor
```

### 从 CLI 提问

```bash
./build/mateway ask "Read README.md and summarize this project."
./build/mateway ask "Inspect the current project directory and identify the runtime entrypoint."
./build/mateway ask "Search today's latest AI news and summarize 5 high-value items."
```

### 运行真实 Runtime 测试

```bash
./build/mateway test --case read-readme
./build/mateway test --case project-index
./build/mateway test --case web-search
```

自定义任务：

```bash
./build/mateway test --session-key demo:a001 --message "Read README.md and explain the memory system."
```

### 启动 Gateway

```bash
./build/mateway gateway serve
```

`gateway serve` 会以前台进程启动已启用的内置 channel 和本地 Web Console。如果要常驻后台，可以用 launchd、systemd 或其他服务管理器托管。

Web Console 默认启用，地址为：

```text
http://127.0.0.1:8765
```

它提供一个本地工作台，用于对话、skills、定时任务、sessions、channels、agents、配置和 usage 查看。可以在 `config.yaml` 的 `web` 块里配置：

```yaml
web:
  enabled: true
  bind: 127.0.0.1:8765
  open_browser: false
  allow_config_write: true
  realtime_enabled: true
  office_watch_enabled: true
  office_watch_assets: ""
```

控制台还提供可选的 Office Watch 页面：`http://127.0.0.1:8765/watch`。它通过本地 WebSocket 事件展示任务从发布、上下文组装、模型轮次、工具执行、usage 增量、回复到完成的动态过程。Office Watch 使用 Mateway 自制占位像素样式，不打包 Star-Office-UI 的非商用素材。标记为 `est` 的上下文数字是基于字符数的估算；模型 provider 返回的 usage 才是真实 token。

查看当前可配置的 channel id：

```bash
./build/mateway channel list
```

示例：

```text
ID      ENABLED  CONFIG
feishu  true     ~/.mateway/config/channels/feishu.yaml
weixin  true     ~/.mateway/config/channels/weixin.yaml
```

配置候选记忆提醒等 channel-scoped 行为时，使用 `ID` 列里的名字。

### 接入微信

Mateway 的原生微信 channel 参考 Hermes 的 iLink Bot API 路线，支持扫码登录、保存账号、长轮询接收文本消息和发送文本回复。

```bash
./build/mateway weixin login
./build/mateway weixin enable
./build/mateway gateway restart
```

登录凭据保存在 `~/.mateway/run/weixin/accounts/`。`weixin enable` 会更新 `~/.mateway/config/channels/weixin.yaml`，但不会把 token 写进配置文件。媒体/CDN 支持暂不纳入当前版本。

## Memory 命令

Lint memory：

```bash
./build/mateway memory lint
```

重建索引：

```bash
./build/mateway memory index rebuild
```

搜索记忆：

```bash
./build/mateway memory search "README experience"
```

Review proposals：

```bash
./build/mateway memory proposal list
./build/mateway memory proposal show <proposal_id>
./build/mateway memory proposal commit <proposal_id>
./build/mateway memory proposal reject <proposal_id> --reason "not reusable"
```

候选记忆提醒可以在 `~/.mateway/config/config.yaml` 配置：

```yaml
execution:
  max_parallel_tools: 4

memory:
  proposal_nudge:
    enabled: true
    interval: 24h
    channels:
      - cli
    max_proposals: 3
```

提醒由 runtime 生成，但只会出现在配置的 channel id 中。提醒会展示少量候选摘要，并给出 `mateway memory proposal show <proposal_id>` 查看详情，不会把所有待审核候选一次性塞进聊天窗口。

蒸馏 session 或 project：

```bash
./build/mateway memory distill session <session_key>
./build/mateway memory distill project close <project_id>
```

运行手动 heartbeat 维护：

```bash
./build/mateway memory heartbeat lint-index
./build/mateway memory heartbeat learning
./build/mateway memory heartbeat skill
```

以前台循环运行 heartbeat 维护：

```bash
./build/mateway memory heartbeat serve
```

这个 heartbeat 命令会 lint Markdown memory，在安全时重建 `indexes/memory_index.json`，蒸馏学习 evidence，生成 skill patch proposal，并写入 audit entry。

## 定时任务

定时任务是 channel-neutral 的。Mateway 负责保存任务、可选试跑、到点执行，并把运行记录写到 `~/.mateway/schedules/runs/`。它不会自动把结果发回飞书、邮件、Slack 或其他渠道。

创建任务。默认会先进入 pending，试跑成功后才激活：

```bash
./build/mateway schedule create --run-at 2026-05-29T18:00:00+08:00 "检查未读邮件并总结重要事项"
./build/mateway schedule test <task_id>
./build/mateway schedule list
```

执行到期任务一次，或以前台 runner 持续运行：

```bash
./build/mateway schedule run-due
./build/mateway schedule serve
```

如果定时任务需要通知某人，请把通知写进任务本身，通过已有 tool、本地脚本、connector 或 skill 完成。没有可用投递渠道时，agent 应说明缺口，并询问是否需要创建相关脚本或 skill。

## 模型工具调用

Mateway 现在优先使用大模型原生 tool/function calling，不再把手写工具 JSON 当作主路径。Anthropic-compatible 模型，例如 MiniMax M3，会使用 Anthropic `tools` / `tool_use`；OpenAI Chat-compatible 模型，例如 GLM Flash，会使用 Chat Completions `tools` / `tool_calls`。旧的 `[TOOL_CALL]` 文本协议只保留为兜底：当某个模型 API 不支持原生工具调用，或原生请求失败时才使用。

所有入口共用同一个 runtime loop，所以 CLI、飞书、微信、Web Console 和定时任务，只要配置的模型支持原生工具调用，都会走同一条原生工具路径。OpenAI Responses 风格的 `api: openai` 目前保持保守处理，在对应 provider 或代理的原生 Responses tools 验证完成前继续使用 fallback 路径。

## Trace 命令

```bash
./build/mateway trace <trace-jsonl-path>
```

Trace 可用于检查：

- model/tool/runtime latency
- 模型请求数和 input/output/total tokens
- hook decisions
- tool calls 和 acceptance evidence
- pending confirmations
- memory proposal generation
- Feishu gateway timing

## HOME 目录结构

```text
~/.mateway/
  config/        # config.yaml, env files, model/channel config
  workspace/     # agent profiles, skills, Markdown memory
  sessions/      # transcripts, task trees, pending states
    archive/     # /new 创建的旧 session 归档
  trace/         # JSONL traces
  observe/       # diary, proposals, reflections, audit logs
  indexes/       # rebuildable memory indexes
  run/           # runtime files such as gateway locks
```

Workspace：

```text
~/.mateway/workspace/
  agents/
    main/
      agent.md
      soul.md
      user.md
      tools.md
      memory.md
      skills/
  skills/
    software-install/
    fresh-search/
    source-evaluation/
    connector-gap/
    skillcreate/
  memory/
    user/
      long/
    org/
      long/
    agents/
      main/
        memory.md
        experiences/
```

## Skills

Mateway skills 是可编辑行为指导，存放在：

```text
workspace/agents/<agent_id>/skills/<skill_name>/SKILL.md
workspace/skills/<skill_name>/SKILL.md
```

发现顺序：

1. agent-specific skills
2. shared workspace skills

同名时 agent-specific skills 优先。

Skills 不能保存明文凭证。凭证应进入本地 secret store，skill frontmatter 里只引用 secret id：

```yaml
required_secrets:
  - id: mail.smtp_pass
    env: SMTP_PASS
```

`mateway skill install` 和写入 `SKILL.md` 的 `file.write` 会拒绝 password、API key、token、bearer token 等疑似明文 secret。

当前行为：

- Runtime 发现本地 skills，并把短 guidance 注入 context。
- 低频 skills 可以自动冷却：active skills 注入完整 guidance，cold skills 只注入一行召回卡片，hidden skills 不进入 context，直到手动恢复。
- 默认初始化的 shared skills 覆盖 fresh search、source evaluation、connector gaps、software installation workflow 和 Mateway skill creation rules。
- Agent 可以检查已有 skills、安装本地/raw skills，并在用户审核后 promote skill patch proposal。

Skill cleanup 通过 `skills.cleanup` 配置：

```yaml
skills:
  cleanup:
    enabled: true
    cold_after_days: 30
    hidden_after_days: 90
    max_usage_count: 1
    protected: []
    restore_mode: permanent
```

Cold skills 仍会用 `name`、`description`、`aliases` 和 `when_to_use` 生成极短召回卡片。Hidden skills 不会被删除；可以用 `mateway skill cleanup list --state hidden` 查看，并用 `mateway skill cleanup restore <id>` 恢复。

已可用：

- `mateway skill catalog report`
- `mateway skill search <query>`
- `mateway skill install <name-or-url>`
- `mateway skill cleanup report|list|restore`
- `mateway skill proposal list|show|promote|reject`
- `mateway skill usage report`
- 外部 skill catalog 集成。规划中的首批来源：`skills.sh`、`skillhub.cn`、`clawhub.ai`
- heartbeat 生成 skill patch proposal 的审核工作流

Script Bridge 保持小而硬：`workspace/agents/<agent_id>/skills/<skill>/scripts/`、`workspace/skills/<skill>/scripts/`、`workspace/scripts/`、`~/.mateway/scripts/` 或配置的 `scripts.dirs` 下的可执行脚本可以通过 `mateway script list` 查看，并通过 `script.run` / `mateway script run` 执行。同名脚本冲突时，agent-specific skill scripts 优先于 shared skill scripts，shared skill scripts 优先于 global scripts。脚本头可以声明 `mateway.required_secret`，凭证来自 `mateway secret`，不写入 `SKILL.md`、trace 或 memory。

## 多 Agent Profiles

Mateway 目前还没有 multi-agent supervisor、subagent spawn 或 DAG router。但它已经具备多个 agent profile 的底座：

- `config.agents.default`
- `config.agents.profiles[]`
- `config.agents.bindings[]`
- `workspace/agents/<agent_id>/`
- `workspace/agents/<agent_id>/skills/`
- `workspace/memory/agents/<agent_id>/`

每个 agent profile 使用同一组 core prompt 文件：`agent.md`、`soul.md`、`user.md`、`tools.md`、`memory.md`。`mateway agent create` 新建 profile 时会生成英文基线模板，且不会覆盖已有文件。

这意味着不同 channel 或 session namespace 可以选择不同 agent 身份、prompt 文件、skill overrides 和 memory scope，同时仍然共享同一个小型 AgentCore runtime。

边界也很明确：profiles 和 bindings 在当前范围内；自主多 agent 编排不属于当前版本。

Profile 产品化命令：

- `mateway agent list`
- `mateway agent report <agent_id>`
- `mateway agent lint <agent_id>`
- `mateway agent create <agent_id> [--name <name>] [--default]`
- `mateway agent bind --channel <channel> [--account-id <id>] [--peer-id <id>] <agent_id>`
- `mateway agent unbind --channel <channel> [--account-id <id>] [--peer-id <id>]`

## Gateway 边界

Gateway 是 channel 汇聚层和本地控制台宿主：负责 session key、dedupe、异步 runtime 执行、reply 分发和 Web Console。`gateway serve` 会从 `channels/` 启动已启用的内置 channel，包括飞书 WebSocket、原生微信长轮询，以及 `web.enabled` 为 true 时的本地 Web Console。

新的稳定 channel 应优先做成内置 channel spec，这样一个 gateway 进程就能统一管理。channel package 负责平台 I/O 和消息归一化，gateway 负责 session key、dedupe、异步 runtime 执行和 trace。

原生微信 channel 参考 Hermes 的 iLink Bot API 路线：`mateway weixin login` 扫码并把凭据保存到 `~/.mateway/run/weixin/accounts/`；`gateway serve` 之后通过 `getupdates` 长轮询收消息，并通过 `sendmessage` 发送文本回复。媒体/CDN 支持暂不纳入首版。

使用 `mateway channel list` 可以从 `~/.mateway/config/channels/*.yaml` 查看 canonical channel id。runtime 配置应使用这些 id，例如 `feishu` 或 `weixin`，而不是 `lark`、`wechat` 这类别名。

`gateway serve` 使用和 CLI 命令相同的 config loader，因此会读取 `~/.mateway/config/mateway.env`。如果进程环境变量里已经有同名变量，则进程环境变量优先。

## 当前边界

Mateway 不会把这些能力伪装成已经完成：

- 还没有 multi-agent supervisor 或 DAG router
- 还没有 OS-level sandbox wrapper
- 还没有通用 mail/SSH/GitHub connector framework
- 还没有外部 skill marketplace installer

当前可用版本聚焦于：稳定小核心 runtime、多 agent profile 基础、hook pipeline、带风险边界的真实工具、飞书/CLI/Web 入口、traceability、white-box memory 和实时运行可观察性。


## Roadmap

### 足以给开发者试用的部分

- 小型 AgentCore runtime loop
- 多 agent profile 和 binding 基础
- CLI / test / Feishu / Weixin entrypoints
- Hook pipeline
- Tool policy 和 confirmation boundaries
- JSONL traces
- Skill discovery 和 context injection
- 本地 secret store 和 skill secret 扫描
- Markdown memory lint/search/index
- Memory proposal list/show/commit/reject workflow
- 可配置的候选记忆提醒
- Self-learning diary/proposal generation
- Memory safe-read context injection
- Session/project distill commands
- Heartbeat `lint-index` 和前台 heartbeat runner
- Channel-neutral scheduled task create/test/run-due/serve
- 通过 `mateway channel list` 发现内置 channel
- 持久化 runtime 记录 secret redaction
- Web Console：对话、skills、定时任务、sessions、配置、记忆和 channel 开关
- Office Watch：基于 WebSocket 的实时执行 timeline，并支持 trace 回放

### 下一步

- 更多内置 channel：钉钉、QQ、企业微信、Telegram
- channel 音频/视频/文件等媒体能力
- user-provided connectors 的 script bridge specification
- skill source adapters 和 promote workflow
- safer terminal sandbox wrappers
- 更完整的 trace/task/memory workspace UI
- mail、SSH、GitHub 和 publishing connector packages

## 设计原则

### 保持核心小而清晰

主循环保持小而清晰。复杂能力通过 hooks、tool contracts、skills 和 scripts 接入。

### 记忆必须可 review

长期记忆应该显示来源，支持编辑/拒绝，并允许重建索引。

### 信任来自 traces

Agent 的可信度来自知道发生了什么、使用了哪些证据、执行了哪些边界。

### Local-first 不等于 local-only

Mateway 优先服务本地工作区和用户机器，但也通过飞书、网页搜索、外部 CLI、脚本和未来 connectors 连接更大的系统。

### 不伪造 connector

当 mail、SSH、GitHub 或 publishing connector 缺失时，Mateway 应该报告缺口，检查安全的本地选项，并提出集成路径，而不是编造执行结果。

## License

Apache License 2.0.
