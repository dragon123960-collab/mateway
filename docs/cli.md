# Mateway CLI 开发计划

Mateway CLI 的定位不是“另一个聊天入口”，而是本地控制中心、开发调试器和 Unix 管道接入点。微信和飞书适合作为远程入口，CLI 负责把 agent 的执行过程、session 状态、trace 和本地环境能力清楚地展示出来。

## 目标体验

CLI 需要接近 Claude Code / Codex CLI 这类工具的体验：

- 直接运行交互式对话。
- 支持实验性全屏 TUI。
- 支持 slash command。
- 实时显示 agent 过程事件，而不是只输出最终回复。
- 可以切换 session。
- 可以从飞书、微信等其他 channel 的本地 session 取回上下文继续任务。
- 支持 stdin/stdout，能进入 Unix 管道。

示例：

```text
$ mateway chat
mateway chat session=cli:default
type /help for commands, /exit to quit
cli:default> 检查这个项目最近失败的 trace
[thinking] waiting for model output
[thinking] prepared tool call project.index
[tool] project.index - /Users/dongping/project/mateway
[result] project.index - found recent trace files
[thinking] prepared tool call file.read
[tool] file.read - /Users/dongping/.mateway/trace/...
[result] file.read - timeout happened before first tool call
问题在模型等待阶段没有过程事件，现在应改为 model_start 时立即显示。
trace: /Users/dongping/.mateway/trace/...
```

## Slash Command

P0 支持：

- `/help`：显示 CLI 内命令。
- `/new`：重置当前 session，让下一条作为新任务。
- `/sessions`：列出本地 session，包括 CLI、飞书、微信。
- `/session <session_key>`：切换当前 CLI 使用的 session key。
- `/resume [--attach] <session_key>`：从其他 channel 取回会话。
- `/show [session_key]`：查看 session 摘要。
- `/trace [trace_path|session_key]`：查看当前或指定任务的 trace 摘要。
- `/events [--json] [trace_path|session_key]`：按过程事件格式查看 trace；`--json` 输出 NDJSON。
- `/tools [--agent <agent_id>] [--verbose]`：列出 agent 当前工具、启停状态、risk、必填参数和确认边界。
- `/tools enable|disable <tool_name> [--agent <agent_id>]`：修改指定 agent profile 的工具启停配置。
- `/model [--agent <agent_id>] [--verbose]`：查看当前 agent profile 的模型选择链路；`--verbose` 同时列出已加载模型端点。
- `/exit`：退出。

`/resume` 默认是 fork 模式：把源 session 的上下文复制到当前 CLI session，让本地继续任务但不污染原飞书/微信会话。显式加 `--attach` 时，CLI 直接切换到源 session key 上继续。

## Ask 模式

`mateway ask` 保留为一次性命令，并补齐脚本友好选项：

```bash
mateway ask --session cli:debug "分析最近的 trace"
cat error.log | mateway ask --quiet "分析报错原因"
mateway ask --json "输出当前任务摘要"
mateway ask --events "执行任务并输出过程事件"
```

- 默认显示过程和最终回复。
- `--quiet` 只输出最终回复，适合脚本。
- `--json` 输出机器可读响应。
- `--events` 输出 NDJSON 过程事件；工具调用参数在 `args`，工具结果或失败原因在 `summary`，最后一行是 final 事件。
- `--session` 指定 session key。

## 跨 Channel 发送

CLI 可以作为网关管理面，直接向远端 channel 投递文本消息：

```bash
mateway send --to feishu:oc_xxx "部署已完成"
mateway send --to feishu:open_id:ou_xxx "请看一下这个任务"
mateway send --to weixin:wxid_xxx "部署已完成"
```

飞书默认把 `feishu:<id>` 解释为 `chat_id`，也可以显式使用 `feishu:open_id:<id>`、`feishu:user_id:<id>`、`feishu:email:<id>`。多账号飞书可以使用 `feishu:<account_id>:chat_id:<id>`。微信默认使用当前启用账号，也可以用 `weixin:<account_id>:<peer_id>` 指定账号。

## 拉取历史

CLI 可以主动从 channel 拉取近期消息，并导入本地 session，之后用 `/resume` 或 `mateway chat --session ...` 继续：

```bash
mateway fetch-history --from feishu:oc_xxx --session cli:from-feishu --limit 50 --since 48h
mateway fetch-history --from feishu:ops:oc_xxx --session cli:ops
mateway fetch-history --from weixin:wxid_xxx --session cli:from-weixin --limit 20
```

飞书使用消息历史 API，按 chat_id 和时间窗口拉取。微信当前使用 iLink `getupdates` 的增量结果，不是任意时间范围历史；它适合把最近未消费的消息同步成本地 session。

## 中断

