# 执行流程

Mateway 的稳定主线是 TaskGraph Runtime：

```text
入站消息
  -> Planner
  -> TaskGraph
  -> Scheduler
  -> Node Executor
  -> Node Verifier
  -> Graph / Task Verifier
  -> Finalizer
  -> Memory Observe
```

## 1. 入站消息

Gateway 和 channel adapter 只负责 I/O、归一化和投递。Runtime 根据 session state 决定消息是新任务、续接任务、pending human control，还是已有 graph 的 steering input。

## 2. Planner

Planner 是唯一的任务规划入口。它一次输出 `TaskGraphPlan`：任务级 goal、risk、acceptance、required capabilities、final output shape，以及子任务 nodes。

Planner 不生成工具调用序列。它生成可验收子任务、依赖关系、执行 mode、allowed tools、skill 选择、human gates 和每个 node 的 acceptance。

历史 `TaskContract` 的完成语义会合并到 Planner 输出中，不再作为独立规划阶段暴露。

## 3. TaskGraph And Scheduler

Runtime 校验 Planner 输出后持久化 TaskGraph。Scheduler 根据 `depends + status` 计算 ready nodes。第一版使用本地调度；后续可开启 `max_parallel_nodes` 进行本地并发。

Completed and verified nodes are never rerun. Pending nodes only run when all dependencies completed or skipped.

## 4. Node Executor

Node 是可验收子任务，不是工具调用。Node execution mode 决定执行方式：

- `direct`: 一次模型调用。
- `react`: node-local AgentCore loop，可调用 allowed tools。
- `skill`: 加载已注册 skill metadata 和 `SKILL.md`。
- `script` / `tool`: 确定性执行特例。
- `human`: 等待确认或审阅。

Node 内部 tool calls 只写入 trace/evidence refs。工具成功不是 node 成功；node final output 必须通过 verifier。

## 5. Verification, Retry, Replan

Node 完成后进入 verifier。Verifier 先做确定性检查，必要时调用 model verifier。

若 node 验收不合格，runtime 使用 verifier feedback 重试同一 node。若 attempts 耗尽，runtime 可触发 local replan：保留 completed upstream nodes，替换 failed node 和 downstream pending nodes。

Graph / task verifier 在所有关键节点完成后检查 task acceptance。如果最终验收不满足，runtime 应追加 repair/synthesis node 或局部 replan，而不是从头执行。

## 6. Human Control

高风险操作、用户明确要求确认或人工审阅时，Planner 插入 `human_confirm` 或 `human_review` node。执行到该 node 时 Runtime 创建 pending action，等待用户继续或取消。

Human confirmation is a node-level gate. It does not bypass tool policy, path validation, secret redaction or verifier.

## 7. Trace, Session, Recovery

Trace 记录事实链：planner、scheduler、node execution、tool calls、verifier、finalizer 和 memory observe。关键事件必须包含 `task_id`、`graph_id`、`node_id` 和 `attempt`。

Session graph state 是恢复状态。崩溃恢复时 completed verified nodes 跳过，running nodes 恢复为 pending/retryable，awaiting input nodes 继续等待用户输入。

TaskGraph 是单个任务内部的 DAG 状态，不是 Git-like tree store。Session 保存可恢复快照，Trace 保存 append-only 事件账本，Memory 保存任务完成后的蒸馏知识。

历史任务的继续和分支由 Task Lineage Tree 表达，而不是由 Session 表达。未完成任务从原 graph state 恢复；已完成任务如果继续，应 fork 新 task，并记录父任务、可选的旧 node/evidence 引用。

长期 Memory Tree/Graph 可以作为 heartbeat/offline distill 的可重建索引演进，用于主体-关系-客体、项目事实和经验沉淀，但不进入 runtime 主状态。

## 8. Finalizer And Memory

Finalizer 只使用 verified node results 生成最终回答或 blocker。任务结束后 runtime 将 GraphMemorySummary 交给 memory observe。Heartbeat 可在离线阶段继续做长期学习和主体-关系-客体整理。
