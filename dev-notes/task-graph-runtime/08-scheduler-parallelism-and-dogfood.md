# 08 Scheduler 并行与真实模型 Dogfood

## 开发前必须先读

OpenCode 开始本阶段前必须先读：

1. `dev-notes/task-graph-runtime/00-architecture-overview.md`
2. `dev-notes/task-graph-runtime/10-integration-gates.md`
3. `dev-notes/task-graph-runtime/02-node-model-and-state-machine.md`
4. `dev-notes/task-graph-runtime/03-node-executor-local-react.md`
5. `dev-notes/task-graph-runtime/04-verifier-retry-and-local-replan.md`
6. `dev-notes/task-graph-runtime/05-trace-session-recovery.md`
7. `dev-notes/task-graph-runtime/06-skill-metadata-and-registration.md`
8. `dev-notes/task-graph-runtime/07-memory-and-heartbeat-distill.md`
9. 本文档
10. 当前相关源码：
   - `internal/runtime/scheduler.go`
   - `internal/runtime/runtime.go`
   - `internal/config/*`
   - `internal/session/graph.go`
   - dogfood/e2e 测试目录或脚本

本阶段是端到端集成压力测试阶段。不要只加并发 helper；必须用真实模型 dogfood 检查主链路是否能完成实际任务。

## 阶段目标

实现本地 ready-node 并行，并通过真实模型 dogfood 验证：

```text
Planner
  -> TaskGraph
  -> Scheduler
  -> Node Executor
  -> Verifier/Retry/Replan
  -> Finalizer
  -> Memory Observe
  -> Recovery
```

并行是 local-first runtime 内部能力，不是分布式调度系统。

## 当前代码基线

前面阶段应已完成：

- Planner schema 与 session graph。
- Node 状态机与恢复。
- Node executor direct/react/skill/human 路由。
- Verifier retry/local replan。
- Trace/session recovery。
- Skill metadata registration。
- GraphMemorySummary observe。

本阶段要补齐：

- `max_parallel_nodes` 配置。
- Scheduler 本地并发执行 independent ready nodes。
- high-risk/human/mutation node 的并行保护。
- parallel trace。
- 真实模型 dogfood checklist 和记录。

## `max_parallel_nodes` 与 `max_parallel_tools`

必须区分：

- `max_parallel_nodes`：TaskGraph Scheduler 同时执行多少个 ready nodes。
- `max_parallel_tools`：单个 node-local AgentCore loop 内部工具并发，若当前 AgentCore 支持。

因为 node 是子任务，不是工具调用，所以这两个配置不是一个东西。

当前默认：

```text
max_parallel_nodes = 1，保守默认，保持单节点推进语义
max_parallel_tools = 保持现有语义
```

如果没有明确需求，不要开启 node 内部工具并发。

## 并行调度规则

Ready 条件：

- 所有 depends 都是 `completed verified` 或明确 `skipped`。
- 当前 node 状态是 pending/ready。
- node 未 blocked、failed、awaiting_input、running、verifying。

并行限制：

- 同时 running nodes 不超过 `max_parallel_nodes`。
- 同一 graph 内 node id 不重复执行。
- completed verified node 不重跑。
- failed/blocked/awaiting_input 不调度。

Mutation/human 风险：

- 高风险 mutation node 默认不与其他 mutation node 并行。
- human_confirm/human_review node 会暂停相关路径。
- 对同一 artifact/path/resource 有写冲突的 node 不应并行；第一版可以保守串行或要求 planner 标出 risk/resource。

## Trace 契约

并行相关事件：

```text
scheduler_tick
node_ready
node_scheduled
node_started
node_completed
node_blocked
scheduler_waiting
```

事件必须带 task_id、graph_id；node 事件带 node_id/attempt。

Trace 应能看出：

- 哪些 node 同时 ready。
- 哪些 node 因 max_parallel_nodes 等待。
- 哪些 node 因 depends 未满足等待。
- 哪些 node 因 human/risk blocked。

