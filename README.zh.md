# Mateway
<p align="center">
  <img src="banner.png" alt="Mateway — memory-native local agent runtime" width="100%" />
</p>

[English](./README.md) | [中文](./README.zh.md)

**Mateway 是一个面向真实工作区的本地优先 Agent Runtime，围绕白盒记忆、自我学习和可审计工具使用构建。**

它不是重型工作流平台，也不是只能演示的聊天机器人。Mateway 是一个围绕小型 AgentCore 主循环构建的 Go 运行时，同时已经预留多 agent profile 和 binding 基础，用于不同工作身份、渠道、skills 和记忆作用域。

> **一句话：Mateway = 小型 AgentCore + 多 Agent Profile + Hook Runtime + 工具边界 + 类 Git 记忆 + 自学习 Proposal + Trace Ledger。**

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
- tool calls 和 tool results
- hook events
- pending confirmations
- final reply
- runtime timings

持久化 trace、session transcript 和 task step summary 会脱敏明显的 secret 字段，例如 `api_key`、`token`、`password`、`smtp_pass`、`imap_pass` 和 bearer token。模型在当前任务中仍能看到实时工具输出；持久化日志避免保存明显凭证。

### 5. Skills 是可编辑行为，不是魔法工具

Mateway 会发现本地 `SKILL.md` 文件，并把精简 guidance 注入 runtime context。当前默认 skills 包括：

- `software-install`
- `fresh-search`
- `source-evaluation`
- `connector-gap`

Skills 是行为指导，本身不是可执行能力。如果任务需要真实动作，Agent 仍必须调用真实工具或脚本，并给出证据。

## 当前能做什么

Mateway 目前支持：

- CLI 任务入口：`mateway ask`
- 飞书 WebSocket gateway
- 真实 runtime 测试：`mateway test`
- trace 回看：`mateway trace`
- task tree 和 follow-up 绑定
- 风险工具 pending confirmation
- 安全内置工具：`file.read`、`file.write`、`project.index`、`terminal.run`、`web.search`、`web.fetch`
- trace 中可见 hook events
- workspace profile 注入
- 从 `workspace/skills` 和 agent-specific overrides 发现 skills
- Markdown memory lint/search/index
- memory proposal create/list/commit/reject
- 有价值任务完成后的自动 diary/proposal 生成
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

### 启动飞书 Gateway

```bash
./build/mateway gateway serve
```

`gateway serve` 以前台进程运行。如果要常驻后台，可以用 launchd、systemd 或其他服务管理器托管。

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
./build/mateway memory proposal commit <proposal_id>
./build/mateway memory proposal reject <proposal_id> --reason "not reusable"
```

蒸馏 session 或 project：

```bash
./build/mateway memory distill session <session_key>
./build/mateway memory distill project close <project_id>
```

运行手动 heartbeat 维护：

```bash
./build/mateway memory heartbeat lint-index
```

以前台循环运行 heartbeat 维护：

```bash
./build/mateway memory heartbeat serve
```

这个 heartbeat 命令会 lint Markdown memory，在安全时重建 `indexes/memory_index.json`，并写入 audit entry。

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

## Trace 命令

```bash
./build/mateway trace <trace-jsonl-path>
```

Trace 可用于检查：

- model/tool/runtime latency
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

当前行为：

- Runtime 发现本地 skills，并把短 guidance 注入 context。
- 默认初始化的 shared skills 覆盖 fresh search、source evaluation、connector gaps 和 software installation workflow。
- Agent 可以检查已有 skills，并把它们作为行为指导。

尚未实现：

- `mateway skill search <query>`
- `mateway skill install <name-or-url>`
- 外部 skill catalog 集成。规划中的首批来源：`skills.sh`、`skillhub.cn`、`clawhub.ai`
- 自动 skill patch / promotion 工作流

## 多 Agent Profiles

Mateway 目前还没有 multi-agent supervisor、subagent spawn 或 DAG router。但它已经具备多个 agent profile 的底座：

- `config.agents.default`
- `config.agents.profiles[]`
- `config.agents.bindings[]`
- `workspace/agents/<agent_id>/`
- `workspace/agents/<agent_id>/skills/`
- `workspace/memory/agents/<agent_id>/`

这意味着不同 channel 或 session namespace 可以选择不同 agent 身份、prompt 文件、skill overrides 和 memory scope，同时仍然共享同一个小型 AgentCore runtime。下一阶段会把这部分产品化，补齐 agent list/report/create/bind 命令、profile lint 和多 profile 验收测试。

边界也很明确：profiles 和 bindings 在当前范围内；自主多 agent 编排不属于当前版本。

## 当前边界

Mateway 不会把这些能力伪装成已经完成：

- 还没有 multi-agent supervisor 或 DAG router
- 还没有 OS-level sandbox wrapper
- 还没有通用 mail/SSH/GitHub connector framework
- 还没有可视化 workspace UI
- 还没有外部 skill marketplace installer

当前可用版本聚焦于：稳定小核心 runtime、多 agent profile 基础、hook pipeline、带风险边界的真实工具、飞书/CLI 入口、traceability 和 white-box memory。

## Banner 图片提示词

README banner (`mateway-banner.svg`) 应表达：

```text
A wide 1600x520 technical product banner for "Mateway", a local-first AI agent runtime.
Visual metaphor: an illuminated memory ledger / commit graph / agent runtime pipeline.
Show a compact AgentCore connected to five labeled flows: Profiles, Hooks, Tools, Memory, Traces.
Memory should feel like a Git-like commit ledger: proposals, commits, audit trail, Markdown pages.
Style: precise, developer-tool, dark background, crisp vector geometry, subtle cyan/amber/green accents.
Avoid cute robots, generic chat bubbles, clouds, and abstract blobs.
Text to include: "Mateway" and "Memory-native local agent runtime".
```

## Roadmap

### 足以给开发者试用的部分

- 小型 AgentCore runtime loop
- 多 agent profile 和 binding 基础
- CLI / test / Feishu entrypoints
- Hook pipeline
- Tool policy 和 confirmation boundaries
- JSONL traces
- Skill discovery 和 context injection
- Markdown memory lint/search/index
- Memory proposal commit/reject workflow
- Self-learning diary/proposal generation
- Memory safe-read context injection
- Session/project distill commands
- Heartbeat `lint-index` 和前台 heartbeat runner
- Channel-neutral scheduled task create/test/run-due/serve
- 持久化 runtime 记录 secret redaction

### 下一步

- 多 agent profile 产品化：agent list/report/create/bind、profile lint 和多 profile 测试
- user-provided connectors 的 script bridge specification
- skill source adapters 和 promote workflow
- safer terminal sandbox wrappers
- 只读 trace/task/memory workspace UI
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
