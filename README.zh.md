<p align="center">
  <img src="mateway-banner.svg" alt="Mateway — local agent runtime for real workspaces" width="100%" />
</p>

# Mateway

**Mateway 是一个面向真实工作区的本地优先 Agent Runtime。**

它不是一个重型工作流平台，也不是一个只能演示的 Agent Demo。Mateway 的目标是把大模型变成一个可以长期在本地项目、团队 IM、文件系统、工具链和任务上下文里工作的“可信执行体”。

> **一句话：Mateway = 单 Agent 主循环 + Hook 化扩展 + 工具安全边界 + 白盒记忆 + Trace 可观察性。**

```text
receive -> context_hook -> model/tool loop -> tool_policy_hook
        -> observe_hook -> response_hook -> reply
```

## 为什么需要 Mateway？

很多 Agent 项目第一次 demo 很惊艳，但真正放进日常工作后会遇到几个问题：

- **能力很强，但不可控**：工具调用、文件写入、终端命令缺少清晰边界。
- **记忆很方便，但不可信**：长期记忆不知道从哪里来，也不好修改、回滚和审查。
- **前端很多，但运行时分裂**：CLI、IM、定时任务、脚本入口各做各的，行为不一致。
- **框架很大，但落地很慢**：还没解决一个真实任务，就先搭了一个复杂平台。
- **Agent 会做事，但不好复盘**：任务为什么这样做、用了什么工具、耗时在哪里，很难追踪。

Mateway 的思路相反：

> 先把一个 Agent 的运行闭环做干净、做透明、做可扩展，再让能力通过 hooks、skills、tools、memory 自然长出来。

## 核心特色

### 1. Pi-style，但更适合本地工作区

Mateway 借鉴 Pi Agent 的运行时思路：不是把所有能力硬编码进一个巨大的 agent，而是围绕一个稳定主循环，提供清晰的扩展点。

Mateway 当前坚持：

- 单 Agent 主线优先
- 不急着做 supervisor / sub-agent routing
- 不把 memory 强行塞进主循环
- 不让 connector 体系过早膨胀
- 让 tools、skills、hooks、trace 形成统一运行协议

这让它更适合个人和小团队先落地，再逐步扩展。

---

### 2. Hook-first Runtime

Mateway 的新版核心是 hook 化运行时。

```text
receive
  -> context_hook
  -> model/tool loop
  -> tool_policy_hook
  -> observe_hook
  -> response_hook
  -> reply
```

每个 hook 都有明确职责：

| Hook | 作用 |
|---|---|
| `context_hook` | 注入 workspace profile、用户/工具摘要、轻量 memory refs、实时性策略 |
| `tool_policy_hook` | 统一处理工具风险、路径策略、危险命令、确认边界 |
| `observe_hook` | 写入 trace、task step、acceptance evidence、后续 memory proposal 候选 |
| `response_hook` | 清理最终回复，适配 CLI / Feishu 等不同 channel |
| `followup_hook` | 处理“继续”“重试”“天津呢”这类上下文续接 |

Hook 的价值是：**能力增强不污染主循环，安全策略不散落在各处，未来扩展不会把 runtime 改成一团。**

---

### 3. 一个 Runtime，多种入口

Mateway 不是只给 CLI 用，也不是只给聊天机器人用。

同一个 agent runtime 可以接入：

- CLI：`mateway ask`
- 真实任务测试：`mateway test`
- Trace 回看：`mateway trace`
- Feishu WebSocket Gateway
- 后续的 schedule / heartbeat / workspace UI

这意味着：**你在终端里验证过的能力，未来可以原样进入飞书、定时任务和自动化场景。**

---

### 4. 工具使用有边界

Mateway 允许 Agent 使用真实工具，但不会把控制权完全交给模型。

当前内置方向包括：

- `file.read`
- `file.write`
- `project.index`
- `terminal.run`
- `web.search`
- `web.fetch`
- 后续接入 `memory.search`
- 后续接入 `schedule.*`

安全原则：

- 文件读写受 workspace path policy 约束
- 写文件、patch、危险 shell 命令需要确认
- 模型自己声称 `confirmed=true` 不会被信任
- 大文件、二进制文件、危险命令会被 guard
- Feishu 最终回复会被 sanitizer 清理，避免泄露工具协议块

