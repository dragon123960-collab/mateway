# 04 Verifier、Retry 与 Local Replan

## 开发前必须先读

OpenCode 开始本阶段前必须先读：

1. `dev-notes/task-graph-runtime/00-architecture-overview.md`
2. `dev-notes/task-graph-runtime/10-integration-gates.md`
3. `dev-notes/task-graph-runtime/02-node-model-and-state-machine.md`
4. `dev-notes/task-graph-runtime/03-node-executor-local-react.md`
5. 本文档
6. 当前相关源码：
   - `internal/runtime/node_verifier.go`
   - `internal/runtime/node_executor.go`
   - `internal/runtime/finalizer*.go`
   - `internal/runtime/planner*.go`
   - `internal/session/graph.go`
   - `internal/session/graph_validator.go`

本阶段目标是让“没验收通过”成为可恢复、可重试、可局部修复的状态，而不是直接生成失败文本或重跑整个任务。

## 阶段目标

建立三层闭环：

```text
Node output
  -> Node Verifier
  -> pass: completed
  -> fail but retryable: node retry with feedback
  -> exhausted/capability failure: failed/blocked or local replan request
```

再建立 task-level 收口：

```text
All required nodes completed
  -> Graph/Task Verifier
  -> pass: final reply
  -> fail: repair node or local replan
```

Verifier 是机器可读决策点。自然语言说明只能作为 reason，不足以驱动 runtime。

## 当前代码基线

前面阶段应已具备：

- Planner 输出 task acceptance 和 node acceptance。
- Node 状态、attempts、result、evidence 能持久化。
- Executor 能写回 node result。

本阶段要补齐：

- Node verifier 的统一结果结构。
- Verifier failure 驱动 node-level retry。
- Node retry 和 tool retry 分开。
- attempts exhausted 后进入 failed/blocked/local replan。
- local replan 最小范围替换 failed node 与 downstream pending nodes，保留 completed upstream nodes。
- task final acceptance 不满足时有 repair/replan 路径。

## Verifier 结果契约

Verifier 输出必须机器可读。字段名可按现有代码调整，但至少表达：

```text
status: passed | retry | failed | blocked | replan
reason
missing_requirements
retryable: bool
feedback_for_next_attempt
confidence or source, optional
evidence_refs_used
```

禁止只返回：

```text
"看起来完成了"
"失败了，请重试"
```

这类文本可以写入 reason，但不能是 runtime 唯一判断依据。

## 验证策略

优先 deterministic verifier，再用 model verifier。

Deterministic checks 包括：

- required structured outputs 是否存在。
- artifact path/ref 是否存在且通过基础校验。
- script/tool exit status 是否成功。
- human confirmation 是否已经 granted。
- node status/evidence refs 是否完整。

Model verifier 只用于语义验收，例如：

- 摘要是否覆盖 acceptance。
- 代码 review 是否给出 grounded findings。
- 分析报告是否回答用户目标。

Model verifier prompt 必须限制在 node result、dependency summaries、acceptance、evidence summary，不要默认塞完整 transcript。

## Retry 契约

Node retry 与 tool retry 分开：

- Tool retry：一次 tool call 的超时/临时错误，属于 node 内部。
- Node retry：整个 node attempt 未通过执行或验收，需要带 feedback 重跑 node。

Node retry 规则：

- 每个 node 有最大 attempts。
- 执行失败且 retryable：重试 node。
- Verifier failed 且 retryable：带 verifier feedback 重试 node。
- attempts exhausted：标 failed 或 blocked，或触发 local replan。
- 重试必须记录 attempt summary、failure reason、verifier feedback。

不要无限 retry。不要为了通过测试把 failed 自动改 completed。

## Local Replan 契约

Local replan 第一版只做最小闭环。

输入：

```text
task goal
task acceptance
current graph
failed node id
failed node goal/type/mode
failure reason
attempt summaries
available tools/skills summaries
completed upstream outputs
downstream pending nodes
```

输出：

```text
replacement nodes
depends
acceptance
mapping from old failed/downstream nodes to new nodes, optional
```

硬规则：