## Dogfood 契约

真实模型 dogfood 不一定全部写成自动化单测，但必须形成可复现记录。建议记录在：

```text
dev-notes/task-graph-runtime/dogfood-YYYY-MM-DD.md
```

完整端到端检查顺序见：

```text
dev-notes/task-graph-runtime/11-end-to-end-test-checklist.md
```

后续真实模型测试应优先按 11 文档逐阶段验收：先检查 Planner 是否生成正确 plan，再检查 Graph/Session、Scheduler、Executor、Verifier、Finalizer、Memory/Recovery，而不是只看最终回答。

Dogfood 场景必须覆盖：

1. 简单问答：一个 `direct` node。
2. 仓库分析：一个或多个 `react` node，node 内有工具调用。
3. 文件/脚本任务：不是爆炸成 tool-node chain，而是合理 subtask node。
4. 写入前 human confirm：human node 或 high-risk gate。
5. verifier failure -> retry：用受控 prompt 或 fake verifier 触发。
6. attempts exhausted -> local replan/blocker。
7. running node crash/recovery。
8. registered skill node。
9. memory observe with GraphMemorySummary。
10. 从历史 task/node/evidence fork 新任务。

每个 dogfood 记录至少包含：

```text
user prompt
expected graph shape
actual graph nodes
key trace events
session recovery state, if relevant
memory summary, if relevant
result
issues/follow-ups
```

## 本阶段必须完成

### TODO 1：配置 `max_parallel_nodes`

可能涉及文件：

- `internal/config/*`
- `internal/runtime/runtime.go`
- docs/README config 相关处，如配置对用户可见

要求：

- 增加配置项。
- 默认保守。
- 配置解析失败有明确错误。
- 不复用 `max_parallel_tools`。

测试：

- default config。
- custom max_parallel_nodes。
- invalid value rejected。

### TODO 2：实现本地 ready-node 并发

可能涉及文件：

- `internal/runtime/scheduler.go`
- `internal/runtime/runtime.go`

要求：

- 一次 scheduler tick 可启动多个 independent ready nodes。
- 受 `max_parallel_nodes` 限制。
- dependency order 正确。
- 状态更新要先持久化，避免重复启动。

测试：

- A/B independent，max=2，两者都能启动。
- A -> B dependency，B 等 A completed。
- max=1 时 independent nodes 串行。
- completed verified node 不重跑。

### TODO 3：并行安全保护

要求：

- human node 不被并行自动越过。
- high-risk mutation node 默认保守串行或 blocked/human gate。
- 同资源写入冲突如果无法判断，保守不并行。

测试：

- mutation/human node 不与另一个 mutation node 并行执行。

### TODO 4：parallel trace coverage

要求：

- scheduler tick 和 node schedule 事件能解释为什么某 node 被执行/等待。

测试：

- trace 包含 node_ready/node_scheduled/scheduler_waiting。

### TODO 5：真实模型 dogfood 文档

要求：

- 新增 dogfood 记录文件。
- 逐项记录 10 个场景，允许标记 blocker。
- blocker 必须具体到文件/trace/event/行为，不写“模型不稳定”这种空话。

## 主链路接入要求

完成本阶段后，必须能用真实模型证明：

```text
简单任务 -> direct node -> final -> memory
复杂任务 -> react node/tool evidence -> verifier -> final
失败任务 -> retry/replan/blocker
恢复任务 -> graph state continue
skill 任务 -> metadata discovery -> skill node
```

## 禁止事项

- 不做 distributed scheduler。
- 不做 worker queue platform。
- 不做 multi-tenant scheduler。
- 不为了并行引入全局锁大改架构。
- 不把 `max_parallel_tools` 改名当 `max_parallel_nodes`。
- 不静默并行高风险写操作。

## 验收标准

