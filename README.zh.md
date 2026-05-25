# Mateway

[English](./README.md) | [中文](./README.zh.md)

Mateway 是一个轻量级 Go runtime，用于构建可以使用工具、记住有用上下文，并同时工作在 CLI 和飞书里的个人或团队 Agent。

它适合那些希望 Agent 真正进入本地工作区和业务聊天场景，又不想从重型工作流平台开始的人。

## 为什么是 Mateway

很多 Agent demo 很容易跑起来，但很难在日常工作里信任。Mateway 选择做一个更小、更可检查的系统：

- **单二进制优先**：构建一个 `mateway` 二进制文件，就可以本地运行或作为服务运行。
- **CLI 和飞书**：终端和飞书聊天共用同一个 runtime。
- **有边界的工具调用**：文件写入、patch 和危险 shell 命令都需要确认。
- **可追踪执行过程**：plan、tool call、tool result 和 reply 都会写入 trace log。
- **感知会话上下文**：short memory 会记录近期任务、产物、待确认操作和 follow-up 上下文。
- **Markdown-first memory**：长期记忆用可 review 的 Markdown 保存，并带 evidence 和可重建索引。
- **用户定时任务**：可以用自然语言创建周期性任务，并通过同一套 runtime 执行。
- **Skill-oriented extension**：通过 skills 和未来 connector 包扩展能力，而不是把业务系统硬编码进核心。

核心 runtime loop 保持有意克制：

```text
receive -> plan -> policy -> act -> observe -> synthesize -> reply
```

## 当前状态

Mateway 仍处于早期阶段，但已经可以作为第一版单 Agent runtime 使用。

已实现：

- CLI 命令：`init`、`doctor`、`ask`、`test`、`eval`、`trace`、`memory`、`heartbeat`、`schedule`、`gateway`
- 飞书 WebSocket receive/reply/reaction
- 兼容 Anthropic 和 OpenAI API 的模型客户端
- 可配置 model 和 agent profiles
- 内置工具：time、config summary、web search、file read/write/patch、terminal run、project index、file summary、memory search/index、user ask
- path guard、危险命令 guard、输出截断和回复清洗
- 持久化 session/task state 和 follow-up 解析
- Markdown memory proposal、commit、reject、lint、index 和 search
- 用于 memory lint/review/compact/index rebuild 的 heartbeat 维护任务
- planner 选择的用户定时任务工具，支持到期检测和 runtime 执行
- 真实模型 planner routing 评测：`mateway eval routing`
- workspace skill discovery 和默认 skills

仍在演进：

- 更高质量的 memory review 和 promote 工作流
- 外部 API / CLI 的 connector 扫描
- 超出当前配置契约的多 Agent profile routing
- 可选 FTS5 或 embedding 检索增强
- 打包、发布自动化和更多生产级加固

## 发布版本

GitHub release 除了源码压缩包之外，还应该附带预编译二进制文件。

推荐的 release 产物：

- `mateway_darwin_arm64`
- `mateway_darwin_amd64`
- `mateway_linux_arm64`
- `mateway_linux_amd64`
- `mateway_windows_amd64.exe`

本地构建 release 产物：

```bash
./build-release.sh v0.1.0
```

tag 触发的自动上传流程位于 `.github/workflows/release.yml`。

## 快速开始

从源码构建：

```bash
git clone https://github.com/dragon123960-collab/mateway.git
cd mateway
go test ./...
go build -o build/mateway ./cmd/mateway
```

初始化本地 runtime 文件：

```bash
./build/mateway init
```

这会创建 `~/.mateway`，并写入配置模板、workspace 文件、memory 骨架和默认 skills。已有真实配置不会被覆盖。

配置密钥并校验：

```bash
cp ~/.mateway/config/mateway.env.sample ~/.mateway/config/mateway.env
vim ~/.mateway/config/mateway.env
vim ~/.mateway/config/config.yaml
./build/mateway doctor
```

从 CLI 提问：

```bash
./build/mateway ask "What time is it?"
./build/mateway ask "Read README.md and summarize this project."
./build/mateway ask "Run pwd, then explain the current working directory."
```

启动 gateway 进程：

```bash
./build/mateway gateway serve
```

`gateway serve` 是前台 runtime 进程。如果希望 Mateway 常驻在线，可以把它放到 LaunchAgent、systemd 或其他服务管理器里运行。

## 配置

运行时配置位于 `~/.mateway/config`。

重要文件：

```text
~/.mateway/config/
  config.yaml
  mateway.env
  models/
    minimax.yaml
    local-mlx.yaml
  channels/
    feishu.yaml
```

配置职责：

- `config.yaml`：应用路径、安全、搜索、scheduler、memory 和 agent 默认值
- `models/*.yaml`：模型 provider、endpoint、API 兼容模式和模型名
- `channels/feishu.yaml`：飞书渠道配置
- `mateway.env`：本地密钥，不要提交

支持的模型 API 模式：

