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
[thinking] prepared tool call file.read
[tool] file.read - /Users/dongping/project/mateway
[result] file.read - found recent trace files
[thinking] prepared tool call file.read
[tool] file.read - /Users/dongping/.mateway/trace/...
[result] file.read - timeout happened before first tool call
问题在模型等待阶段没有过程事件，现在应改为 model_start 时立即显示。
trace: /Users/dongping/.mateway/trace/...
```

## Slash Command

Mateway 是通用 agent 网关，slash command 不应只围绕代码仓库，而应围绕 session、channel、agent、工具、模型、trace、memory 和本地控制。命令分组如下。

P0 已支持或应优先支持：

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

P1/P2 通用控制台命令：

- `/agent [agent_id]`：查看或切换当前 agent profile。
- `/channels`：查看已启用 channel、账号和绑定关系。
- `/send --to <channel:target> <message>`：从 TUI 内直接向飞书/微信等 channel 发送消息。
- `/fetch-history --from <channel:target> [--limit <n>]`：从远端 channel 主动拉历史并导入当前 session。
- `/memory search <query>`：搜索当前 agent 可用记忆。
- `/memory proposals`：查看待确认的记忆/经验沉淀。
- `/config`：查看当前配置摘要，不输出 secret。
- `/workspace`：查看本地工作目录、可访问路径和 sandbox 状态。

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
- 支持基础 slash command：`/help`、`/exit`、`/new`、`/session`、`/sessions`、`/resume`、`/show`、`/trace`、`/events`。

当前 TUI 已采用 Bubble Tea / Bubbles / Lip Gloss 作为成熟 Go TUI 框架，不再扩展手写 ANSI/raw input 实现。界面参考 opencode 的 split-footer 思路：上方过程区追加 transcript / tool event，底部 footer 负责输入、状态和命令入口，宽屏时右侧常驻 Mateway 状态栏。Todo、工具详情和问题不应照搬为编程任务侧栏，而应作为过程区的结构化 entry、右侧状态摘要或底部临时 panel 展示。

opencode 的 TUI 使用 `@opentui/core`、`@opentui/solid` 和 `@opentui/keymap`，输入由 textarea 组件处理，宽度使用成熟 string-width 能力，scrollback 与 footer 分离。Mateway 对应采用 Bubbles `viewport` 承载过程区、`textarea` 承载底部输入、Lip Gloss 负责状态行和样式、`go-runewidth` 处理中文宽度。后续 TUI 改进必须优先落在这些组件之上，避免重新引入自维护终端渲染逻辑。

继续调研 opencode 后的几个设计判断：

- 鼠标滚动和右侧滚动条来自 renderer 的 mouse support 与独立 `scrollbox`，不是业务层判断鼠标在左侧还是右侧。
- 光标闪烁由 textarea/cursor 组件负责；TUI 应使用终端真实 cursor，不要用文本字符模拟。
- 底部状态应显示 Mateway 自己的状态，例如 `Idle`、`Thinking`、`Acting`，不要照搬 `Build` 等 opencode agent 标签。
- 模型名称应来自 Mateway 配置里的实际 model，不附加模仿式 provider 文案。

右侧常驻状态栏应优先展示 Mateway 的网关信息：

- 当前 agent、session、model 和 channel。
- 当前任务状态、过程事件数量、最新 trace。
- 本地上下文，例如 cwd、workspace、可访问路径和 sandbox 状态。
- channel bridge 状态，例如 CLI、飞书、微信账号是否启用。
- 工具状态，例如启用工具数量、最近工具执行结果和 blocked tool。
- 关键 slash 命令提示。

## 差异化

Mateway CLI 可以学习 opencode / Claude Code 的交互清晰度，但定位不同：

- 编程 CLI 的中心是代码仓库、diff、LSP、测试和 Todo；Mateway 的中心是跨 channel session、agent profile、tool contract、trace、memory 和本地控制。
- 编程 CLI 的右侧栏适合显示 Todo / LSP / cost；Mateway 的右侧栏应显示 gateway、channel、session、agent、model 和 tool 状态。
- 编程 CLI 的 `/init`、`/diff`、`/commit` 是高频；Mateway 的高频是 `/resume`、`/sessions`、`/trace`、`/events`、`/tools`、`/model`、`/send`、`/fetch-history`。
- 编程 CLI 主要接管本地项目；Mateway 要能把飞书/微信里的任务拉回本地继续，也要能从本地把消息、结果、提醒发回远端 channel。
- 编程 CLI 的可观测性服务于代码变更；Mateway 的可观测性服务于 agent 网关调试，必须能回答“哪个 agent、哪个 channel、哪个 session、调用了什么工具、成功还是失败、trace 在哪”。

## 工具清单

可以在 REPL 里使用 `/tools`，也可以从 shell 使用：

```bash
mateway tools list
mateway tools list --agent main --verbose
mateway tools disable terminal.run --agent main
mateway tools enable terminal.run --agent main
```

这个命令用于调试 agent 当前能调用哪些工具，以及哪些工具有 hard boundary 或会被策略阻止。启停状态写入 `agents.profiles[].tools.allow/deny`：默认没有 allow list 时，除 deny 之外的内置工具都可用；如果配置了 allow list，则只有 allow 中的工具可用。

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

+ Thought: prepared tool call file.read
→ Index /Users/dongping/project/mateway
✓ Index (34ms)
→ Read README.md
✓ Read (12ms)
→ Run go test ./...
✓ Run (800ms) - ok

Assistant
最终回复文本
```

在 TTY 中，过程符号会用低对比或状态色区分：thought 黄色、调用蓝色、成功绿色、失败红色。`waiting for model output` 这类高频空转事件默认不显示；`file.read` 这类读操作的结果内容默认折叠，只显示成功和耗时，详细内容留给 trace / NDJSON 事件。管道和测试输出不带 ANSI 颜色；需要机器可读事件时使用 `--events` 或 `/events --json`。

这些是操作状态，不是 chain-of-thought。CLI 只展示“正在调用什么、返回了什么、等待什么、被什么策略阻止”。

## 后续优先级

P1：

- NDJSON 事件流。
- 更详细的 `/trace`、`/events`，包含工具参数、成败、耗时和结果摘要。

P2：

- `/model`、工具启停管理。
- 更完整的 TTY 颜色、折叠和展开。
