# 05 Trace、Session 与恢复

## 开发前必须先读

OpenCode 开始本阶段前必须先读：

1. `dev-notes/task-graph-runtime/00-architecture-overview.md`
2. `dev-notes/task-graph-runtime/10-integration-gates.md`
3. `dev-notes/task-graph-runtime/02-node-model-and-state-machine.md`
4. `dev-notes/task-graph-runtime/03-node-executor-local-react.md`
5. `dev-notes/task-graph-runtime/04-verifier-retry-and-local-replan.md`
6. 本文档
7. 当前相关源码：
   - `internal/session/*`
   - `internal/runtime/trace*.go`
   - `internal/runtime/runtime.go`
   - `internal/runtime/scheduler.go`
   - `internal/gateway/*`
   - CLI/TUI status display 相关文件

本阶段不是做 Memory，也不是做 Git-like store。目标是让 trace、session、recovery 在 graph runtime 主链路里可用、可审计、可恢复。

## 阶段目标

建立清晰边界：

```text
Trace   = append-only 事实链
Session = 当前对话和 graph 恢复快照
Memory  = 任务完成后的蒸馏结果
Task Lineage = 历史任务之间的 fork/continue 关系
```

恢复必须从 session graph state 继续，而不是从 transcript 猜测。

## 当前代码基线

当前代码已有 trace event、session store、graph recovery、continuation decision、memory observe 的基础。前面阶段应已写入 node status/result/evidence/verifier state。

本阶段要补齐：

- graph/node 关键 trace event 是否统一带 required IDs。
- session 是否保存足够的 graph recovery state。
- 崩溃恢复是否从第一个未完成 node 继续。
- awaiting human/pending control 是否能恢复。
- CLI/TUI 是否能显示 graph/node 状态，而不是只显示 contract/checklist。
- Task Lineage 字段是否和 Session tree 边界清楚。

## 当前实现状态

本阶段已完成最小闭环：

- `internal/runtime.runGraphTask` 在执行前调用 `session.RecoverRunningNodes`，然后写入 `graph_recovery_normalized` trace event。
- node 级关键 trace event 通过 `writeNodeEvent` 保证带 `task_id`、`graph_id`、`node_id`、`attempt`。
- session store 会持久化 `TaskGraph`、node 状态、attempts、result summary、evidence refs、acceptance、pending action。
- `running` / `retrying` node 恢复为 `pending`，`verifying` 保留结果和 evidence，`completed + verified` 不重跑。
- `awaiting_input`、`failed`、`blocked` 恢复后不自动调度。
- high-risk / mutation / human-gate node 如果在 `running` 或 `retrying` 中断，恢复为 `awaiting_input`，避免静默重放真实动作。
- TUI task sidebar 优先展示 graph/node 状态；旧 contract 只作为 task acceptance 兼容提示。

本阶段仍不做：

- Session tree / Memory tree / Git-like object store。
- 从历史 completed task 直接改写旧 graph。
- 真正的长期 task browser 索引。
- 自动创建 repair/synthesis node。

## Trace 契约

Graph 相关事件必须带：

```text
task_id
graph_id
event_type
```

Node/attempt 相关事件必须额外带：

```text
node_id
attempt
```

Tool 相关事件必须额外带：

```text
tool_name
evidence_ref or result_ref
```

核心事件名建议稳定为英文，不要使用中文 trace key：

```text
planner_start
planner_output
planner_validated
graph_attached
node_scheduled
node_started
node_tool_call
node_tool_result
node_final_output
node_verifier_start
node_verified
node_retry
node_failed
node_blocked
node_awaiting_input
local_replan_start
local_replan_applied
graph_finalize_start
graph_completed
graph_blocked
graph_failed
memory_observe_start
memory_written
```

事件 payload 必须脱敏。不要把 secret、token、cookie、raw session dump 写入 trace。

## Session 契约

Session 保存恢复快照，不保存长期知识树。

必须能保存：

- 当前 active task id。
- task -> graph id。
- graph nodes 状态、attempts、outputs、result summary、evidence refs、verifier result。
- pending action / human confirm / awaiting input。
- continuation decision 所需的最小上下文。
- Task Lineage refs：`ParentID`、`ForkedFromNodeID`、`ContextRefs`，如果当前代码已有等价字段则复用。

Session 不做：

- Memory Tree。
- Task history browser 的完整索引。
- content-addressed object store。
- trace dump 长期事实库。

## Recovery 契约

恢复流程：

```text
load session
  -> load active/incomplete task graph
  -> normalize transient states
  -> keep completed verified nodes
  -> keep awaiting_input pending actions
  -> keep failed/blocked nodes as non-ready
  -> scheduler computes ready nodes
```

必须覆盖：

- `completed verified` 不重跑。
- `running` 恢复为 pending/retryable。
- `verifying` 保留 result/evidence，可重新 verify。
- `awaiting_input` 保留 pending action，不自动执行。
- `failed/blocked` 不自动变 ready。
- 高风险 mutation node 崩溃时，如果 evidence 不明确，不静默重跑。

## Continuation 与 Fork

用户发来新消息时：

- 未完成任务：优先判断是否继续当前 graph 或回答 pending action。
- 已完成任务：创建新 task，必要时作为 fork。
- 从历史 task/node/evidence 继续：创建新 task，记录 lineage refs，不改写旧 task。

不要把已完成 task 重新打开继续写。历史是事实，继续是 fork。

## CLI/TUI 状态展示