- `api: anthropic`
- `api: openai`

模型选择示例：

```yaml
model:
  default: minimax
  fallbacks: []
  roles:
    planning: minimax
    repair: minimax
    synthesis: minimax
    followup: minimax
```

当前 runtime 主要使用默认模型。按角色路由属于配置契约的一部分，后续 runtime 扩展时会变得更重要。

## CLI 用法

常用命令：

```bash
mateway init
mateway doctor
mateway ask "Summarize the current repository."
mateway gateway serve
mateway gateway status
mateway trace tail
mateway trace show <trace_id>
```

Memory 命令：

```bash
mateway memory list --area inbox --status proposed
mateway memory show <id-or-path>
mateway memory commit --proposal <proposal-id>
mateway memory reject --proposal <proposal-id>
mateway memory lint
mateway memory index
```

用户定时任务命令：

```bash
mateway schedule propose --title "AI trends" --prompt "Collect recent AI trend articles with sources." --daily-at 09:00
mateway schedule proposals
mateway schedule commit-proposal <id>
mateway schedule list
mateway schedule due
mateway schedule run-due
```

自然语言创建定时任务会走普通 runtime planning loop。planner 应选择 `schedule.create` 并补齐字段；信息不足时先补问：

```text
Every day at 9:00, collect recent AI trend articles and write a short sourced report.
```

schedule CLI 仍保留 proposal/review 命令，供手动工作流使用；runtime 创建定时任务则直接通过工具执行。

## 飞书

Mateway 可以作为飞书 WebSocket 机器人运行。

飞书渠道有意保持简单：

- 接收和标准化消息
- 回复用户
- 添加轻量 reaction
- 忽略 app/self 消息
- 默认不发送嘈杂的中间进度消息

runtime 工作会在飞书回调外执行，避免较慢的模型或工具调用阻塞事件 ACK。

飞书配置位于：

```text
~/.mateway/config/channels/feishu.yaml
~/.mateway/config/mateway.env
```

然后运行：

```bash
mateway gateway serve
```

## Memory

Mateway 采用 Markdown-first memory 设计。

它分为两层：

- **Short memory**：近期 session/task state、artifacts、pending confirmations 和 follow-up context
- **Long memory**：workspace memory tree 下经过 review 的 Markdown notes

Long memory 走 proposal 流程：

```text
task with evidence -> memory proposal -> user review -> commit/reject -> searchable long memory
```

这样可以让持久化知识保持可检查、可编辑。JSON memory index 可以从 Markdown 重建，因此 Markdown 仍然是真相源。

## 定时任务与 Heartbeat

Mateway 有两个不同的后台概念：

- **Heartbeat**：系统维护任务，例如 memory lint、daily review、recent compaction 和 memory index rebuild
- **用户定时任务**：用户创建的周期性业务任务，例如每天早上收集 AI 趋势文章

用户定时任务会走和普通用户请求一样的 runtime 路径，因此 tool policy、confirmation、trace、memory 和 artifacts 都保持一致。

生命周期是：

```text
user request -> planner selects schedule tool -> task YAML -> due run -> runtime invocation -> artifact
```

## Skills 与 Connectors

Skills 是本地能力包，用来描述 Agent 在某个领域应该如何工作。

当前方向是：

```text
skill = instructions + metadata + optional scripts/assets + allowed tools
connector = scanned config that exposes API/CLI/software capability as a tool
```

Mateway 不会把业务集成硬编码进核心 runtime。未来 connector 支持应该允许团队通过配置暴露已有 API、CLI 和内部系统，并显式声明参数 schema、风险等级、evidence、鉴权要求和确认边界。

## 安全模型

Mateway 的设计目标是让工具调用显式、可观测：

- 文件工具限制在项目根目录、Mateway workspace 和配置的 accessible paths 内
- 文件写入和 patch 需要确认
- 危险 shell 命令需要确认
- 模型给出的 `confirmed=true` 不会绕过 guarded tools
- 飞书回复会清洗，避免泄漏原始 tool call JSON
- 所有 runtime 步骤都可以通过本地 event logs 追踪

这仍然是早期软件。请在敏感机器上运行前检查配置、allowed paths 和确认行为。

## 仓库结构

```text
cmd/mateway              CLI entrypoint
internal/app             application wiring
internal/channel         channel interfaces
internal/channel/feishu  Feishu adapter
internal/config          configuration loading and init templates
internal/gateway         channel orchestration and service management
internal/heartbeat       maintenance scheduler
internal/memory          Markdown memory store, lint, index, search
internal/model           model clients and planning helpers
internal/observer        trace and event inspection
internal/runtime         agent loop, task binding, session flow
internal/schedule        user scheduled task store and runner
internal/session         persisted session/task state
internal/skill           skill discovery and default skills
internal/tool            built-in tools and safety policy
```

## 开发

运行测试：

```bash
go test ./...
```

构建：

```bash
go build -o build/mateway ./cmd/mateway
```

## License

Apache License 2.0。
