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

## Session 保存点

本阶段要防止重复执行，保存点必须具体：

- graph planned 后保存。
- ready node 被标记 running 前保存。
- node execute start 后保存 attempts/status。
- node execute result 后保存 result/evidence/failure。
- node verifier apply 后保存 acceptance/verified_at/status。
- human pending 创建后保存 pending action。
- finalizer 更新 task/graph status 后保存。

如果某个保存点暂时无法接入 runtime，必须在代码注释或 TODO 中说明对应风险，不能默默省略。

## Concurrency Config 规则

`max_parallel_tools` 和 `max_parallel_nodes` 不是同一个配置，不能合并。

- `max_parallel_tools`：已有配置，作用域是单个 AgentCore / node executor 内部的 tool calls 并发。
- `max_parallel_nodes`：Task Graph scheduler 配置，作用域是同一 graph 中同时进入 running 的 ready nodes 数量。

两者位于不同调度层：

```text
TaskGraph scheduler
  - max_parallel_nodes
  - controls how many ready nodes run at once

Node executor / AgentCore loop
  - max_parallel_tools
  - controls tool calls inside one ReAct/model/skill node when that node executor allows multiple tool calls
```

第一版 graph runtime 可以继续使用 `max_parallel_nodes=1`，保持单进程顺序 dispatch。实现配置时建议放在 `execution.max_parallel_nodes`，默认 `1`。不要复用 `max_parallel_tools`，否则一个配置会同时改变 graph-level node scheduling 和 node-local tool execution，恢复、trace 和性能问题会难以定位。

Atomic `tool` node 不使用 `max_parallel_tools`。它只执行一个真实工具调用；是否和其他 node 并行由 `max_parallel_nodes` 决定。`max_parallel_tools` 仅作为 AgentCore/ReAct loop 型 node executor 的内部并发配置保留，例如未来某些 `model` node 或 adapted/legacy `skill` node 内部允许模型一次产生多个 tool calls 时才会生效。

Recovery 要记录和尊重 node-level 并发状态：

- scheduler 选择 ready nodes 时最多取 `max_parallel_nodes` 个。
- node 标记 running 后必须保存 session，再执行 node。
- crash recovery 看到 running node 时，按本阶段 recovery 规则转回 pending/blocked，不依赖 `max_parallel_tools`。
- `max_parallel_tools` 只影响同一个 node 内部工具执行，不影响哪些 node 可恢复或可 ready。

## Recovery 规则

恢复时：

- completed node 不重跑。
- running node 如无 durable in-flight marker，按 retry policy 变为 pending/blocked，由 scheduler 决定。
- awaiting input node 等用户输入。
- blocked/failed node 只有明确 resume/retry 才继续。
- completed graph 不重新激活，只能作为 context refs。

## Recovery Helper 规则

建议新增 deterministic helper，例如：

```go
type RecoveryDecision struct {
    Action  string // continue_graph | resume_node | wait_input | blocked | completed_reference
    TaskID  string
    GraphID string
    NodeID  string
    Reason  string
}
```

该 helper 只能读取 session graph state，不读 raw transcript 猜测状态。

处理规则：

- `completed` node 永远不进入 ready list。
- `running` node 如果没有 durable in-flight marker，恢复为 `pending` 或 `blocked`，并记录 reason。
- `awaiting_input` node 返回 `wait_input`，用户回复进入该 node。
- `blocked/failed` node 默认不自动重试，只有 continuation decision 明确 `resume_node` 时才恢复。
- completed graph 被引用时，只作为 context refs，不修改旧 graph trace refs。

## Trace 兼容规则

- 旧 trace summary 必须继续可读。
- 新 graph/node 事件要在 summary 中可识别。
- trace event 不能写 raw secret、token、cookie、私钥。
- `graph_id`、`node_id` 缺失时，node 级事件应在测试中失败。

## 实现 TODO

- [ ] trace identity 增加 graph/node 上下文 helper。
- [ ] session 保存 graph state 的每次 status 变化。
- [ ] node execute 前后都有持久化保存点。
- [ ] `ContinuationDecision` 使用 graph/node state，而不是只看 `ActiveTask`。
- [ ] recovery helper 根据 graph state 返回可继续节点或 blocker。
- [ ] trace summary 命令兼容旧 trace，同时能显示 graph/node 事件。
- [ ] recovery 处理 running node crash 后的 pending/blocked 转换。
- [ ] completed graph context refs 不写回旧 task。

## 测试 TODO

- [ ] node completed 后恢复不会重跑。
- [ ] running node crash 后不会被误认为 completed。
- [ ] blocked node 恢复后等待明确 resume。
- [ ] awaiting input 恢复后用户回复进入同 node。
- [ ] completed graph 被引用时不更新旧 graph trace refs。
- [ ] trace event 包含 graph_id/node_id。
- [ ] 旧 trace summary 仍可读。
- [ ] session 文件 round-trip 保留 graph/node verified_at/evidence refs。
- [ ] recovery helper 不读取 transcript 也能做出决策。

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
