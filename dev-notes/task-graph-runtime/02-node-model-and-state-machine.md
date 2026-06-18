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

#### TODO 5.1：清理 runtime 测试中的 legacy AgentCore loop 假设

已经完成：

- 删除所有直接调用 `skipLegacyAgentLoopTest(t)` 的旧 runtime 测试。
- 删除 `skipLegacyAgentLoopTest` helper 本身。
- 删除范围包括：
  - `internal/runtime/runtime_test.go`
  - `internal/runtime/delivery_regression_test.go`
  - `internal/runtime/contract_strategy_test.go`
  - `internal/runtime/context_budget_test.go`
  - `internal/runtime/redact_test.go`
- 这些测试覆盖的是旧的“全局 AgentCore loop + checklist/tool chain”语义；保留 skipped 测试会误导后续开发和 review。
- 新行为必须通过 TaskGraph/node-local executor 的测试重新建立，不再保留旧 skipped 回归基线。

#### TODO 5.2：planner failure 不走旧 contract fallback

已经完成：

- `internal/runtime/graph_bootstrap.go` 在 unified planner 失败时返回 concrete error，不再调用 `ensureTaskContract`，不再构造 fallback graph。
- 删除 `fallbackGraphFromContract` / `fallbackNodesFromContract` 和对应 `graph_bootstrap_test.go`。
- 保留并通过已有守卫测试：
  - `TestRuntimeHandle_PlannerFailureDoesNotUseContractFallback`
  - `TestHandle_PlannerFailureFallsBackToModelGraph`
- 主线原则：旧 `TaskContract` 只能作为 planner 输出的 acceptance/compat bridge，不是 planner failure fallback。

#### TODO 5.3：planner 单元测试不再依赖“并行 tool 链”假设

需要审计的测试：

- `internal/runtime/graph_planner_test.go::TestConvertPlannerOutput_ToolNodes`：当前断言 planner 能产出 1 个 tool node + 1 个 model node 的两节点图。这条仍然有效（tool mode 是支持的确定性特例），但测试名要让人一眼看出“tool 节点是特例，不是默认形态”。建议在测试注释里写明：
  - 这是 `type=tool/mode=tool` 的特例；
  - 默认形态是 `type=subtask/mode=react`，tool 调用发生在 node 内部，落到 `EvidenceRefs`。
- `internal/runtime/graph_planner_test.go::TestConvertPlannerOutput_*Human*`：human_confirm / human_review 节点继续是 `type=human_*/mode=human`，不混入 tool 节点。
- `internal/runtime/graph_planner_test.go::TestPlanGraphWithModel_SimpleQA`：单一 model 节点是默认最小形态，1 个节点，不拆成多个 tool 节点。

子任务：

- 给 `TestConvertPlannerOutput_ToolNodes` 加注释，明确这是“特例 tool 节点”测试。
- 检查其它 `TestConvertPlannerOutput_*` 测试中是否还有 “n tool nodes 链式串联” 形态；如果有，拆成“n 个 subtask/react node 串联 + tool evidence 写在各自 node 的 EvidenceRefs”。
- 不要为了“让旧测试通过”而引入 parallel tool-node chain 的兼容层。

#### TODO 5.4：executor / verifier 的 “ToolNode_” 测试范围

已经存在且需要保留语义（这些不是“旧语义”，而是 `type=tool/mode=tool` 特例的执行 + 验收测试）：

- `internal/runtime/node_executor_test.go::TestExecuteNode_ToolNode_*`（Success / EvidenceIncludesStructuredFields / UnknownTool / EmptyExecutor / FailingTool / IncrementsAttempts / WithTrace_DoesNotCrash / ObserveCreatesStep / EvidenceHasElapsed / CriteriaUnmet_Blocked / NoCriteria_Completes）：这些测试 node 都是 `Type: NodeTypeTool, Executor: <tool>`，只测单 node + 单 tool 调用的执行和 evidence 写入，是 tool-mode 特例的正确测试，必须保留。
- `internal/session/node_verifier_test.go::TestVerifyNode_ToolNode_*`：同上，测 verifier 对 tool node 的 evidence 验收。

子任务：

- 确认这批测试没有任何一个断言“多 tool 节点 = 1 个任务的全部节点”；如果有，标记改写。
- 在 `node_executor_test.go` 顶部加一段注释，把 “ToolNode_*” 系列与 “react node 内含 tool evidence” 系列区分清楚，避免后续 reviewer 把特例当成通用路径。

#### TODO 5.5：scheduler / recovery 测试不再以 tool 节点链为 ready 计算输入

需要审计：

- `internal/session/scheduler_test.go` 里的多 node 用例：是否还有“把每个 tool 调用当独立 node、靠 depends 形成链”的写法。如果有，改为：
  - 链的中间节点改为 `type=subtask/mode=react` 或 `type=model/mode=react`；
  - tool 调用体现在下游 node 的 `Inputs` 或 `EvidenceRefs` 里。
- `internal/session/graph_recovery_test.go`：已经在 TODO 4 中覆盖了 completed verified / running / verifying / retrying / awaiting_input / failed / blocked 的恢复语义；继续保留 tool / model / human 三种 type 混用的 fixture，但不允许出现“链式 tool 节点”作为 fixture 主体。

子任务：

- 走查 `scheduler_test.go` 的 multi-node fixture，把“链式 tool 节点”改写为“链式 react/node 节点 + tool evidence”。
- 走查 `graph_recovery_test.go` 的 multi-node fixture，保证恢复语义测试不依赖“多 tool 节点 = 完整任务”这种旧假设。

## 实现状态（截至当前 commit）