- completed verified upstream nodes 不允许修改、不允许重跑。
- 只替换 failed node 和依赖它的 downstream pending nodes。
- blocked/awaiting_input node 不被 local replan 偷偷绕过。
- local replan 次数必须有限制。
- local replan 结果必须过 graph validator。

如果暂时无法完整调用真实 planner，可以先实现 fake/local entrypoint，但主接口必须定好，测试要覆盖 completed upstream preserved。

## Task Acceptance Repair

Graph 中所有关键 node 完成后，还要判断 task acceptance。

如果 task acceptance 不满足：

- 可以创建 synthesis/repair node。
- 可以触发 local replan。
- 可以 blocked，给出 concrete blocker。

不能因为已有 final text 就跳过 task acceptance。

## 本阶段必须完成

### TODO 1：统一 NodeVerifierResult

可能涉及文件：

- `internal/runtime/node_verifier.go`
- `internal/session/graph.go`

要求：

- 增加或收敛 verifier result 结构。
- Verifier result 写回 node。
- Verifier result 能驱动 completed/retry/failed/blocked/replan。

测试：

- deterministic pass。
- deterministic missing output -> retry/failed。
- model verifier JSON pass/fail parse。

### TODO 2：实现 verifier failure -> node retry

可能涉及文件：

- `internal/runtime/node_executor.go`
- `internal/runtime/node_verifier.go`
- `internal/runtime/runtime.go`

要求：

- Node retry 前 attempts 增加。
- 下一次 attempt 能收到 verifier feedback。
- retry trace 写入 `node_retry`，带 reason 和 attempt。

测试：

- fake verifier 第一次 fail retryable，第二次 pass。
- 断言同一 node attempts 为 2，最终 completed verified。

### TODO 3：实现 attempts exhausted 处理

要求：

- retryable failure 超过 max attempts 后，不再执行。
- 根据 failure 类型进入 failed、blocked 或 replan request。
- 写 failure reason。

测试：

- fake verifier 一直 fail，max attempts 后 node failed 或 blocked。
- Scheduler 不再调度该 node。

### TODO 4：实现 local replan 最小入口

可能涉及文件：

- `internal/runtime/planner*.go`
- `internal/runtime/local_replan*.go`
- `internal/session/graph.go`
- `internal/session/graph_validator.go`

要求：

- 提供 local replan 函数或 runtime path。
- 保留 completed upstream nodes。
- 替换 failed node 和 downstream pending nodes。
- 新 graph 通过 validator。
- trace 写 `local_replan_start`、`local_replan_applied` 或失败事件。

测试：

- A completed，B failed，C depends B pending。
- replan 后 A 仍 completed 且不重跑，B/C 被替换或重置为新 pending nodes。

### TODO 5：实现 task acceptance repair path

可能涉及文件：

- `internal/runtime/finalizer*.go`
- `internal/runtime/node_verifier.go`

要求：

- Graph nodes completed 后检查 task acceptance。
- 不满足时创建 repair/synthesis path 或返回 blocker/replan request。
- Finalizer 不直接输出空 final。

测试：

- all nodes completed but task acceptance missing final artifact -> repair/blocker。

## 主链路接入要求

完成本阶段后，必须能走通：

```text
Executor result
  -> Node Verifier
  -> retry with feedback
  -> completed or failed
  -> local replan entrypoint
  -> finalizer checks task acceptance
```

这条链路至少要有 fake model/verifier 的集成测试，不能只测 verifier parse。

## 禁止事项

- 不做无限 replan。
- 不重写 entire graph。
- 不修改 completed verified upstream nodes。
- 不用自然语言 verifier 结果直接驱动状态。
- 不让 finalizer 跳过 task acceptance。
- 不引入 distributed workflow 或 worker queue。

## 验收标准

- Verifier result 是机器可读。
- Verifier failure 会先 node retry。
- Retry feedback 能进入下一次 attempt。
- attempts exhausted 后状态明确。
- local replan 保留 completed upstream nodes。
- task acceptance 不满足时有 repair/blocker/replan，不是静默 final。
- `go test ./internal/runtime ./internal/session` 通过。

## 集成闸门检查