交互式 `mateway chat` 捕获 Ctrl+C。执行中的任务会收到 context cancellation 并停在当前 session，CLI 随后回到输入提示符。用户可以继续补充一句修正，也可以输入 `/new` 开新任务。

## TUI 模式

`mateway chat` 在交互式终端中默认进入 TUI；非 TTY 或显式 `--classic` 时回到旧的行式 REPL。`mateway tui` 也可以直接启动全屏入口。

`chat` 不建议删除。它是面向人的默认会话入口，后续会继续向 Claude Code / opencode 这类工作台体验演进；`ask` 负责一次性脚本调用和管道；`chat --classic` 保留给非全屏调试、日志复现和终端能力异常时兜底。

```bash
mateway chat
mateway chat --classic
mateway tui
mateway tui --session cli:review
```

当前按键：

- `Enter`：提交输入。
- `Ctrl+C`：退出。
- `↑/↓`：滚动过程历史。
- `PageUp/PageDown`：大步滚动过程历史。
- 审批时在底部输入 `y` 或 `n` 后回车。
- 支持基础 slash command：`/help`、`/exit`、`/new`、`/session`、`/sessions`、`/resume`、`/show`、`/trace`、`/events`。

当前版本优先验证布局和信息架构。后续如果要做可展开工具块、右侧 todo 实时高亮、命令面板和鼠标交互，可以在这个入口上继续演进，或切换到 Bubble Tea / tview 这类成熟 TUI 框架。

## 审批

交互式 `mateway chat` 会对需要人工确认的高风险本地操作暂停并提示：

```text
[approval] terminal command requires approval: shell
tool: terminal.run
detail: npm install
allow? [y/N]:
```

当前 P1 先覆盖 `terminal.run` 中无法判定为只读的 shell / unknown / guarded mutation 命令。Destructive 命令仍然由工具策略硬阻止，不进入审批。

## 工具清单

可以在 REPL 里使用 `/tools`，也可以从 shell 使用：

```bash
mateway tools list
mateway tools list --agent main --verbose
mateway tools disable terminal.run --agent main
mateway tools enable terminal.run --agent main
```

这个命令用于调试 agent 当前能调用哪些工具，以及哪些工具会触发审批或硬阻止。启停状态写入 `agents.profiles[].tools.allow/deny`：默认没有 allow list 时，除 deny 之外的内置工具都可用；如果配置了 allow list，则只有 allow 中的工具可用。

## 模型诊断

可以在 REPL 里使用 `/model`，也可以从 shell 使用：

```bash
mateway model show
mateway model show --agent main --verbose
mateway model list --verbose
```

`model show` 只做诊断，不切换模型。它输出 global default、agent default、fallbacks、roles 和最终候选链路，便于定位某个 agent/session 实际会优先使用哪些模型。后续如果要做 `/model set`，应先明确状态写入位置，避免把临时调试选择误写进全局配置。

## Session 设计

默认 session：

- `cli:default`：普通本地对话。
- `cli:cwd-<hash>`：通过 `mateway chat --cwd-session` 使用当前目录派生 session。
- `feishu:<thread>` / `weixin:<thread>`：由 gateway 写入的远程 channel session。

P0 的跨 channel 取回基于本地 session store。也就是说，飞书/微信消息只要已经经过 gateway 处理，就可以在 CLI 里通过 `/sessions` 找到并 `/resume`。主动调用飞书 API 拉更早历史消息属于 P1/P2。

## 过程渲染

CLI 消费 runtime 的过程事件，默认渲染为接近 opencode 的轻量过程流，而不是把过程和最终回复混在同一类日志行里：

```text
User
│ review project

+ Thought: prepared tool call project.index
→ Index /Users/dongping/project/mateway
✓ Index (34ms)
→ Read README.md
✓ Read (12ms)
→ Run go test ./...
✓ Run (800ms) - ok

Assistant
最终回复文本
```

在 TTY 中，过程符号会用低对比或状态色区分：thought 黄色、调用蓝色、成功绿色、失败红色、approval 黄色。`waiting for model output` 这类高频空转事件默认不显示；`file.read`、`project.index` 这类读操作的结果内容默认折叠，只显示成功和耗时，详细内容留给 trace / NDJSON 事件。管道和测试输出不带 ANSI 颜色；需要机器可读事件时使用 `--events` 或 `/events --json`。

这些是操作状态，不是 chain-of-thought。CLI 只展示“正在调用什么、返回了什么、等待什么、需要什么权限”。

## 后续优先级

P1：

- NDJSON 事件流。
- 更详细的 `/trace`、`/events`，包含工具参数、成败、耗时和结果摘要。

P2：

- `/model`、工具启停管理。
- 更完整的 TTY 颜色、折叠和展开。
