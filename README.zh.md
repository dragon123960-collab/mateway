# Mateway

<p align="center">
  <img src="banner.png" alt="Mateway — local-first agent runtime" width="100%" />
</p>

[English](./README.md) | [中文](./README.zh.md)

**Mateway 是一个面向真实工作区的 local-first Go agent runtime。它会先把用户任务变成轻量 contract，再通过小型 transcript-driven 工具循环执行，最后只有在证据满足或出现明确 blocker 时才收口。**

它不是重型 workflow 平台。Mateway 的核心方向是 loop engineering：保持 AgentCore loop 足够小，把可靠性交给 task contract、工具边界、可编辑 skills、白盒 memory 和可审计 evidence。

```text
message -> task contract/checklist -> selected skill preflight
        -> AgentCore ReAct loop -> tool evidence
        -> completion evaluator -> final answer or blocker
```

## 特色

- **Task contract，而不是盲聊。** 动作型任务会带 required tools、required evidence、selected skills 和 plan items。runtime 用这份 checklist 判断任务是否完成。
- **Transcript-driven execution。** Mateway 不机械 replay PlanExecute 图或 DAG。模型仍通过正常 ReAct loop 行动，hooks 和 evaluator 负责约束完成质量。
- **可编辑 skills。** Skills 是 workspace 中的 `SKILL.md` 文件。Planning 可以选择相关 skill，但 skill name 永远不是 tool name；真实执行仍使用 `file.read`、`file.write`、`terminal.run`、`web.search`、`web.fetch` 等工具。
- **白盒 memory。** 长期记忆是带 YAML frontmatter 的 Markdown，并配套 proposal 和 audit。Agent 可以建议保存长期记忆，但由用户决定是否提交。
- **Trace ledger。** 每次运行都会产生 JSONL trace，记录 model turns、tool calls、evidence、hook events、timing、token diagnostics，并对 secret 做脱敏。
- **小型本地 runtime。** CLI、飞书、微信、定时任务和测试共用同一套 runtime，而不是各自一套 agent stack。

## 当前能力

- CLI 入口：`mateway ask`
- 飞书 WebSocket gateway 和原生微信 iLink Bot channel
- 内置工具：`file.read`、`file.write`、`file.edit`、`file.delete`、`terminal.run`、`web.search`、`web.fetch`、`secret.set`、`schedule.manage`、`task.search`、`task.resume`、`toolresult.read`
- Tool policy：阻断 destructive terminal command、路径校验、secret 脱敏
- 复杂或高风险任务的 plan review
- Context budget、压缩 tool output、通过 `raw_ref` 回读原始输出
- 多 agent profile 基础：channel bindings、agent-specific skills、agent-scoped memory
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
```

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

- [Architecture](./docs/architecture.md)：代码结构、包边界和小型 runtime 设计。
- [Configuration](./docs/configuration.md)：本地路径、模型、渠道、skills、memory 和安全配置。
- [Execution Flow](./docs/execution-flow.md)：从用户消息到最终回答或 blocker 的完整链路。
- [Roadmap](./docs/roadmap.md)：当前路线和明确不做的方向。

开发 scratch 放在 `dev-notes/`，完成后应及时清理。

## 设计边界

Mateway 不追求变成 workflow platform。当前明确不做：

- 不做 PlanExecute framework。
- 不做 DAG runtime。
- 不做 multi-agent supervisor 或 subagent spawning。
- 不做 gateway 业务级 routing。
- 不新增 `terminal.run` 之外的命令执行工具。

未来方向是继续做深 loop engineering：更好的 planning contract、更干净的执行上下文、更强的 evidence evaluator、更安全的 terminal isolation，以及更有用的 skill/memory crystallization。