README 里提到的 `mateway chat` / TUI 后续需要微调。这个阶段至少要确保状态来源转向 graph/node：

显示建议：

```text
Task: <goal> <status>
Graph: <graph_id>
Nodes:
  - id goal status attempts
Pending:
  - human confirm / awaiting input
Blocked:
  - concrete reason
```

不要继续只显示 contract checklist，除非作为兼容性的只读历史字段。

## 本阶段必须完成

### TODO 1：审计 trace required IDs

可能涉及文件：

- `internal/runtime/trace*.go`
- `internal/runtime/node_executor.go`
- `internal/runtime/node_verifier.go`
- `internal/runtime/planner*.go`
- `internal/runtime/finalizer*.go`

要求：

- graph/node/attempt 事件都有 task_id、graph_id、node_id、attempt。
- 缺失字段的事件补齐。
- trace event key 统一英文。

测试：

- 运行一个 fake graph，检查 trace events required fields。

### TODO 2：强化 session graph recovery state

可能涉及文件：

- `internal/session/*`
- `internal/runtime/runtime.go`

要求：

- session 持久化恢复所需 graph state。
- 不把 raw transcript 当恢复事实来源。
- 不丢 pending action。

测试：

- 保存 session -> 重新加载 -> graph state 等价。

### TODO 3：实现 crash-style resume 测试

测试至少覆盖：

- running node crash -> reload -> pending/retryable -> 后续可执行。
- completed verified node -> reload -> 不重跑。
- awaiting_input -> reload -> pending action 保留。
- failed/blocked -> reload -> 不调度。

### TODO 4：高风险 mutation recovery 保护

要求：

- 对标记为 high-risk、mutation、human gate 的 node，恢复时如果状态不明确，不能自动再次执行真实动作。
- 进入 awaiting_input/blocked/evidence_check 等明确状态。

测试：

- high-risk running mutation node 恢复后不会直接执行 fake mutation tool。

### TODO 5：CLI/TUI graph status 最小接入

可能涉及文件：

- CLI/TUI status display 文件，先用 `rg "contract|checklist|mateway chat|status"` 查找。

要求：

- 状态显示使用 graph/node status。
- 旧 contract/checklist 只作为历史兼容展示，不作为新主线。

测试：

- 如有 CLI/TUI 单测，更新断言。
- 如没有，至少增加格式化函数测试，避免只靠手测。

### TODO 6：Task Lineage 字段边界

要求：

- 如果 Phase 01/continuation 已有 `ParentID`、`ForkedFromNodeID`、`ContextRefs`，确认不和 session tree 混用。
- 如果没有，预留最小字段或在本文档对应代码 TODO 标明后续接入点。
- 从已完成任务继续时，不改写旧 task。

测试：

- completed task follow-up creates/forks new task or at least records context refs in new task path。

## 主链路接入要求

完成本阶段后，端到端骨架应能做到：

```text
run graph partially
  -> save session and trace
  -> simulate restart
  -> recover graph state
  -> scheduler continues unfinished nodes
  -> finalizer uses recovered results
```

不要求真实断电测试，但必须有 crash-style unit/integration tests。

## 禁止事项

- 不实现 Memory Tree/Graph；属于未来 memory 索引方向。
- 不把 Session 做成 tree。
- 不把 trace dump 当长期 memory。
- 不把 secrets/raw session dump 写入 trace。
- 不为了 TUI 展示重建旧 contract checklist 主线。
- 不新增分布式恢复、worker lease、queue platform。

## 验收标准

- graph/node trace events required IDs 完整。
- session 保存并恢复 graph state。
- completed verified node 恢复后不重跑。
- awaiting human/pending action 恢复后仍等待。
- high-risk mutation 不在模糊恢复时静默重跑。
- CLI/TUI 或 status formatting 能展示 graph/node 状态。
- `go test ./internal/session ./internal/runtime` 通过；若改 CLI/TUI 包，也跑对应包测试。

## 集成闸门检查

对照 `10-integration-gates.md`，本阶段必须满足：

- Trace 关键事件都有 required IDs。
- 恢复从 graph state 继续。
- TUI/CLI 状态不再只依赖 contract/steps。
- Task Lineage 不污染 Session tree。
- Memory 后续只消费 summary/evidence refs，不读 raw trace dump。

## 交给 OpenCode 的提示词模板

```md
请先读取并遵守根目录 `AGENTS.md`，然后读取：

- dev-notes/task-graph-runtime/00-architecture-overview.md
- dev-notes/task-graph-runtime/10-integration-gates.md
- dev-notes/task-graph-runtime/02-node-model-and-state-machine.md
- dev-notes/task-graph-runtime/05-trace-session-recovery.md

只实现 Phase 05。

TODO checklist:
- [ ] 确保 graph/node/attempt trace events 在适用时都带 task_id、graph_id、node_id、attempt。
- [ ] 持久化足够的 session graph state，用于恢复 nodes、outputs、evidence refs、verifier state、pending actions。
- [ ] 增加 crash-style recovery tests，覆盖 running、completed verified、awaiting_input、failed、blocked nodes。
- [ ] 防止状态不明确的 high-risk mutation nodes 在恢复后静默重跑。
- [ ] 更新 CLI/TUI/status display，让 graph/node status 成为主状态来源，而不是 contract/checklist。
- [ ] Task Lineage 只能作为 task refs 保存，不要把 Session 做成 tree。

不要实现 Memory Tree、distributed recovery 或旧语义 fallback。
不要把 secrets 或 raw session dumps 写入 trace。
```