下表是 `fix/doc-review-corrections` 分支当前实际落地情况，用于让 reviewer 知道哪些 TODO 已可用、哪些仍是设计意图。

| TODO | 状态 | 主要落点 |
|------|------|---------|
| TODO 1 graph node 持久化字段 | 已完成 | `internal/session/graph.go` 新增 `NodeStatusVerifying` / `NodeStatusRetrying`、`NodeModeTool` / `NodeModeScript` / `NodeModeHuman`；`TaskGraphNode` 已能序列化和反序列化 `type` / `mode` / `attempts` / `result_summary` / `evidence_refs` / `failure_reason` / `acceptance.verified` / `verified_at`。 |
| TODO 2 type/mode validation | 已完成 | `internal/session/graph_validator.go::validateNodeFields` 新增 `IsValidTypeModeCombo` 校验；`internal/session/graph_test.go` 新增 `TestValidateTaskGraph_ValidTypeModeCombos` / `TestValidateTaskGraph_InvalidTypeModeCombos` / `TestValidateTaskGraph_UnknownMode`。 |
| TODO 3 状态流转 helper | 已完成 | `internal/session/graph.go` 新增 `TransitionTo` / `SetCompleted` / `SetFailed` / `SetBlocked` / `IsTerminal` / `IsActive`；`internal/session/graph_test.go` 新增 `TestTransitionTo_RunningIncrementsAttempts` / `TestTransitionTo_VerifyingKeepsAttempts` / `TestSetCompleted_Verified` / `TestSetCompleted_Unverified` / `TestSetFailed`。 |
| TODO 4 recovery normalize | 已完成 | `internal/session/graph_recovery.go::RecoverRunningNodes` 扩展为：running / retrying → pending、verifying 保持 verifying 且不丢 result/evidence、completed 且未 verified → verifying、completed 且 verified → 保持 completed、awaiting_input → 保持、failed / blocked → 不变。`internal/session/graph_recovery_test.go` 新增 `TestRecoverRunningNodes_VerifyingStaysVerifying` / `TestRecoverRunningNodes_RetryingBecomesPending` / `TestRecoverRunningNodes_CompletedUnverifiedBecomesVerifying` / `TestRecoverRunningNodes_CompletedVerifiedStaysCompleted` / `TestRecoverRunningNodes_AwaitingInputPreserved` / `TestRecoverRunningNodes_FailedBlockedNotChanged`。`internal/session/scheduler.go::UpdateGraphStatus` 把 verifying / retrying 计入 active 集合，避免 ready 误判。 |
| TODO 5 清理 tool-node 旧语义 | 已完成本阶段范围 | 已删除所有 `skipLegacyAgentLoopTest(t)` 旧测试和 helper；planner failure 不再走旧 contract fallback；skill node 测试改为 `type=skill/mode=skill`；scheduler 依赖改为 `completed + verified` 才解锁；保留 `ToolNode_*` 作为 `type=tool/mode=tool` 确定性特例测试。 |

代码基线命令：

```bash
go test ./internal/session/ ./internal/runtime/
```

当前结果（在本分支上）：

- `go test ./internal/session ./internal/runtime`：通过。
- `go test ./...`：通过。

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

只实现 Phase 02 当前未完成的部分（参考“实现状态”表）。

TODO checklist:
- [x] 审计并补齐 graph node 的持久化字段：type/mode/attempts/result/evidence/failure/acceptance。已落 `internal/session/graph.go` 与对应 round-trip 测试。
- [x] 增加或收紧 type/mode validation，并为合法、非法组合补测试。已落 `internal/session/graph_validator.go` + `TestValidateTaskGraph_ValidTypeModeCombos` / `InvalidTypeModeCombos` / `UnknownMode`。
- [x] 集中状态流转语义，避免 scheduler/recovery/verifier 各自手写冲突状态。已落 `TransitionTo` / `SetCompleted` / `SetFailed` / `SetBlocked` / `IsTerminal` / `IsActive`，并同步 `UpdateGraphStatus`。
- [x] 增加 recovery normalization 测试，覆盖 completed verified、running、verifying、retrying、awaiting_input、failed、blocked。已落 `internal/session/graph_recovery_test.go` 6 个新用例。
- [x] TODO 5.1：清理所有 `skipLegacyAgentLoopTest(t)` 旧测试和 helper，不留 skipped 残骸。
- [ ] TODO 5.3：给 `TestConvertPlannerOutput_ToolNodes` 加注释说明这是 `type=tool/mode=tool` 特例，并把其它把“链式 tool 节点”当完整任务的 planner 单元测试改写为“链式 react/node 节点 + tool evidence”。
- [ ] TODO 5.5：审计 `scheduler_test.go` 和 `graph_recovery_test.go` 的 multi-node fixture，把“链式 tool 节点”改写为“链式 react/node 节点 + tool evidence”，保证 ready 计算和恢复语义不再依赖旧假设。
- [x] planner failure 不再走旧 contract fallback；已删除 fallback graph helper 和测试，保留 planner failure 不 attach graph 的守卫测试。
- [ ] 在 `node_executor_test.go` 顶部加注释，区分 “ToolNode_* 特例” 与 “react node 内部 tool evidence” 两类测试。

必须包含 `internal/session` 和 `internal/runtime` 的测试。
不要实现 Phase 03 executor、Phase 04 local replan、Phase 08 parallelism，也不要增加任何旧语义 fallback。
如果现有字段命名和本文档冲突，先停止并报告，不要自行猜。
CI 基线：`go test ./internal/session ./internal/runtime` 和 `go test ./...` 必须通过。