Mateway 的目标不是让 Agent “什么都能干”，而是让它能在可理解、可确认、可复盘的边界内干活。

---

### 5. 白盒记忆，不做黑箱记忆

Mateway 的长期记忆不是一坨不可见的 embedding，也不是自动写入的聊天摘要。

新版记忆设计坚持：

```text
task/trace/source evidence
  -> memory proposal
  -> user review
  -> commit / reject
  -> searchable Markdown memory
```

记忆目录以 Markdown 为 source-of-truth：

```text
~/.mateway/workspace/
  agents/
    main/
      memory.md                 # prompt-facing 短摘要
  memory/
    user/
      long/
      inbox/
    org/
      long/
      inbox/
    agents/
      main/
        memory.md               # 长期记忆入口
        long/
        inbox/
        diary/
```

核心原则：

- 自动生成内容先进入 `inbox/` 或 `diary/`
- 不自动提交长期记忆
- 事实必须带 source evidence
- `index.json`、SQLite、embedding 都只能是可重建索引
- Markdown 永远是可读、可改、可审计的事实源

---

### 6. Trace-first 可观察性

Mateway 把 Agent 执行过程当成一等公民。

每次任务可以被记录为 JSONL trace，包括：

- request
- model event
- tool call
- tool result
- pending confirmation
- reply
- runtime timing

你可以用：

```bash
mateway trace <trace-jsonl-path>
```

快速看出：

- 模型耗时
- 工具耗时
- runtime 耗时
- Feishu 回复耗时
- 哪一步失败或可疑

这对调试 Agent 比“看最终回答”重要得多。

---

## 当前能做什么？

Mateway 目前已经形成第一版真实闭环：

- CLI 任务入口
- Feishu WebSocket gateway
- LaunchAgent gateway serve/start/restart/stop/status
- 单实例锁
- task tree
- follow-up 续接
- pending confirmation
- trace JSONL
- 基础 file/project/terminal/web tools
- model fallback
- workspace profile / tools / skills 摘要注入
- skills discovery 第一版
- 真实任务测试入口 `mateway test`

适合现在就测试的任务：

```bash
mateway ask "读取 README.md，总结这个项目的架构。"

mateway ask "查看当前项目目录结构，说明 runtime 入口在哪里。"

mateway test --message "请搜索今天最新 AI 资讯，给我 5 条高价值摘要。"

mateway test --message "请 review internal/tool/builtin.go，指出风险和缺测试点。"
```

---

## 当前不伪装已经完成的能力

Mateway 仍在快速演进中。下面这些方向已经设计清楚，但不应该在 README 里吹成已完整完成：

- 完整 memory write/proposal/commit/reject 工作流
- `memory.search` safe-read tool
- heartbeat 自动维护
- 用户显式 schedule runner
- mail / SSH / GitHub / 自媒体发布等 connector framework
- OS-level sandbox
- 可视化 workspace UI
- 多 agent supervisor / sub-agent routing

当前策略是：

> 先把单 Agent Runtime 做稳定，再把 memory、schedule、connector、visualization 接到 hook 和 trace 体系里。

---

## 与其他 Agent 框架的区别

| 方向 | 常见 Agent 框架 | Mateway |
|---|---|---|
| 核心目标 | 快速编排复杂 Agent 流程 | 让一个 Agent 在真实工作区可靠运行 |
| 架构倾向 | 多 Agent / workflow / graph | 单 Agent 主循环 + hooks |
| 记忆 | 自动摘要或黑箱向量库 | Markdown-first、proposal-based、可审查 |
| 工具 | 能调就行 | 工具契约 + 风险边界 + confirmation |
| 观察性 | 最终结果为主 | trace / task step / evidence 一等公民 |
| 前端 | 通常分裂 | CLI、Feishu、测试、未来 schedule 共享 runtime |
| 扩展 | 插件或平台化 | skills + tools + hooks + external scripts |

---

## Quick Start

### Build

```bash
git clone https://github.com/dragon123960-collab/mateway.git
cd mateway

go test ./...
go build -o build/mateway ./cmd/mateway
```

