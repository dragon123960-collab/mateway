# 04：Scheduler

更新：2026-06-15

## 目标

实现本地单进程 graph scheduler：根据 node `depends + status` 计算 ready nodes，推进 graph 状态，并限制并行度。

Scheduler 是 deterministic runtime logic，不由模型决定。

## 当前机制参考

- `internal/runtime/runtime.go`
  - 当前单任务 `runTask`
  - execution phase
- `internal/session/store.go`
  - task status 和 execution frame
- 阶段 02 `TaskGraph`
- 阶段 03 graph planner 输出

## 调度规则

Ready 规则：

```text
node.status == pending
AND all depends nodes are completed or skipped
```

Blocked 规则：

- 任一 required dependency failed/blocked，则依赖它的 pending node 暂不可 ready。
- graph 内存在 awaiting_input node 时，graph status 为 `awaiting_input`。
- graph 内存在 blocked node 且无 ready/running node 时，graph status 为 `blocked`。

Completed 规则：

- 所有关键 node 都 `completed` 或 `skipped`，graph status 为 `completed`。
- 第一版不实现 optional node；`skipped` 只能由 replan/finalizer 显式设置，scheduler 不随意跳过。

## 并行策略

- 配置项建议：`max_parallel_nodes`，默认 1。
- 本阶段可以先实现 ready calculation 和 sequential dispatch。
- 如果实现并行，只允许本地 goroutine，并必须保存 node status，避免重复执行。
- 不做分布式队列。

## 实现 TODO

- [ ] 新增 scheduler 类型或纯函数，输入 `TaskGraph`，输出 ready node IDs 和 graph status update。
- [ ] 实现 `ReadyNodes(graph, max)`。
- [ ] 实现 `UpdateGraphStatus(graph)`。
- [ ] 执行前将 ready node 标记为 `running`，保存 session。
- [ ] node 完成/失败后重新计算 ready nodes。
- [ ] 支持 `resume_node`：只恢复 blocked/failed/awaiting node，不重跑 completed nodes。
- [ ] trace 记录 `graph_schedule_tick`、`graph_ready_nodes`、`node_scheduled`。

## 测试 TODO

- [ ] 无依赖节点 ready。
- [ ] dependency 未完成时节点不 ready。
- [ ] dependency completed 后节点 ready。
- [ ] diamond graph 中 B/C 可同时 ready，D 等 B/C 完成才 ready。
- [ ] `max_parallel_nodes` 限制 ready 输出数量。
- [ ] completed node 不会再次 ready。
- [ ] failed dependency 阻止下游 ready。
- [ ] awaiting input node 使 graph status 为 `awaiting_input`。
- [ ] resume blocked node 不重跑 completed nodes。

## 非目标

- 不实现 planner。
- 不实现 node executor 内部逻辑。
- 不做分布式调度。
- 不做 cron/workflow platform。
- 不引入 multi-agent supervisor。

## Codex Review 重点

- ready calculation 是否完全由状态和依赖决定。
- 是否不会重跑 completed nodes。
- 是否有 session 保存点，防重复执行。
- 是否没有模型参与调度。
- 是否没有把 scheduler 放进 gateway/channel。