对照 `10-integration-gates.md`，本阶段必须满足：

- Node Verifier -> Retry/Replan：机器可读状态驱动 runtime。
- Graph/Task Verifier -> Finalizer：final reply 基于 task acceptance。
- Completed upstream nodes preserved。
- blocker 具体、可追踪。

## 交给 OpenCode 的提示词模板

```md
请先读取并遵守根目录 `AGENTS.md`，然后读取：

- dev-notes/task-graph-runtime/00-architecture-overview.md
- dev-notes/task-graph-runtime/10-integration-gates.md
- dev-notes/task-graph-runtime/02-node-model-and-state-machine.md
- dev-notes/task-graph-runtime/03-node-executor-local-react.md
- dev-notes/task-graph-runtime/04-verifier-retry-and-local-replan.md

只实现 Phase 04。

TODO checklist:
- [ ] 增加或收敛机器可读的 NodeVerifierResult，并持久化到 node state。
- [ ] 将 verifier 的 retryable failure 接入 node retry，并把 feedback 传给下一次 attempt。
- [ ] max attempts exhausted 后必须进入 failed/blocked/replan request，不能无限重新调度。
- [ ] 增加最小 local replan entrypoint：替换 failed node 和 downstream pending nodes，保留 completed upstream nodes。
- [ ] 确保 finalizer/task verifier 在 nodes completed 后检查 graph-native task/node acceptance；旧 `TaskContract` 只能作为兼容展示/上下文，不得覆盖 graph 完成状态。

必须包含 retry success、retry exhausted、local replan 保留 upstream completed nodes、graph-native task acceptance/blocker 的测试。
不要重写 entire graph，不要增加 distributed scheduling，不要保留旧 finalization 语义。
```

## 实现状态（截至当前 commit）

| TODO | 状态 | 主要落点 |
|------|------|---------|
| TODO 1 NodeVerifierResult | 已完成 | `session.NodeVerificationResult` 增加 `retry` / `replan` 状态、`Retryable`、`FeedbackForNextAttempt`。`verifyNodeWithModel` 支持解析 `retry`、`replan`、`retryable`、`feedback_for_next_attempt`。 |
| TODO 2 verifier failure -> node retry | 已完成 | `runtime.verifyAndTraceNode` 调用 `prepareNodeRetry`；retryable verifier result 写 `node_retry` trace，并把 feedback 写入 `node.Input["attempt_feedback"]`。下一次 attempt 会通过 node input 进入 node-local prompt。 |
| TODO 3 attempts exhausted | 已完成 | `maxNodeAttempts` 默认 2，可用 node input `max_attempts` 覆盖。耗尽后写 `node_retry_exhausted` trace，并把结果转为 `failed`，scheduler 不再调度该 node。 |
| TODO 4 local replan 最小入口 | 已完成最小闭环 | `session.ApplyLocalReplan` 替换 failed/blocked node 和依赖它的 downstream pending nodes，保留 completed verified upstream 和 blocked/awaiting_input nodes；结果必须通过 `ValidateTaskGraph`。`runtime.applyLocalReplanWithTrace` 写 `local_replan_start` / `local_replan_applied` / `local_replan_failed`。暂不接真实 Planner。 |
| TODO 5 task acceptance repair path | 已完成 graph-native 收口 | `VerifyTaskGraphWithContract` 保留 `TaskContract` 兼容参数，但 graph 状态只由 node status、node acceptance 和 graph-native task acceptance 决定；旧 contract 不再把 completed graph 改成 failed/blocker。repair/synthesis node 仍留给后续扩展，本阶段不自动生成。 |

测试基线：

- `go test ./internal/session`：通过。
- `go test ./internal/runtime ./internal/session`：通过。
- `go test ./...`：通过。

新增/更新测试覆盖：

- `TestRunGraphTask_NodeVerifierRetryThenPasses`
- `TestRunGraphTask_NodeVerifierRetryExhausted`
- `TestApplyLocalReplan_PreservesCompletedUpstream`
- `TestApplyLocalReplan_DoesNotBypassBlockedDownstream`
- `TestVerifyNode_ToolNode_TimedOut`
