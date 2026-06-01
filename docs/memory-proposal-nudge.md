# 候选记忆提醒

Mateway 会在任务完成后生成长期记忆候选。候选不会自动进入长期记忆，用户需要 review 后执行 `commit` 或 `reject`。

## 配置

候选提醒配置在：

```text
~/.mateway/config/config.yaml
```

默认配置：

```yaml
memory:
  proposal_nudge:
    enabled: true
    interval: 24h
    channels:
      - cli
    max_proposals: 3
```

字段说明：

- `enabled`: 是否启用候选提醒。
- `interval`: 同一个 session 的提醒间隔，默认 `24h`。
- `channels`: 允许提醒出现在哪些 channel。channel id 用 `mateway channel list` 查看。
- `max_proposals`: 每次最多展示几条候选摘要。

默认只在 `cli` 提醒，避免微信等聊天 channel 被维护消息打扰。需要飞书提醒时：

```yaml
memory:
  proposal_nudge:
    enabled: true
    interval: 24h
    channels:
      - cli
      - feishu
    max_proposals: 3
```

## 提醒内容

提醒不会只显示“有 N 条候选”，而是展示少量摘要：

```text
有 36 条长期记忆候选待审核，我先列 3 条最值得看的：

1. prop_xxx 微信接入 Mateway 的登录流程
   类型：experience / agent，置信度：medium
   价值：记录扫码、账号保存、启用配置和 gateway 重启路径。
   来源：trace:...
   查看：mateway memory proposal show prop_xxx

还有 33 条未展示。查看全部：mateway memory proposal list
```

摘要只用于提醒。完整信息请使用 `show` 命令。

## 查看候选详情

```bash
mateway memory proposal list
mateway memory proposal show <proposal_id>
```

`show` 会展示：

- proposal id
- 状态
- 类型和范围
- 标题
- 置信度
- 创建/更新时间
- 来源
- 为什么值得保存的摘要
- 候选正文
- commit/reject 操作命令

## 处理候选

保存为长期记忆：

```bash
mateway memory proposal commit <proposal_id>
```

忽略候选：

```bash
mateway memory proposal reject <proposal_id> --reason "一次性调试记录"
```

聊天入口中，当 runtime 正在等待 `memory_proposal_review` 时，也可以回复 `保存` / `忽略` 或 `save` / `ignore`。