- `max_parallel_nodes` 生效。
- independent ready nodes 可在限制内并发。
- dependency order 正确。
- high-risk/human gates 不被并行绕过。
- trace 能解释调度行为。
- 真实模型 dogfood 文档覆盖 10 个场景。
- `go test ./internal/runtime ./internal/session` 通过；如改 config，跑 config 包测试。

## 集成闸门检查

对照 `10-integration-gates.md`，本阶段必须满足：

- Scheduler 只依赖 graph state。
- completed verified node 不重跑。
- blocked/awaiting_input 不误调度。
- finalizer/memory/recovery 在 dogfood 中被验证。
- 真实模型测试不只是 unit fake。

## 交给 OpenCode 的提示词模板

```md
请先读取并遵守根目录 `AGENTS.md`，然后读取：

- dev-notes/task-graph-runtime/00-architecture-overview.md
- dev-notes/task-graph-runtime/10-integration-gates.md
- dev-notes/task-graph-runtime/08-scheduler-parallelism-and-dogfood.md

只实现 Phase 08。

TODO checklist:
- [ ] 增加 `max_parallel_nodes` 配置，并明确区别于 `max_parallel_tools`。
- [ ] 实现本地 ready-node parallel scheduling，必须遵守 depends 和 max limit。
- [ ] 为 human/high-risk mutation/resource-conflict nodes 增加保守安全闸门。
- [ ] 增加 scheduler trace coverage，覆盖 ready/scheduled/waiting nodes。
- [ ] 创建真实模型 dogfood 记录，覆盖本文档要求的 10 个场景。

必须包含 independent parallel nodes、dependency order、max limit、completed skip、high-risk/human gate behavior 的测试。
不要做 distributed scheduling、worker queues 或 multi-agent orchestration。
```

## 当前实现状态

截至 2026-06-17：

- 已增加 `execution.max_parallel_nodes`，默认值为 `1`。
- 已保持 `max_parallel_tools` 原语义；它只代表 node-local AgentCore loop 内部工具并发能力，不代表 graph node 调度。
- Runtime scheduler 已从固定 `ReadyNodes(g, 1)` 改为：
  - 先获取完整 ready node 列表；
  - 按 `max_parallel_nodes` 选择本 tick 的 selected batch；
  - 对未选 node 写入 `scheduler_waiting` 和等待原因；
  - 对 selected node 先统一标记 `running` 并持久化，避免恢复时重复启动。
- 第一版采用本地 goroutine 并行，不引入 worker pool 或分布式调度。每个 selected node 在独立的 session/graph sandbox 中执行，完成后由主 goroutine 按 node 顺序合并 node result、messages、usage、pending action 和 task step 增量。
- `traceRecorder` 已加锁，允许并行 node 写入同一个 trace 文件。
- 已加入 high-risk/human/mutation 保守批次保护：
  - `human_review` / `human_confirm` / `mode=human` 视为 parallel-sensitive；
  - `Input.risk`、`Input.mutation`、`Input.human_gate`、`Input.requires_human_confirmation` 命中高风险值时视为 parallel-sensitive；
  - sensitive node 在一个 tick 中独占批次。
- 已新增 trace 事件：
  - `scheduler_tick`
  - `node_ready`
  - `scheduler_waiting`
  - `node_completed`
- 自动化测试覆盖：
  - 默认 `max_parallel_nodes=1`；
  - 自定义 `max_parallel_nodes`；
  - invalid 值回落；
  - independent ready nodes 在配置为 2 时同 tick selected；
  - independent ready nodes 会真实并发进入模型调用；
  - max limit waiting reason；
  - sensitive node 独占批次；
  - required scheduler/node trace events。
- `go test -race ./internal/runtime` 已覆盖 node 并行路径。

仍需真实模型 dogfood：

- 已创建 dogfood 记录模板：`dev-notes/task-graph-runtime/dogfood-2026-06-17.md`。
- 真实模型运行需要可用模型配置和本地运行环境；若运行失败，必须在 dogfood 文档里记录具体 blocker。
