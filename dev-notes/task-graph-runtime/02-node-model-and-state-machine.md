# 02 Node 模型与状态机

## 开发前必须先读

OpenCode 开始本阶段前必须先读：

1. `dev-notes/task-graph-runtime/00-architecture-overview.md`
2. `dev-notes/task-graph-runtime/10-integration-gates.md`
3. 本文档
4. 当前相关源码：
   - `internal/session/graph.go`
   - `internal/session/graph_validator.go`
   - `internal/session/graph_recovery.go`
   - `internal/runtime/scheduler.go`
   - `internal/runtime/node_verifier.go`
   - `internal/runtime/runtime.go`

不要只新增孤立结构体。这个阶段必须让 Planner 输出落到 session 后，后续 Scheduler、Executor、Verifier、Recovery 都能共享同一套 node 状态语义。

## 阶段目标

把 TaskGraph node 从“计划条目/工具步骤”收敛为最终主线架构里的 **可验收子任务**。

Node 是：

- 调度边界：Scheduler 根据 depends 和 status 判断是否 ready。
- 执行边界：Node Executor 只执行一个 node，不重新规划整个 task。
- 验收边界：Verifier 判断 node output 是否满足 node acceptance。
- 恢复边界：进程中断后从未完成 node 继续，completed verified node 不重跑。
- 记忆边界：任务完成时 memory 能看到 node summary、attempts、evidence refs。

Node 不是：

- 一个工具调用。
- 一个 checklist 文本项。
- 一个全局 ReAct loop 的内部 step。
- 一个历史任务分支节点。历史分支属于 Task Lineage，不属于 Session tree。

## 当前代码基线

Phase 01 已经完成统一 Planner 的主线方向：

- 新任务进入 unified planner。
- Planner 输出 `TaskGraphPlan`，包含 task acceptance 和 graph nodes。
- 旧 `ContractModel` 不再作为主规划入口。
- Planner 可以产出 `subtask` node、mode、depends、allowed tools、required skills 等字段。

本阶段要检查并补齐：

- session graph node 是否能持久化 mode/type/attempt/result/evidence/acceptance。
- validator 是否统一验证 type 和 mode。
- scheduler/recovery 是否按同一状态机处理 node。
- 旧测试是否仍把 node 当成 tool call 或 checklist step，如果是，改写或删除。

## Node 数据契约

允许沿用现有 Go 类型名，但行为上必须能表达下列字段。字段命名可以贴合现有代码，不要求机械照搬。

```text
id               stable node id inside graph
goal             node goal in human-readable text
type             subtask | skill | human_confirm | human_review | tool
mode             direct | react | skill | script | tool | human
depends          upstream node ids
inputs           planner declared inputs or dependency bindings
outputs          structured outputs produced by node
allowed_tools    node-local tool allowlist
skill            selected skill ref, when type/mode requires it
status           node lifecycle status
attempts         node-level attempts, not tool retry count
result_summary   concise result for downstream context and memory
evidence_refs    trace/evidence refs generated inside node
failure_reason   machine-readable or concise textual failure reason
acceptance       node acceptance/verifier requirement
verified         whether node acceptance passed
verified_at      timestamp or equivalent marker
```

### type 与 mode 的含义

`type` 描述 node 的角色，`mode` 描述 executor 怎么执行。

推荐组合：

```text
type=subtask, mode=direct     简单问答、摘要、纯模型生成
type=subtask, mode=react      需要多步工具/模型循环的子任务
type=skill,   mode=skill      已注册 skill node
type=tool,    mode=tool       确定性低成本原子动作，仅作特例
type=tool,    mode=script     确定性脚本/API 特例
type=human_confirm, mode=human  写入/高风险动作前确认
type=human_review,  mode=human  需要用户审阅/选择
```

不要把所有工具调用拆成 `type=tool` graph node。工具调用默认是 node 内部 action/evidence。

## 状态机契约

本阶段必须把代码、文档、测试统一到这套语义。