### Init

```bash
./build/mateway init
```

初始化后会创建：

```text
~/.mateway/
  config/
  workspace/
    agents/
    skills/
    memory/
  sessions/
  trace/
  run/
```

### Configure

```bash
cp ~/.mateway/config/mateway.env.sample ~/.mateway/config/mateway.env
vim ~/.mateway/config/mateway.env
vim ~/.mateway/config/config.yaml
```

检查配置：

```bash
./build/mateway doctor
```

### Ask from CLI

```bash
./build/mateway ask "What time is it?"
./build/mateway ask "Read README.md and summarize this project."
./build/mateway ask "Run pwd, then explain the current working directory."
```

### Run real runtime tests

```bash
./build/mateway test --case read-readme
./build/mateway test --case project-index
./build/mateway test --case web-search
```

自定义真实任务：

```bash
./build/mateway test --session-key demo:a001 --message "请查看当前项目结构，并总结 runtime 入口。"
```

查看 trace：

```bash
./build/mateway trace <trace-jsonl-path>
```

### Start Gateway

```bash
./build/mateway gateway serve
```

`gateway serve` 是前台运行进程。你可以用 launchd、systemd 或其他 service manager 托管它。

---

## HOME 目录结构

Mateway 默认使用：

```text
~/.mateway/
```

核心目录：

```text
~/.mateway/
  config/        # 配置、模型、channel、env sample
  workspace/     # agent profile、skills、memory
  sessions/      # transcript、task tree、pending 状态
  trace/         # 每次任务的 JSONL trace
  run/           # gateway lock 等运行态文件
```

Workspace 关键结构：

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
    org/
    agents/main/
```

---

## Skills

Mateway 的 skills 当前是“可编辑的行为指导”，不是默认可执行工具。

发现顺序：

```text
workspace/agents/<agent_id>/skills/*/SKILL.md
workspace/skills/*/SKILL.md
```

同名 skill 下，agent-specific 优先。

Skill 可以描述：

- 这个领域应该怎么做
- 什么时候使用哪些工具
- 产出应该如何验收
- 哪些风险需要提醒用户
- 后续是否可以沉淀为脚本或正式 tool

后续方向：

```text
successful task -> trace evidence -> skill candidate -> user review -> promoted skill
```

---

## Roadmap

### M1 — Hook skeleton

- `context_hook`
- `tool_policy_hook`
- `observe_hook`
- `response_hook`
- `followup_hook`
- trace 中可见 hook 影响

### M2 — Memory safe-read

- Markdown frontmatter
- source evidence
- index rebuild
- lint
- keyword search
- `memory.search` tool

### M3 — Memory proposal

- `memory.propose`
- `memory.commit`
- `memory.reject`
- inbox / diary / long memory 分层
- 不自动提交长期记忆

### M4 — Heartbeat / schedule

- memory index rebuild
- memory lint
- 用户显式定时任务
- 默认关闭后台维护任务

### M5 — Visualization

- trace timeline
- task tree
- memory ledger
- skill shelf
- workspace health
- 先静态 HTML / Markdown 报告，再考虑长期 Web UI

### M6 — Safer terminal runtime

- macOS sandbox-exec
- Linux bubblewrap
- 可选 terminal sandbox wrapper
- network/filesystem 限制策略

---

## 设计原则

### Keep the core small

主循环应该小而清晰。复杂能力通过 hook 和 tool contract 接入，而不是把 runtime 变成巨大的不可维护平台。

### Make memory reviewable

长期记忆必须能看到来源、能编辑、能拒绝、能重建索引。

### Trust comes from traces

Agent 的可信度不是来自“回答很像真的”，而是来自它能展示自己做了什么、为什么这样做、证据在哪里。

### Local-first does not mean local-only

Mateway 优先围绕本地 workspace 和用户机器构建，但可以通过 Feishu、web search、外部 CLI、脚本和未来 connector 接入更大的工作系统。

### Don’t fake connectors

没有 mail、SSH、GitHub 或发布平台 connector 时，Mateway 应该明确说明缺口，并提出接入方案，而不是编造执行结果。

---

## License

Apache License 2.0.
