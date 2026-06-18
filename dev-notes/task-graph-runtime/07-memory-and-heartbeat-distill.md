# 07 Memory 与 Heartbeat Distill

## 开发前必须先读

OpenCode 开始本阶段前必须先读：

1. `dev-notes/task-graph-runtime/00-architecture-overview.md`
2. `dev-notes/task-graph-runtime/10-integration-gates.md`
3. `dev-notes/task-graph-runtime/02-node-model-and-state-machine.md`
4. `dev-notes/task-graph-runtime/05-trace-session-recovery.md`
5. 本文档
6. 当前相关源码：
   - `internal/memory/*`
   - `internal/runtime/memory*.go`
   - `internal/runtime/finalizer*.go`
   - `internal/session/graph.go`
   - heartbeat/automation/distill 相关文件

本阶段不是重写 memory 系统，也不是实现完整 Tree Memory。目标是让现有 memory consume graph-aware summary，而不是读 raw trace dump 或只看 final text。

## 阶段目标

任务结束时构造 `GraphMemorySummary`：

```text
task goal/status/final text
graph id
node timeline
node attempts/failures/retries
node result summaries
structured outputs
evidence refs
selected skills
blockers
```

然后交给现有 memory observe：

```text
GraphMemorySummary
  -> diary markdown
  -> learning JSONL
  -> skill usage JSONL
  -> proposals
  -> heartbeat/offline distill
```

Memory 不驱动工具动作，不绕过 policy，不参与实时恢复。

## 当前代码基线

当前 memory 已有 diary、learning JSONL、skill usage、proposal、heartbeat distill。前面阶段应已让 trace/session 保存 node-level result/evidence/verifier state。

本阶段要补齐：

- `GraphMemorySummary` 字段是否覆盖 node 粒度。
- runtime finalizer 是否在任务完成/失败/blocked 时调用 memory observe。
- skill usage 是否关联成功 skill node，而不是“读过 SKILL.md”。
- heartbeat S-R-O 主体-关系-客体整理继续作为离线层。
- Markdown memory 与 JSONL/index 的主从关系明确。

## Memory 边界

### Memory 是什么

- 长期经验和项目事实的蒸馏。
- 用户可读 diary。
- learning events。
- skill usage。
- proposal。
- 未来 Memory Tree/Graph 的输入来源。

### Memory 不是什么

- 当前任务恢复状态。恢复属于 Session/TaskGraph。
- trace dump。Trace 是事实账本，Memory 是蒸馏结果。
- 工具权限系统。工具动作仍由 policy/human confirm 控制。
- 实时 Planner 的硬依赖。Planner 可以读取 memory context，但 memory 不能直接触发工具动作。

## GraphMemorySummary 契约

字段命名可贴合现有代码，但语义必须覆盖：

```text
task_id
graph_id
task_goal
task_status
final_text
created_at/completed_at
nodes:
  node_id
  type
  mode
  goal
  status
  attempts
  result_summary
  structured_outputs
  evidence_refs
  failure_reason
  verifier_status
  selected_skill
failed_nodes
retried_nodes
blocked_nodes
selected_skills
trace_refs
context_refs
```

不要把 raw trace events 或完整 transcript 塞进 summary。

## Diary / JSONL / Markdown Source of Truth

用户担心手动修改 Markdown memory 后与 JSONL 不同步。原则：

- 用户可编辑 Markdown memory 是 source of truth。
- JSONL、embedding、index、S-R-O relation store 都是派生或 append-only 辅助数据。
- 如果 Markdown 被用户修改，后续 heartbeat 可以重新构建派生索引。
- 不要求用户手改 JSONL。

本阶段只记录并保持这个方向，不实现完整 rebuild engine，除非现有代码已有轻量机制可接。

## Heartbeat Distill 契约

Heartbeat/offline distill 继续做：

- diary/learning 整理。
- proposal。
- skill usage consolidation。
- 主体-关系-客体抽取。
- 冲突事实标记。
- 旧事实降权或失效建议。

S-R-O relation 不在 node execution 同步抽取。它使用 GraphMemorySummary、diary、learning 和 evidence refs 离线蒸馏。

## 本阶段必须完成

### TODO 1：补齐 GraphMemorySummary

可能涉及文件：

- `internal/runtime/memory*.go`
- `internal/memory/*`
- `internal/session/graph.go`

要求：

- Summary 包含 task + node 粒度。
- failed/retried/blocked nodes 可识别。
- attempts、failure_reason、verifier result、evidence_refs 可进入 memory observe。

测试：

- 构造 graph with retried/failed node，summary 包含对应节点。

### TODO 2：Finalizer 调用 memory observe

可能涉及文件：

- `internal/runtime/finalizer*.go`
- `internal/runtime/runtime.go`

要求：

- completed task 调用 memory observe。
- failed/blocked task 也可以写 lightweight learning/diary，至少记录 blocker 和 failed node。
- memory observe 失败不能篡改 task completion 状态，但应 trace/report。

