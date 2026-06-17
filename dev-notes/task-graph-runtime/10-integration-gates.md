# 10 集成闸门与端到端骨架

## 目的

这个文档不是第十阶段实现任务，而是所有阶段都必须对照的集成闸门。它用于防止“每个阶段单独 review 都通过，但最后端到端链路缺胶水”的问题。

OpenCode 每开发一个阶段，都要同时读：

1. `00-architecture-overview.md`
2. `10-integration-gates.md`
3. 当前阶段文档

Codex review 每个阶段时，也必须同时检查当前阶段验收标准和本文件对应闸门。

## 核心原则

- 阶段文档定义局部功能。
- 本文件定义跨组件接口和端到端骨架。
- 每个阶段必须让主链路更可运行，而不是只增加孤立函数或孤立单测。
- 如果某阶段需要后续阶段才能完整运行，必须提供清晰 stub、adapter 或 TODO 标记，并在测试中证明接口已经接上。
- 不为了测试通过保留旧 runtime 兼容层；旧语义可以删除或改写测试。

## 最小主链路

最终主链路必须能这样走通：

```text
用户消息
  -> runtime 接收并创建/选择 task
  -> Planner 一次生成 TaskGraphPlan
  -> plan adapter 持久化 session.TaskGraph 和 task acceptance
  -> Scheduler 计算 ready nodes
  -> Node Executor 执行 node
  -> Node Verifier 更新 node acceptance
  -> Graph/Task Verifier 判断 task acceptance
  -> Finalizer 输出 final reply 或 blocker
  -> Memory Observe 接收 GraphMemorySummary
```

## 跨组件接口契约

### Planner -> Session / TaskGraph

Planner 产物必须能落到 session：

- task id
- graph id
- task goal
- task acceptance
- nodes
- depends
- mode/type
- allowed tools
- skill refs
- human gates
- expected outputs

不能只在 planner 测试里验证 JSON parse 成功。

### Session / TaskGraph -> Scheduler

Scheduler 必须只依赖 graph state：

- `pending` node 等待 depends
- `ready` node 可调度
- `completed + verified` node 不重跑
- `failed/blocked/awaiting_input` node 不被误调度

Scheduler 不应依赖 transcript 猜测任务进度。

### Scheduler -> Node Executor

Executor 输入必须包含：

- task id
- graph id
- node id
- attempt
- node goal
- node mode/type
- dependency outputs/summaries
- allowed tools
- skill refs
- acceptance

Executor 不能重新规划整个任务。

### Node Executor -> Trace / Session

Executor 必须写回：

- node status
- attempts
- result summary
- structured outputs
- evidence refs
- failure reason
- trace events with `task_id`、`graph_id`、`node_id`、`attempt`

工具调用只作为 node 内 evidence，不默认成为 graph node。

### Node Verifier -> Retry / Replan

Verifier 输出必须能驱动：

- node completed
- node retry with feedback
- node failed/blocked
- local replan request

Verifier 不能只返回自然语言说明而没有机器可读状态。

### Graph/Task Verifier -> Finalizer

Finalizer 只能基于 verified node results 和 task acceptance 收口：

- 全部满足：final reply
- 不满足但可修复：repair/synthesis node 或 local replan
- 需要用户：pending human node/blocker
- 无法继续：concrete blocker

不能因为模型生成了一段 final text 就跳过 task acceptance。

### Finalizer -> Memory Observe

任务结束时必须构造 `GraphMemorySummary`：

- task id / graph id
- task goal / status / final text
- node timeline
- attempts
- failed/retried nodes
- result summaries
- evidence refs
- selected skills

Memory 不读取 raw trace dump 作为长期事实。

### Task Lineage

从历史继续必须区分：

- 未完成任务：恢复原 graph state
- 已完成任务：fork 新 task

Fork 需要记录：

- `ParentID`
- `ForkedFromNodeID`，可选
- `ContextRefs`