```text
pending -> ready -> running -> verifying -> completed
running -> awaiting_input
running/verifying -> retrying -> running
running/verifying -> failed
failed -> blocked
failed -> local replan, 由 04 阶段接完整闭环
completed -> recovery skip
```

状态含义：

- `pending`：node 已存在，但依赖未满足，或恢复后等待重新调度。
- `ready`：依赖满足，可以被 Scheduler 交给 Executor。
- `running`：Executor 正在执行 node。
- `verifying`：执行产物已生成，Verifier 正在验收。
- `completed`：node 已通过验收；必须带 verified 标记或等价 verifier result。
- `awaiting_input`：等待用户输入、确认或人工审阅。
- `retrying`：node 级重试即将发生或正在准备。
- `failed`：node 尝试失败，等待 blocker 或 local replan。
- `blocked`：无法自动继续，需要用户、权限、工具或环境变化。
- `skipped`：只用于 planner/replan 明确声明不再需要的 node，不要用它掩盖失败。

## 恢复规则

Recovery 必须只依赖 session graph state，不依赖 transcript 猜测进度。

必须实现或验证：

- `completed + verified`：永不重跑。
- `completed` 但缺 verifier 通过标记：不能当作 verified completed；要进入 verifying 或 blocked。
- `running`：恢复为 `pending` 或 retryable 状态，attempts 保留，不能直接标 completed。
- `verifying`：恢复为 `verifying` 或 retryable verifying，不能丢 result/evidence。
- `awaiting_input`：保留 pending action，不被 Scheduler 自动执行。
- `failed` / `blocked`：不进入 ready 队列。
- `pending` / `ready`：由 Scheduler 根据 depends 重新计算。

高风险 mutation node 如果崩溃时状态不明确，不要静默重跑。应保留 evidence refs，并要求后续阶段通过 evidence check 或 human review 处理。

## Task Lineage 边界

本阶段不要把 Session 做成 tree。

需要表达“从历史任务继续/分支”时，字段属于 task lineage：

```text
ParentID
ForkedFromNodeID
ContextRefs
```

这些字段用于历史任务关系，不参与当前 graph 内部状态机。Session 仍只保存当前对话、active task、pending action 和 graph recovery state。

## 本阶段必须完成

### TODO 1：审计并补齐 node 持久化字段

可能涉及文件：

- `internal/session/graph.go`
- `internal/session/task.go`
- `internal/session/store.go`
- `internal/runtime/runtime.go`

要求：

- session graph node 能保存 `type`、`mode`、`attempts`、`result_summary`、`outputs`、`evidence_refs`、`failure_reason`、`acceptance/verifier result`。
- 如果已有字段语义接近，优先复用，不为了名字漂亮新增重复字段。
- 如果旧字段只服务 checklist/contract step，迁移到 graph node 语义，避免双写两套状态。

测试：

- 新增或更新 `internal/session/*graph*_test.go`，验证 graph 序列化/反序列化不丢上述字段。

### TODO 2：统一 type/mode validation

可能涉及文件：

- `internal/session/graph_validator.go`
- `internal/runtime/planner*.go`

要求：

- Validator 拒绝未知 `type` / `mode`。
- Validator 拒绝明显不合法组合，例如 `type=human_confirm, mode=react`。
- Validator 允许 `subtask/direct`、`subtask/react`、`skill/skill`、`tool/tool`、`tool/script`、`human_confirm/human`、`human_review/human`。
- Validator 对空 mode 应按 Phase 01 的默认策略补齐，或明确报错。不要在多个包里各自猜默认值。

测试：

- 合法组合通过。
- 未知 mode 失败。
- 不合法组合失败。

### TODO 3：统一状态流转 helper

可能涉及文件：

- `internal/session/graph.go`
- `internal/runtime/scheduler.go`
- `internal/runtime/node_executor.go`
- `internal/runtime/node_verifier.go`

要求：

- 尽量提供集中 helper 或方法更新 node 状态，避免各包手写字符串。
- 状态流转时保留 attempts、evidence refs、failure reason。
- completed 时必须能记录 verifier passed。
- failed/blocked 时必须记录原因。

测试：

