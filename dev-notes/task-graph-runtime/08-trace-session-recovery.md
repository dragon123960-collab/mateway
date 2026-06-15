# 08：Trace / Session / Recovery

更新：2026-06-15

## 目标

让 trace 和 session 记录 `graph_id`、`node_id`、node status 和恢复点，支持从 graph state 继续任务，而不是只靠 transcript 推断。

## 当前机制参考

- `internal/session/store.go`
  - session JSON
  - archive
  - task trace refs
- `internal/runtime/trace.go`
- `internal/runtime/runtime.go`
- `ContinuationDecision`
- progress sink

## Trace 规则

新增 trace event 应包含：

- `task_id`
- `graph_id`
- `node_id`（node 事件必须有）
- `phase`
- `status`
- `reason`

关键事件：

- `continuation_decision`
- `graph_planned`
- `graph_validation_failed`
- `graph_schedule_tick`
- `node_scheduled`
- `node_execute_start`
- `node_execute_result`
- `node_verified`
- `graph_finalized`

## Recovery 规则

恢复时：

- completed node 不重跑。
- running node 如无 durable in-flight marker，按 retry policy 变为 pending/blocked，由 scheduler 决定。
- awaiting input node 等用户输入。
- blocked/failed node 只有明确 resume/retry 才继续。
- completed graph 不重新激活，只能作为 context refs。

## 实现 TODO

- [ ] trace identity 增加 graph/node 上下文 helper。
- [ ] session 保存 graph state 的每次 status 变化。
- [ ] node execute 前后都有持久化保存点。
- [ ] `ContinuationDecision` 使用 graph/node state，而不是只看 `ActiveTask`。
- [ ] recovery helper 根据 graph state 返回可继续节点或 blocker。
- [ ] trace summary 命令兼容旧 trace，同时能显示 graph/node 事件。

## 测试 TODO

- [ ] node completed 后恢复不会重跑。
- [ ] blocked node 恢复后等待明确 resume。
- [ ] awaiting input 恢复后用户回复进入同 node。
- [ ] completed graph 被引用时不更新旧 graph trace refs。
- [ ] trace event 包含 graph_id/node_id。
- [ ] 旧 trace summary 仍可读。

## 非目标

- 不实现分布式 checkpoint。
- 不迁移历史 archive 文件。
- 不破坏现有 trace summary 命令。
- 不把 runtime state 写到项目根目录。

## Codex Review 重点

- 是否有足够保存点防重复执行。
- trace refs 是否不污染被引用旧任务。
- recovery 是否依赖 graph state，而不是只靠 transcript。
- 是否兼容旧 session/trace。
