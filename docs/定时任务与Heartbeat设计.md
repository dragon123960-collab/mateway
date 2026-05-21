# 定时任务与 Heartbeat 设计

## 总体结论

Heartbeat 是 best-effort background maintenance，不是强 cron。
用户发布的业务定时任务是另一类 schedule，不属于 heartbeat job。

```text
scheduler 负责什么时候运行
heartbeat 负责为什么运行、运行哪些维护策略
memory pipeline 负责怎么整理、提议、提交
user scheduled task 负责按用户授权执行业务请求并交付结果
```

关键原则：

- `gateway serve` 运行时才可能触发 scheduler。
- 单实例锁避免多进程重复运行。
- heartbeat 不承诺精准间隔。
- 错过时间后可以补跑一次。
- 关键状态推进不能只依赖 heartbeat。
- 半夜不主动询问用户。

## 配置建议

```yaml
scheduler:
  enabled: false
  timezone: Asia/Shanghai
  state_dir: ""

agents:
  profiles:
    - id: main
      heartbeat:
        enabled: true
        schedule:
          daily_at: "03:30"
        jobs:
          - memory_daily_review
          - memory_recent_compact
          - memory_index_rebuild
          - memory_lint
        auto_send_summary: false
```

默认：

- scheduler 默认关闭。
- `ask/test/doctor` 不启动 scheduler。
- `gateway serve` 才会启动 scheduler。
- heartbeat 只写 recent、inbox、log、report，不提交高影响长期记忆。
- 自动 scheduler 只运行低风险维护 job，不主动发送消息。
- SQLite FTS5 后续可作为 Markdown memory 的可重建全文索引；当前不作为必需依赖。

## 用户发布的业务定时任务

例子：

- 每天早上帮我收集 AI 最新趋势文章。
- 每周五汇总某个项目的 open issues。
- 每天下午检查某个接口状态并给我报告。

这类任务需要独立于 heartbeat 建模：

- heartbeat 是系统维护任务，由配置控制，默认不主动发消息。
- user scheduled task 是用户授权的业务任务，有 owner、prompt、schedule、delivery target。
- 两者可以复用底层 scheduler tick、state store 和 due job 判断，但不能共用同一份 job 白名单。
- scheduler 只负责判断任务到期和派发，不重新实现 agent 执行逻辑。
- 任务正文必须构造成 scheduled invocation 交给现有 runtime 主链执行。
- runtime 继续负责 planning、tool policy、act、observe、synthesize、reply 和 memory proposal。
- delivery 层只负责把 runtime 的最终结果发回指定 channel/thread 或写入指定 workspace artifact。

建议任务定义：

```yaml
id: ai-trends-daily
title: Daily AI trends
owner:
  channel: feishu
  thread_id: example-thread
  user_id: example-user
agent_id: main
schedule:
  daily_at: "09:00"
prompt: "Collect recent AI trend articles, summarize key points, and include sources."
allowed_tools:
  - web.search
delivery:
  channel: feishu
  thread_id: example-thread
confirmation:
  create: required
  risky_tools: required
limits:
  max_runtime_seconds: 300
  max_output_chars: 6000
```

默认策略：

- 创建、修改、删除 schedule 必须用户确认。
- 用户一次没有说全信息时，先进入补问，不写 proposal。
- 创建 proposal 至少需要 title、prompt、daily_at。
- 每次执行仍然经过 tool risk policy。
- 低风险 search/read/summarize/report 可自动执行。
- shell、file write、external posting、admin action 等高风险动作仍需确认。
- 默认尊重 quiet hours，不在静默时间主动打扰。
- 运行结果要记录 sources/evidence。
- 失败记录 last_error，不无限重试。

状态建议：

```text
~/.mateway/schedules/tasks/*.yaml
~/.mateway/run/scheduler/user_tasks_state.json
```

## 适合定时的 Job

### memory_daily_review

职责：

- 汇总当天任务。
- 写 `recent/YYYY-MM-DD.md`。
- 汇总 open questions。
- 汇总 decisions。
- 第一版不生成 long memory proposal。
- 更新 `log.md`。

默认行为：

- 可自动写 recent。
- 第一版只写事实索引和 log。
- 不自动写 long memory。
- 不主动发消息，除非配置打开 summary。

### memory_recent_compact

职责：

- 清理过旧 recent。
- 将多日 recent 压缩为周摘要。
- 把明显长期价值的条目写入 inbox proposal。

### memory_lint

记忆库体检，不是沉淀主路径。

检查：

- broken `[[wikilinks]]`
- 缺少 frontmatter
- 缺少 source
- 重复页面
- 冲突事实
- 长期未处理 inbox proposal
- 过期 recent memory
- 孤立页面

运行方式：

- 手动优先：`mateway memory lint`
- 可选低频定时：每周一次或随 heartbeat 配置运行
- 默认只报告，不修改。

### memory_index_rebuild

职责：

- 从 Markdown memory wiki 重建 `workspace/memory/index.json`。
- 作为用户手工编辑 Markdown 后的补漏刷新。
- 不做模型判断，不提交长期记忆。

运行方式：

- 手动：`mateway memory index`
- heartbeat：`mateway heartbeat run --agent main --job memory_index_rebuild`

## 不适合默认定时的 Job

### memory_skill_candidates

不建议作为默认定时任务。

原因：

- 技能结晶应该紧贴成功任务事件。
- 半夜不应该询问用户。
- 定时扫描只能作为补漏或 lint，不应作为主路径。

主路径：

```text
task success
-> update pattern counter
-> threshold reached
-> write skill candidate
-> ask user on next interaction
```

## Scheduler 状态

未来状态文件：

```text
~/.mateway/run/scheduler/state.json
```

记录：

- job name
- agent id
- last_run_at
- last_status
- last_error
- next_run_at

## 手动命令建议

```bash
mateway heartbeat status
mateway heartbeat run --agent main --job memory_daily_review
mateway heartbeat run --agent main --job memory_index_rebuild
mateway memory lint
mateway memory index
```
