# 定时任务与 Heartbeat 设计

## 总体结论

Heartbeat 是 best-effort background maintenance，不是强 cron。

```text
scheduler 负责什么时候运行
heartbeat 负责为什么运行、运行哪些维护策略
memory pipeline 负责怎么整理、提议、提交
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
          - memory_lint
        auto_send_summary: false
```

默认：

- scheduler 默认关闭。
- `ask/test/doctor` 不启动 scheduler。
- `gateway serve` 才会启动 scheduler。
- heartbeat 只写 recent、inbox、log、report，不提交高影响长期记忆。

## 适合定时的 Job

### memory_daily_review

职责：

- 汇总当天任务。
- 写 `recent/YYYY-MM-DD.md`。
- 汇总 open questions。
- 汇总 decisions。
- 生成 long memory proposal。
- 更新 `log.md`。

默认行为：

- 可自动写 recent。
- 可自动写 inbox proposal。
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
- 可选低频定时：每周一次
- 默认只报告，不修改。

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
mateway memory lint
```

第一版只实现 `mateway memory lint`。

