# 微信接入指南

Mateway 的个人微信接入使用内置 `weixin` channel，参考 Hermes 的 iLink Bot API 路线。它不是微信协议逆向，也不是 OpenClaw 插件兼容层。

当前支持：

- 扫码登录。
- 保存账号凭据到本地 run 目录。
- 长轮询接收文本消息。
- 发送文本回复。
- 复用 `context_token`，保持微信侧会话上下文。

当前不支持：

- 图片、语音、文件、视频等媒体上传或 CDN 处理。
- 直接实现个人号协议。
- 外部 adapter/bridge 进程。

## 1. 登录

```bash
mateway weixin login
```

终端会显示二维码。用微信扫描并确认后，Mateway 会保存账号文件：

```text
~/.mateway/run/weixin/accounts/<account_id>.json
```

账号文件包含 token，请不要提交到仓库。

## 2. 启用

```bash
mateway weixin enable
```

这会更新：

```text
~/.mateway/config/channels/weixin.yaml
```

典型配置：

```yaml
weixin:
  enabled: true
  base_url: "https://ilinkai.weixin.qq.com"
  account_id: "<account_id>"
  token: ""
  token_env: MATEWAY_WEIXIN_TOKEN
  poll_timeout_ms: 35000
  retry_interval: 3s
```

`weixin enable` 不会把 token 写入配置文件。运行时会优先从 env 或本地账号文件加载 token。

## 3. 重启 Gateway

当前版本不做热启用 channel。启用后重启 gateway：

```bash
mateway gateway restart
```

查看状态：

```bash
mateway gateway status
```

查看日志：

```bash
tail -n 80 ~/.mateway/logs/mateway-gateway.err.log
```

成功启动时会看到类似：

```text
mateway weixin channel starting account=39e9698f base_url=https://ilinkai.weixin.qq.com
```

收到并回复消息时会看到：

```text
mateway weixin received 1 update(s)
mateway weixin inbound message_id=...
mateway weixin sending reply message_id=...
mateway weixin sent reply message_id=...
```

## 4. 验证

在微信里给绑定的 bot 发：

```text
你好，你叫什么
```

如果 `~/.mateway/workspace/agents/main/soul.md` 中定义了身份，例如“小代”，runtime 回复应体现该身份。

## 5. 排障顺序

1. `mateway channel list` 确认 `weixin` 存在且 `ENABLED=true`。
2. `mateway gateway status` 确认 gateway 正在运行。
3. 查看 `~/.mateway/logs/mateway-gateway.err.log` 是否有 `mateway weixin channel starting`。
4. 如果没有收到入站日志，检查账号是否过期，必要时重新 `mateway weixin login`。
5. 如果收到但发送失败，查看 `sendmessage failed` 的 `ret`、`errcode`、`errmsg`。

## 6. 安全边界

- token 只应保存在 `~/.mateway/run/weixin/accounts/` 或 `~/.mateway/config/mateway.env`。
- 不要把账号 JSON、runtime state、日志中的敏感内容提交到仓库。
- 当前 channel 只承诺文本消息闭环，媒体能力需要后续单独设计。