Session 不做 tree。

## 阶段闸门

### 01 Planner 与计划 Schema

必须证明：

- runtime 入口能调用 Planner 或 planner adapter
- Planner output 能落到 `session.TaskGraph`
- task acceptance 能持久化
- trace 有 planner output/validated 事件
- 简单问答和复杂任务的 plan mode/type 不同

允许 stub：

- Node Executor 可以先不真实执行。
- Scheduler 可以只调度第一个 ready node。

不允许：

- 只写 Planner parse 单测。
- 只保留旧 `TaskContract` 作为真正规划结果。

### 02 Node 模型与状态机

必须证明：

- session graph node 能保存 mode、attempts、result、evidence、acceptance。
- completed verified node 恢复后不重跑。
- running/awaiting_input/failed 的恢复语义有测试。

允许 stub：

- Executor 可用 fake node result。

### 03 Node Executor 与局部 ReAct

必须证明：

- scheduler 能把 ready node 交给 executor。
- executor 写回 node result/evidence/session/trace。
- direct/react/skill/human 至少有可测试路由。

允许 stub：

- react 可以先用受控 fake model/tool。

### 04 Verifier、Retry 与 Local Replan

必须证明：

- verifier result 是机器可读状态。
- verifier failure 能触发 retry。
- attempts exhausted 能触发 failed/blocker 或 local replan request。
- completed upstream nodes 保留。

允许 stub：

- local replan 的 planner 可以先用 fake plan。

### 05 Trace、Session 与恢复

必须证明：

- trace 关键事件都有 `task_id`、`graph_id`、`node_id`、`attempt`。
- 重启/恢复从 graph state 继续。
- TUI/CLI 至少能显示 graph/node 状态，不再只依赖 contract/steps。
- Task Lineage 字段不会污染 Session tree。

### 06 Skill Metadata 与注册

必须证明：

- Planner discovery 只看到 registered skill metadata。
- Executor 选中 skill node 后才读取 `SKILL.md`。
- 缺 metadata 的 skill 不可执行。
- agent-scoped skill 覆盖 shared skill。

### 07 Memory 与 Heartbeat Distill

必须证明：

- task completion 会生成 GraphMemorySummary。
- summary 包含 failed/retried nodes、attempts、evidence refs、skill usage。
- heartbeat/offline distill 不依赖 raw trace dump。
- Memory Tree/Graph 只是未来索引方向，不影响当前恢复路径。

### 08 Scheduler 并行与 Dogfood

必须证明：

- ready nodes 可按 `max_parallel_nodes` 并发。
- dependent nodes 等待。
- 并发调度写 trace。
- 真实模型 dogfood 覆盖 simple/direct、react、skill、human、retry、replan、recovery、memory、task lineage。

### 09 Agent Node 未来方向

必须证明：

- 当前实现没有引入 distributed supervisor。
- 文档只预留 local agent node role。
- 当前主链路不依赖 agent node。

## Review Checklist

Codex review 每阶段至少检查：

- 当前阶段 TODO 是否完成。
- 本文件对应阶段闸门是否完成。
- 是否新增孤立代码但没有接入 runtime 主链路。
- 是否为了测试通过保留旧兼容路径。
- trace/session/memory 三层是否混用。
- node 是否被错误降级成 tool call。
- Session 是否被错误做成 tree。
- 失败是否产生 concrete blocker。

## 最小端到端测试要求

每完成一个阶段，至少维护一个“主链路骨架测试”。这个测试可以使用 fake model/tool，但必须从 runtime 入口进入，而不是直接调用内部 helper。

推荐测试形态：

```text
runtime.Handle(message)
  -> creates task
  -> creates/persists graph
  -> schedules or stubs ready node
  -> records trace
  -> updates session
  -> returns final/pending/blocker
```

阶段越往后，这个测试里的 stub 应越少。到 08 阶段，必须有真实模型 dogfood 记录。