- completed verified node 不会被 scheduler 选为 ready。
- failed/blocked/awaiting_input 不会被 scheduler 误调度。

### TODO 4：实现 recovery normalize

可能涉及文件：

- `internal/session/graph_recovery.go`
- `internal/runtime/runtime.go`

要求：

- 恢复时 normalize transient 状态。
- `running` 恢复为 pending/retryable。
- `awaiting_input` 保持等待状态。
- `completed verified` 保持 completed。
- `failed/blocked` 保持不可调度。

测试：

- `TestGraphRecoveryCompletedVerifiedSkipped`
- `TestGraphRecoveryRunningBecomesPendingWithAttempts`
- `TestGraphRecoveryAwaitingInputPreserved`
- `TestGraphRecoveryFailedBlockedNotReady`

测试名不要求完全一致，但覆盖点必须有。

### TODO 5：清理 tool-node 旧语义

可能涉及文件：

- `internal/runtime/*test.go`
- `internal/session/*test.go`
- 旧文档或 fixtures

要求：

- 如果测试暗示“一个工具调用就是一个 graph node”，改成“react node 内部产生 tool evidence”。
- 保留 `tool` mode 作为确定性特例，但不要让它成为默认 planner 产物。
- 不要为了旧测试保留 parallel tool-node chain 的兼容层。

## 主链路接入要求

完成本阶段后，最小主链路必须能做到：

```text
Planner output
  -> session.TaskGraph 持久化 node type/mode/status/acceptance
  -> recovery normalize
  -> scheduler 只选择 ready nodes
  -> completed verified nodes skip
```

如果 Executor 还没完整实现，可以用 fake result 测试 session/scheduler/recovery，但接口必须接到主链路，不允许只测纯函数。

## 禁止事项

- 不实现并行调度；并行属于 08。
- 不实现完整 node-local ReAct；属于 03。
- 不实现 local replan 闭环；属于 04。
- 不把 Session 改成 tree 或 Git-like object store。
- 不新增 runtimev2、agentv2、workflowv2 等平行实验包。
- 不为旧 checklist/global loop 语义增加兼容层。

## 验收标准

- Node model 能表达 direct、react、skill、script/tool、human node。
- `type` / `mode` 有统一 validator。
- Node attempts、result summary、outputs、evidence refs、failure reason、verifier result 能持久化。
- Recovery 后 completed verified node 不重跑。
- Recovery 后 running/awaiting_input/failed/blocked 语义正确。
- Scheduler 不把不可调度状态误判为 ready。
- `go test ./internal/session ./internal/runtime` 通过。

## 集成闸门检查

对照 `10-integration-gates.md`，本阶段必须满足：

- Planner -> Session：node type/mode/depends/acceptance 能落 session。
- Session -> Scheduler：Scheduler 只依赖 graph state。
- Node Executor -> Trace/Session：本阶段至少保证 session 字段已准备好。
- Task Lineage：不把 lineage 塞进 Session tree。

## 交给 OpenCode 的提示词模板

```md
请先读取并遵守根目录 `AGENTS.md`，然后读取：

- dev-notes/task-graph-runtime/00-architecture-overview.md
- dev-notes/task-graph-runtime/10-integration-gates.md
- dev-notes/task-graph-runtime/02-node-model-and-state-machine.md

只实现 Phase 02。

TODO checklist:
- [ ] 审计并补齐 graph node 的持久化字段：type/mode/attempts/result/evidence/failure/acceptance。
- [ ] 增加或收紧 type/mode validation，并为合法、非法组合补测试。
- [ ] 集中状态流转语义，避免 scheduler/recovery/verifier 各自手写冲突状态。
- [ ] 增加 recovery normalization 测试，覆盖 completed verified、running、awaiting_input、failed、blocked。
- [ ] 改写或删除“每个 tool call 都是 graph node”的旧测试。

必须包含 `internal/session` 和 `internal/runtime` 的测试。
不要实现 Phase 03 executor、Phase 04 local replan、Phase 08 parallelism，也不要增加任何旧语义 fallback。
如果现有字段命名和本文档冲突，先停止并报告，不要自行猜。
```
