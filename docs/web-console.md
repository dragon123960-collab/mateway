# Web Console 本地控制台

Mateway Web Console 是随 `gateway serve` 启动的本地 control plane，用来在浏览器里查看和管理 runtime 状态。它不是外部消息 channel，不参与 Feishu/Weixin 这类平台 I/O。

实时执行事件来自同一个 gateway 进程内的 trace event bus。只要 Feishu、Weixin、Web 或 schedule 任务由这个进程执行，Office Watch 就可以按对应 `session_key` 实时订阅进度；服务重启或跨进程后的历史查看走 JSONL trace 回放。

## 启动

```bash
mateway gateway serve
```

默认访问地址：

```text
http://127.0.0.1:8765
```

默认配置位于 `~/.mateway/config/config.yaml`：

```yaml
web:
  enabled: true
  bind: 127.0.0.1:8765
  open_browser: false
  allow_config_write: true
  realtime_enabled: true
  office_watch_enabled: true
  office_watch_assets: ""
```

首版默认只监听 `127.0.0.1`。如果改成局域网地址，请先补充认证或反向代理保护。

## 页面

- 对话：向本地 runtime 发送任务，并在对话页的 session 列表中查看 usage、切换会话或新建 Web 会话。
- 技能：查看 skill 列表、active/cold/hidden 状态和 cleanup restore。
- 定时任务：创建、激活、暂停和测试 schedule。
- 配置：查看非 secret 配置，选择默认模型，开关 Feishu/Weixin，查看 agents，调整 Web 监听地址，并可写入允许的主配置块。
- 记忆：查看 memory、learning、skill events 和 proposal 统计。上下文窗口/token 使用在右侧状态和 session usage 中显示。
- Office Watch：独立运行态看板 `/watch`，通过 WebSocket 展示任务发布、context、模型轮次、工具、usage、回复和完成状态。

Office Watch 只借鉴像素办公室的表现方式，使用 Mateway 自制 CSS/占位素材，不提交 Star-Office-UI 的非商用资产。`context est` 是基于字符数的估算；`model_usage` 中的 input/output/total tokens 才来自模型 provider。

飞书 session key 通常来自 thread/message 归一化；在 Office Watch 左侧选择对应 session 后即可查看实时事件或最近 trace。WebSocket 只推送当前进程内的新事件，不保证离线期间补发。

## API

Web Console 后端直接复用 Go internal API，不解析 CLI 输出：

- `POST /api/chat`
- `GET /api/overview`
- `GET /api/skills`
- `GET /api/skills/cleanup`
- `POST /api/skills/:id/restore`
- `GET /api/schedules`
- `POST /api/schedules`
- `PATCH /api/schedules/:id/activate`
- `PATCH /api/schedules/:id/pause`
- `PATCH /api/schedules/:id/test`
- `GET /api/sessions`
- `GET /api/sessions/:key`
- `GET /api/events/ws?session_key=<key>`
- `GET /api/runs?session_key=<key>`
- `GET /api/runs/:trace_id`
- `GET /api/channels`
- `GET /api/agents`
- `GET /api/config`
- `PATCH /api/config`
- `GET /api/memory/report`

## 安全边界

- Web Console 默认本机访问，不应直接暴露公网。
- `PATCH /api/config` 只写主配置中的非 secret 区域。
- 含 `password`、`secret`、`token`、`api_key`、`authorization` 等字段的 patch 会被拒绝。
- 写操作会追加审计到 `~/.mateway/observe/audit/web.jsonl`。
- Channel 凭证仍应通过 env 或 secret store 管理，不在 Web Console 中保存明文。
