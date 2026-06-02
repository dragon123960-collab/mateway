# Channel 配置与内置接入

Mateway 当前采用内置 channel 模式：`gateway serve` 是唯一常驻入口，负责启动已启用的 channel、统一 session key、dedupe、异步运行 runtime 和发送回复。`gateway serve` 也会在 `web.enabled` 为 true 时启动本地 [Web Console](./web-console.md)；Web Console 是 control plane，不算外部消息 channel。

同一个 `gateway serve` 进程内，Feishu、Weixin、Web 和 schedule 触发的 runtime 都会写 JSONL trace，并把新 trace event 发布到 WebSocket event bus。打开 Office Watch 后，选择对应 `session_key` 就能实时观察任务进度；服务重启后的历史查看走 trace replay。

## 查看 Channel ID

配置中使用的 channel 名称以本机 `~/.mateway/config/channels/*.yaml` 文件名为准。运行：

```bash
mateway channel list
```

示例：

```text
ID      ENABLED  CONFIG
feishu  true     /Users/example/.mateway/config/channels/feishu.yaml
weixin  true     /Users/example/.mateway/config/channels/weixin.yaml
```

`ID` 列就是配置里应使用的 canonical channel id，例如 `feishu`、`weixin`。不要在 runtime 配置里写 `lark`、`wechat` 这类别名。

## 配置目录

channel 配置位于：

```text
~/.mateway/config/channels/
  feishu.yaml
  feishu.sample.yaml
  weixin.yaml
  weixin.sample.yaml
```

`mateway channel list` 会跳过 `.sample.yaml` 和 `.example.yaml`。

## 开发新 Channel 的边界

新增稳定 channel 时，优先做成内置 channel spec：

- channel package 只负责平台 I/O 和消息归一化。
- gateway 负责 session key、dedupe、异步 runtime 执行和 channel serving。
- observe/Web 只订阅 runtime trace event，不参与 channel I/O。
- runtime 负责 setup -> AgentCore loop -> finalize。
- tool/connector 能解决的问题，不上升到 runtime 复杂机制。

新 channel 接入后应提供：

- `~/.mateway/config/channels/<id>.yaml` 的默认模板。
- `mateway channel list` 可见的 `<id>`。
- 入站消息归一化为 `channel.InboundMessage`。
- 文本回复发送能力。
- 明确说明媒体能力是否支持。

当前版本不做进程外 bridge，也不做多 adapter 进程管理。服务重启后读取新配置即可接受。
