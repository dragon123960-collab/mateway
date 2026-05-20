# Mateway

这是 Mateway 的干净版 Go 重写，目标是做一个小而有用、能跑在 CLI 和飞书上的工具型 Agent。

当前已经成立的最小闭环是：

`receive -> MiniMax JSON plan -> policy -> tool execution -> evidence -> MiniMax final reply -> channel response`

当前内置工具包括：

- `time.now`
- `config.summary`
- `web.search`
- `file.read`
- `file.write`
- `file.patch`
- `project.index`
- `file.summary`
- `shell.run`
- `user.ask`

## 配置

Mateway 从 `MATEWAY_HOME` 读取运行时配置，默认目录是 `~/.mateway`。

预期文件：

- `~/.mateway/config/config.yaml`
- `~/.mateway/config/models/minimax.yaml`
- `~/.mateway/config/channels/feishu.yaml`
- `~/.mateway/config/mateway.env`

密钥保留在 `~/.mateway` 或环境变量里，不进入仓库。

## 命令

```bash
go run ./cmd/mateway init
go run ./cmd/mateway doctor
go run ./cmd/mateway ask "现在几点？"
go run ./cmd/mateway ask "运行 pwd，然后读取 README.md 总结"
go run ./cmd/mateway gateway serve
go run ./cmd/mateway gateway status
go run ./cmd/mateway gateway restart
go run ./cmd/mateway trace tail
go run ./cmd/mateway trace show <trace_id>
scripts/restart-plist.sh --env-file ~/.mateway/config/mateway.env
```

## 设计边界

Hermes Agent 只作为架构参考；本仓库不依赖，也不复制 Hermes 的 Python 实现。

`feishu` 只是兼容快捷入口，因为当前第一个 channel 是飞书。真实的前台服务进程是 `gateway serve`；`gateway start/restart/stop/status` 只是操作系统服务适配层。

## Channel 与 Gateway 边界

channel 包只负责接收、归一化、发送和 reaction，不直接调用 runtime，也不拥有业务流程。

gateway 负责编排：

- 构造 channel 维度的 session key，例如 `feishu:<thread_id>`
- 调用 runtime
- 决定 reaction 状态
- 发送回复

gateway 服务管理命令操作的是当前操作系统已经注册的服务：

- macOS：LaunchAgent label `com.dongping.mateway.gateway`
- Linux：user systemd unit `mateway-gateway.service`
- 其他 OS：暂未实现，直接把 `mateway gateway serve` 交给系统服务管理器运行

`gateway serve` 会在 `~/.mateway/run/mateway.lock` 上持有本地进程锁。这才是真正的单实例保护：即使用户从另一个 plist、登录项或终端启动了第二个 Mateway，第二个服务进程也应该快速失败，而不是重复处理飞书消息。

## 重启服务

如果现有 macOS LaunchAgent 已经指向 `build/mateway gateway serve`，可以这样编译并重启：

```bash
scripts/restart-plist.sh --env-file ~/.mateway/config/mateway.env
```

脚本会先加载 env 文件，编译 `build/mateway`，然后执行：

```bash
launchctl kickstart -k gui/$(id -u)/com.dongping.mateway.gateway
```

它不会生成、安装或修改 plist 文件。

## 飞书回复

飞书事件会先快速 ACK，再异步处理。这样可以避免模型还在 planning 或工具还在运行时，飞书把同一事件重复投递进来。

对每条用户消息，Mateway 会在内存里对 inbound `message_id` 做短窗口去重，但默认只回复一次最终结果，或者一次待确认/待补问结果；不会再默认流式发送 plan/tool 过程消息。

## 日志与 Trace

LaunchAgent 的 stdout/stderr 写到：

- `~/.mateway/logs/mateway-gateway.out.log`
- `~/.mateway/logs/mateway-gateway.err.log`

结构化 runtime 事件会按 JSONL 追加到：

- `~/.mateway/trace/events-YYYY-MM-DD.jsonl`

测试时可以直接用 trace CLI：

```bash
mateway trace tail
mateway trace tail --no-follow -n 40
mateway trace show <trace_id>
mateway trace show <trace_id> --raw
```

常用事件包括 `runtime.receive`、`runtime.task_binding_started`、`runtime.followup_resolved`、`runtime.task_activated`、`runtime.plan`、`runtime.tool_start`、`runtime.tool_done`、`runtime.plan_repair`、`runtime.control`、`runtime.reply`、`runtime.failed`。

session/task 状态保存在：

- `~/.mateway/run/sessions`

常用 session 事件包括 `runtime.session_loaded`、`runtime.followup_resolved`、`runtime.task_pending_input`、`runtime.task_pending_approval`、`runtime.task_continuation_created`、`runtime.session_saved`。

现在 runtime 在 planning 前会先做 task binding：

- 加载 session 状态
- 把当前消息绑定到 active/open/historical task，或者新建 task
- 如果绑定不明确，先澄清
- 然后再进入 planning / tool execution / synthesis

## 文件默认根

当用户没有显式提供路径根时，文件工具会把相对路径解析到 `~/.mateway` 下。同时也允许当前项目目录，方便本地开发时有意识地读取或编辑项目文件。

## Skills

Mateway 首次初始化 `~/.mateway` 时，会把默认 skills 释放到：

- `~/.mateway/workspace/skills`
- `~/.mateway/workspace/agents/main/skills`

runtime discovery 优先使用 workspace 里的副本，因此用户可以直接修改默认 skill，而不需要重新编译二进制。

## Agent Context

Mateway 还会把核心 agent 文档释放到：

- `~/.mateway/workspace/agents/main/soul.md`
- `~/.mateway/workspace/agents/main/agent.md`
- `~/.mateway/workspace/agents/main/user.md`
- `~/.mateway/workspace/agents/main/memory.md`
- `~/.mateway/workspace/agents/main/tools.md`
- `~/.mateway/workspace/agents/main/heartbeat.md`

在每次 `planning`、`planning_repair`、`synthesis` 模型调用前，runtime 会注入：

- 当前日期
- 用户时区
- 当前用户请求
- 环境摘要：
  - OS / arch
  - shell
  - HOME / workspace / project root
  - available common commands
- 核心 agent 文件：
  - `soul.md`
  - `agent.md`
  - `user.md`
  - `memory.md`
  - `tools.md`
- 选中的 skills
- 当前可用工具

`heartbeat.md` 不会进入普通任务 prompt，它只为未来的主动任务或定时流程保留。