测试：

- fake memory observer 接收到 GraphMemorySummary。
- memory observer failure 被记录，不使 completed task 变 failed。

### TODO 3：Skill usage 关联到 skill node result

要求：

- 只有成功执行或有明确结果的 skill node 记录 usage。
- usage 包含 task_id、graph_id、node_id、skill name/id、result summary。
- 不因为 Planner 看见 skill 或 Executor 读取 `SKILL.md` 就记录成功 usage。

测试：

- selected skill node completed -> skill usage written with node id。
- skill discovered but not selected -> no usage。
- skill node failed -> usage 标记 failure 或不记 success。

### TODO 4：保持 diary/learning/proposal 输出兼容

要求：

- 现有 Markdown diary、learning JSONL、skill usage JSONL、proposal 机制不被推翻。
- 新增 graph fields 时保持旧消费者不崩。

测试：

- 现有 memory tests 通过。
- 新 graph-aware summary tests 通过。

### TODO 5：记录 heartbeat S-R-O 离线方向

要求：

- 文档和代码注释/TODO 明确：主体-关系-客体整理在 heartbeat/offline distill。
- Runtime 不同步抽取关系。
- Memory 不直接执行工具动作。

测试：

- 如果已有 heartbeat tests，增加 graph summary input case。
- 如暂无，至少保证 memory observe 不调用 tool/action。

## 主链路接入要求

完成本阶段后：

```text
Graph/Task Finalizer
  -> GraphMemorySummary
  -> memory observe
  -> diary/learning/skill usage/proposal
  -> heartbeat/offline distill 可读取
```

Memory 只消费 summary，不读取 raw trace dump 作为长期事实。

## 禁止事项

- 不实现完整 Tree Memory store。
- 不在 node execution 同步抽取 S-R-O。
- 不让 memory 直接驱动工具动作。
- 不绕过用户确认和 tool policy。
- 不把 JSONL 变成用户必须手动维护的唯一真源。
- 不把 raw transcript/trace dump 写入长期 memory。

## 验收标准

- GraphMemorySummary 包含 task + node 粒度信息。
- retried/failed/blocked nodes 在 memory 中可见。
- skill usage 关联 node result。
- diary/learning/skill usage/proposal 输出保持可用。
- heartbeat/offline distill 方向保留，runtime 不同步抽取关系。
- `go test ./internal/memory ./internal/runtime` 通过。

## 当前实现状态

本阶段已完成最小闭环：

- `session.GraphMemorySummary` 已包含 graph/task 基本信息、node mode/executor/output/evidence/verifier/selected skill，以及 failed/retried/blocked 分类。
- `FinalizeAndRespond` 在 completed/failed/blocked 收口时调用 memory observe，并写入 `memory_observe_start`、`memory_written` trace event。
- memory observe 失败仍通过 hook warning 记录，不改变 task finalization 状态。
- learning JSONL 包含 graph_id、node_records、failed_nodes、retried_nodes、blocked_nodes。
- skill usage JSONL 只基于 graph summary 中的 skill node，包含 skill_node_id、graph_id、node_result，并使用 selected skill/executor 名称。
- 现有 diary、learning JSONL、skill usage JSONL、proposal 机制保持兼容。

本阶段仍不做：

- Tree Memory store。
- runtime 同步抽取主体-关系-客体。
- memory 直接触发工具或绕过 policy。
- Markdown -> JSONL/index 的完整 rebuild engine。

## 集成闸门检查

对照 `10-integration-gates.md`，本阶段必须满足：

- Finalizer -> Memory Observe：summary 包含 task/node/attempt/failure/evidence/skills。
- Memory 不读取 raw trace dump。
- Trace/Session/Memory 边界清楚。
- Markdown memory source of truth，derived indexes 可重建。

## 交给 OpenCode 的提示词模板

```md
请先读取并遵守根目录 `AGENTS.md`，然后读取：

- dev-notes/task-graph-runtime/00-architecture-overview.md
- dev-notes/task-graph-runtime/10-integration-gates.md
- dev-notes/task-graph-runtime/05-trace-session-recovery.md
- dev-notes/task-graph-runtime/07-memory-and-heartbeat-distill.md

只实现 Phase 07。

TODO checklist:
- [ ] 构建或补齐 GraphMemorySummary，包含 task 和 node-level fields。
- [ ] 在 finalizer 中对 completed 以及有意义的 failed/blocked tasks 调用 memory observe。
- [ ] skill usage 必须关联成功的 skill node result，不能因为 discovery 或读取 `SKILL.md` 就记成功。
- [ ] 保持现有 diary、learning JSONL、skill usage JSONL、proposal 格式兼容。
- [ ] S-R-O relation distill 保持在 heartbeat/offline 层，memory 不能运行工具。

必须包含 retried/failed node summary、memory observe callback、skill usage node association、observer failure handling 的测试。
不要实现 Tree Memory store，也不要把 JSONL 变成用户可编辑 source of truth。
```
