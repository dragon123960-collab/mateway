# Mateway

<p align="center">
  <img src="banner.png" alt="Mateway — local-first agent runtime" width="100%" />
</p>

[English](./README.md) | [中文](./README.zh.md)

**Mateway 是一个面向真实工作区和领域应用的 local-first agent runtime kernel。它把用户任务规划成由可验收子任务组成的 TaskGraph，通过 node-local 的模型、工具、skill 执行完成任务，并在 graph evidence 满足任务或出现明确 blocker 时收口。**

它不是重型 workflow 平台。Mateway 的核心方向是 loop engineering：从一个超长全局 ReAct loop，升级为可恢复、可验收的 subtask node graph，同时保留工具边界、可编辑 skills、白盒 memory 和可审计 evidence。

```text
message -> Planner -> TaskGraph -> Scheduler
        -> node-local execution -> verifier
        -> finalizer -> memory observe
```

## 特色

- **TaskGraph planning，而不是盲聊。** Planner 一次输出 task acceptance 和可验收子任务 graph。
- **Node-local ReAct。** 复杂 node 内部可以跑受限 ReAct；简单 node 可直接模型调用；确定性工作可用 script/tool 特例。
- **可编辑 skills。** Skills 是带 `SKILL.md` 和 `.mateway/metadata.yaml` 的本地注册能力包。Planner 可以把 skill 绑定到 node，但工具调用仍是 node 内部 action/evidence。
- **白盒 memory。** 长期记忆是带 YAML frontmatter 的 Markdown，并配套 proposal 和 audit。Agent 可以建议保存长期记忆，但由用户决定是否提交。
- **Trace ledger。** 每次运行都会产生 JSONL trace，记录 model turns、tool calls、evidence、hook events、timing、token diagnostics，并对 secret 做脱敏。
- **小型本地 runtime。** CLI、飞书、微信、定时任务和测试共用同一套 runtime，而不是各自一套 agent stack。

## 当前能力

- CLI 入口：`mateway ask`、`mateway chat`、`mateway tui`
- 飞书 WebSocket gateway 和原生微信 iLink Bot channel
- 内置工具：`file.read`、`file.write`、`file.edit`、`file.delete`、`terminal.run`、`web.search`、`web.fetch`、`secret.set`、`schedule.manage`、`task.search`、`task.resume`、`toolresult.read`
- Tool policy：阻断 destructive terminal command、路径校验、secret 脱敏
- TaskGraph runtime 基础：planner、graph state、node execution、verifier、finalizer 和 recovery-oriented trace
- Context budget、压缩 tool output、通过 `raw_ref` 回读原始输出
- 多 agent profile 基础：channel bindings、agent-specific skills、agent-scoped memory，以及未来本地 agent node 角色
- Memory proposal、lint、search、index rebuild 和 learning heartbeat 命令

`terminal.run` 是唯一命令执行工具。它可以通过 `env_secrets` 注入 secret；trace 只记录 secret id 和环境变量名，不记录明文 secret。

## 快速开始

```bash
git clone https://github.com/dragon123960-collab/mateway.git
cd mateway

go test ./...
go build -o build/mateway ./cmd/mateway
./build/mateway init
./build/mateway doctor
```

然后配置本地模型和渠道：

```bash
cp ~/.mateway/config/mateway.env.sample ~/.mateway/config/mateway.env
vim ~/.mateway/config/mateway.env
vim ~/.mateway/config/config.yaml
```

试一下 CLI：

```bash
./build/mateway ask "Read README.md and summarize this project."
./build/mateway ask "Inspect the current project directory and identify the runtime entrypoint."
./build/mateway chat
```

`mateway chat` 会在支持交互终端时打开 TUI，也可以通过 `--classic` 使用传统行式 REPL。`mateway tui` 可以直接启动 TUI。

`mateway init` 会在 `~/.mateway/` 下创建本地 home：

```text
config/      models, channels, runtime settings
workspace/   agent profiles, shared skills, Markdown memory
sessions/    compacted session state
trace/       JSONL runtime traces
observe/     memory and skill learning evidence
indexes/     derived indexes
run/         runtime locks and channel state
```

## 文档

- [Architecture](./docs/architecture.md)：代码结构、包边界和 runtime kernel 设计。
- [TaskGraph Runtime](./docs/task-graph-runtime.md)：最终 TaskGraph 架构。
- [Execution Flow](./docs/execution-flow.md)：从用户消息到最终回答或 blocker 的完整链路。
- [Embedding And App Runtime](./docs/embedding-and-app-runtime.md)：应用如何把 Mateway 作为本地 agent kernel。
- [Configuration](./docs/configuration.md)：本地路径、模型、渠道、skills、memory 和安全配置。
- [Roadmap](./docs/roadmap.md)：当前路线和明确不做的方向。

开发 scratch 放在 `dev-notes/`，完成后应及时清理。

## 设计边界

Mateway 不追求变成 distributed workflow platform。当前明确不做：

- 不做 heavy workflow platform 或 distributed workflow engine。
- 不做 multi-tenant company scheduler。
- 不做 distributed multi-agent supervisor 或 subagent spawning。
- 不做 gateway 业务级 routing。
- 不新增 `terminal.run` 之外的命令执行工具。

Mateway 可以支持本地 agent node 作为执行角色；外部调度系统和公司级 orchestration 应把它作为 runtime kernel 调用，而不是放进 Mateway 内部。
