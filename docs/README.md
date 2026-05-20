# 文档说明

## 当前设计

Mateway 目前是一个小型工具型 Agent，而不是重型框架化 runtime。

核心包：

- `internal/config`：加载 `~/.mateway` 配置和 `.env`
- `internal/model`：MiniMax Anthropic-compatible 客户端
- `internal/tool`：工具注册、基础工具、策略保护、输出截断
- `internal/runtime`：任务绑定、JSON planning loop、一次 repair、证据收集、总结回复
- `internal/session`：session/task 状态保存、active task、continuation
- `internal/skill`：workspace skill discovery 和默认 skill 释放
- `internal/channel/feishu`：飞书 WebSocket、回复、reaction
- `internal/gateway`：channel 编排、session key、runtime 调用
- `internal/observer`：结构化日志和 trace 读取

## Workspace 初始化

运行 `mateway init` 会创建 `~/.mateway`，并释放配置、sample、说明文档和默认 skills。

配置文件会生成到：

- `~/.mateway/config/config.yaml`
- `~/.mateway/config/config.sample.yaml`
- `~/.mateway/config/mateway.env.sample`
- `~/.mateway/config/models/minimax.yaml`
- `~/.mateway/config/models/minimax.sample.yaml`
- `~/.mateway/config/models/local-mlx.yaml`
- `~/.mateway/config/models/local-mlx.sample.yaml`
- `~/.mateway/config/channels/feishu.yaml`
- `~/.mateway/config/channels/feishu.sample.yaml`

这些模板随二进制内置，不依赖用户下载源码仓库。`mateway init` 不覆盖已有真实配置。

默认 skills 释放到：

- `~/.mateway/workspace/skills`
- `~/.mateway/workspace/agents/main/skills`

同时也会释放核心 agent 文档到 `~/.mateway/workspace/agents/main`：

- `soul.md`
- `agent.md`
- `user.md`
- `memory.md`
- `tools.md`
- `heartbeat.md`

## 主链

当前主链已经变成：

1. 接收 CLI 或飞书消息
2. 加载 session 状态
3. 进入 task binding：
   - 绑定当前活动任务
   - 或切回其他 open task
   - 或命中历史 continuation
   - 或创建新任务
   - 或在不明确时直接澄清
4. 请求 MiniMax 返回有边界的 JSON plan
5. 校验每个 step 是否在 tool registry 内
6. 对危险命令和写入操作先拦截确认
7. 顺序执行允许的工具
8. 截断大输出并保留 evidence
9. 请求 MiniMax 生成最终中文回答
10. 回复 channel 并保存 session/task 状态

## Prompt Context

在每次 `planning`、`planning_repair`、`synthesis` 模型调用前，runtime 会注入：

- 当前日期
- 用户时区
- 当前用户请求
- 环境摘要：
  - 操作系统 / 架构
  - shell
  - HOME / workspace / project root
  - 常用命令可用性摘要
- agent 文件：
  - `soul.md`
  - `agent.md`
  - `user.md`
  - `memory.md`
  - `tools.md`
- 选中的 skills
- 当前可用工具

`heartbeat.md` 不进入普通任务 prompt，只保留给未来的主动 heartbeat 流程。

## 边界规则

channel 只是传输适配层。gateway 负责编排，并且必须先分配 channel-scoped session key，再调用 runtime。

gateway 对外提供：

- `gateway serve`：真实前台服务进程
- `gateway start/restart/stop/status`：当前 OS 的服务管理适配层

当前 OS 目标：

- macOS：LaunchAgent label `com.dongping.mateway.gateway`
- Linux：user systemd unit `mateway-gateway.service`

`gateway serve` 在 `~/.mateway/run/mateway.lock` 上持有单实例锁。服务管理命令只管理已注册服务，真正避免重复实例的是进程锁本身。

## 飞书回复

飞书 WebSocket handler 会先快速返回，再在后台 worker goroutine 里执行实际 runtime 任务，这样慢模型或慢工具不会触发重复投递。

对一条 inbound message，gateway 会：

1. 拒绝处理中或近期完成窗口内的重复 `message_id`
2. 在后台执行 runtime
3. 默认只回复一次最终结果，或者一次待确认/待补问结果
4. 忽略 `sender_type=app` 的飞书消息，防止自回环

## Trace CLI

- `mateway trace tail`：跟随今天的 `~/.mateway/trace/events-YYYY-MM-DD.jsonl`
- `mateway trace tail --no-follow -n 40`：打印最近 40 条格式化事件后退出
- `mateway trace show <trace_id>`：查看一次请求在所有 trace 文件里的完整事件
- 加 `--raw` 可输出原始 JSONL

当前 session/task 状态保存在 `~/.mateway/run/sessions`。

常见 trace 事件包括：

- `runtime.session_loaded`
- `runtime.task_binding_started`
- `runtime.followup_resolved`
- `runtime.task_activated`
- `runtime.task_pending_input`
- `runtime.task_pending_approval`
- `runtime.task_continuation_created`
- `runtime.session_saved`
