# Channel 配置与内置接入

Mateway 当前采用内置 channel 模式：`gateway serve` 是唯一常驻入口，负责启动已启用的 channel、统一 session key、dedupe、异步运行 runtime 和发送回复。

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

## 飞书多机器人

`channels/feishu.yaml` 支持一个 channel 下的多个机器人账号。顶层字段作为共享默认值，`feishu.accounts[]` 中的每个账号会启动一个独立 WebSocket client，并把账号 id 写入入站消息的 `metadata.account_id`。

```yaml
feishu:
  enabled: true
  base_url: https://open.feishu.cn
  websocket:
    enabled: true
  accounts:
    - id: ops-bot
      app_id_env: MATEWAY_FEISHU_OPS_APP_ID
      app_secret_env: MATEWAY_FEISHU_OPS_APP_SECRET
    - id: local-bot
      app_id_env: MATEWAY_FEISHU_LOCAL_APP_ID
      app_secret_env: MATEWAY_FEISHU_LOCAL_APP_SECRET
```

然后用 account id 绑定不同 agent：

```bash
mateway agent bind --channel feishu --account-id ops-bot ops
mateway agent bind --channel feishu --account-id local-bot local
```

如果还要在同一个机器人内按群聊或私聊分流，可以继续加 `--peer-id <open_chat_id>`。绑定匹配顺序会同时检查 `channel`、`account_id` 和 `peer_id`，未命中则使用默认 agent。

## 开发新 Channel 的边界

新增稳定 channel 时，优先做成内置 channel spec：

- channel package 只负责平台 I/O 和消息归一化。
- gateway 负责 session key、dedupe、异步 runtime 执行和 channel serving。
- runtime 负责 setup -> AgentCore loop -> finalize。
- tool/connector 能解决的问题，不上升到 runtime 复杂机制。

新 channel 接入后应提供：

- `~/.mateway/config/channels/<id>.yaml` 的默认模板。
- `mateway channel list` 可见的 `<id>`。
- 入站消息归一化为 `channel.InboundMessage`。
- 文本回复发送能力。
- 明确说明媒体能力是否支持。

当前版本不做进程外 bridge，也不做多 adapter 进程管理。服务重启后读取新配置即可接受。
