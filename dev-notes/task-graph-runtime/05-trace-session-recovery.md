# 05 Trace、Session 与恢复

## 目标

让 trace、session 和 recovery 成为 graph runtime 的一等基础能力。

## 层次边界

- Trace：事实证据链，append-only JSONL。
- Session graph state：可恢复执行状态。
- Task Lineage Tree：历史任务之间的父子/分支关系。
- Memory：任务完成后的 task/node learning 蒸馏。

不要混用这些层。

Mateway 可以借用 Git-like 原则，但不做 Git-like 存储：

- immutable event history 属于 trace
- mutable resume state 属于 session
- 历史任务分支属于 Task Lineage
- durable user/project knowledge 属于 memory
- graph dependencies 属于 TaskGraph

本阶段不要实现 content-addressed tree/object database。

## 必需 Trace 字段

Graph 相关事件必须包含：

- `trace_id`
- `task_id`
- `graph_id`
- `node_id`，适用于 node 事件
- `attempt`，适用于 node attempt
- event type
- status / summary / evidence ref，按事件需要写入

核心事件：

```text
planner_start, planner_output, planner_validated, graph_attached
node_scheduled, node_started, node_tool_call, node_tool_result
node_final_output, node_verifier_start, node_verified
node_retry, node_failed, node_blocked, node_awaiting_input
local_replan_start, local_replan_applied
graph_finalize_start, graph_completed, graph_blocked, graph_failed
memory_observe_start, memory_written
```

## 恢复规则

进程重启或恢复任务时：

- completed verified nodes 保持 completed
- running nodes 变成 pending/retryable
- awaiting_input nodes 保留 pending actions
- failed/blocked nodes 保留失败状态
- pending/ready nodes 继续由 scheduler 处理

高风险 mutation node 在模糊崩溃后不应静默重跑，应要求 evidence check 或 human review。

## Task Lineage 规则

未完成任务优先从原 graph state 恢复。已完成任务不修改历史，而是 fork 新 task：

- `ParentID` 指向父任务
- `ForkedFromNodeID` 可指向旧 graph node
- `ContextRefs` 指向旧 task/node/evidence

这用于“从历史任务继续”和“从某个节点分支”。它不是 Session tree；Session 仍只保存当前对话和恢复快照。

## 待办

- [ ] 审计 graph trace events 是否都有 required IDs 和 attempt。
- [ ] 在 session 中持久化 node output、evidence、verifier state。
- [ ] 强化 running nodes 和 pending human actions 的恢复。
- [ ] 增加 crash-style resume 测试：running/pending/awaiting/completed。
- [ ] 确保 trace secrets 始终脱敏。
- [ ] 将 CLI/TUI 侧栏从 contract/step 展示改为 graph/node status 展示。
- [ ] 预留或实现 Task Lineage 字段：`ParentID`、`ForkedFromNodeID`、`ContextRefs`。

## 验收标准

- 任务可以从第一个未完成 node 继续。
- completed nodes 恢复后不重跑。
- trace 可以重建 planner、node actions、verifier、finalizer 路径。
- memory observe 引用 graph/node evidence refs，而不是 raw trace dump。
- 已完成历史任务的继续操作会创建 fork，而不是改写旧 task。
