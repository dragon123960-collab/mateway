# TaskGraph Runtime

Mateway 的最终主线是一个 local-first agent runtime kernel。它把用户任务规划为 `TaskGraphPlan`，再按可恢复、可验收的子任务节点执行。Mateway 可以被 CLI、Bot、Electron 应用、本地服务或外部调度系统调用，但自身不演变为分布式 workflow 平台。

## 核心模型

```text
Inbound Message
  -> Planner
  -> TaskGraph
  -> Scheduler
  -> Node Executor
  -> Node Verifier
  -> Graph / Task Verifier
  -> Finalizer
  -> Memory Observe
```

Planner 是唯一的任务规划入口。历史上的 `TaskContract` 语义会并入 Planner 输出中的 task acceptance、required capabilities 和 final output contract，不再作为第二套计划。

TaskGraph node 表示一个可验收子任务，不表示一次工具调用。工具调用只存在于 node 内部的 ReAct/action trace 中，并作为 evidence refs 支撑 node 验收。`tool` node 可以保留为低成本、确定性的特例，但不是默认规划粒度。

## TaskGraphPlan

Planner 一次输出完整计划：

- task goal、risk、acceptance、required capabilities 和 final output shape
- nodes、depends、mode、inputs、outputs、allowed tools、skill、risk 和 acceptance
- human confirm / review gates

Planner 不输出工具调用序列。复杂任务应该生成少量子任务节点，每个节点内部再通过 direct model call、local ReAct、skill、script 或 human gate 完成。

## Node 执行模式

Node execution mode 描述子任务内部如何执行：

- `direct`: 一次模型调用完成简单生成、摘要、解释或最终 synthesis。
- `react`: 使用 AgentCore 作为 node-local ReAct loop。模型可调用受限工具，直到产出 node final output。
- `skill`: 读取已注册 skill metadata 和 `SKILL.md`，按 skill execution type 执行。
- `script` / `tool`: 确定性脚本、API 或单工具特例。
- `human`: 等待用户确认或审阅。

ReAct 不再是全局任务控制流，而是复杂 node 的局部执行策略。Node 执行完成后必须由 verifier 判断是否满足 acceptance。

## 验证、重试与局部重规划

Verifier 分层执行：确定性检查优先，只有语义验收需要时才调用 model verifier。

Node 验收失败不会立刻重跑整个任务。Runtime 先用 verifier feedback 重试同一 node；attempts 耗尽后进入最小 local replan：保留 completed upstream nodes，替换 failed node 和 downstream pending nodes，然后继续执行。

Task 最终验收检查 graph outputs 是否满足 task acceptance。若 task acceptance 不满足，runtime 应追加 repair/synthesis node 或触发局部 replan，而不是从头执行整个任务。

## Trace、Session 与恢复

Trace 是事实证据链。所有关键事件必须可关联到 `task_id`、`graph_id`、`node_id` 和 `attempt`。Trace 记录 planner output、node scheduling、node-local tool calls、node final output、verifier result、finalizer 和 memory observe。

Session graph state 是恢复状态。崩溃或断电后：

- completed and verified nodes are skipped
- awaiting input nodes keep pending actions
- running nodes recover to pending/retryable with attempts preserved
- pending nodes wait for dependencies
- ready nodes resume scheduling

Runtime should continue from the graph state instead of replaying the whole task from transcript.

## 状态层

Mateway 不把 session、task 或 memory 做成 Git 那种 content-addressed tree/object store。当前稳定边界是：

- TaskGraph：单个任务内部的 DAG 状态，记录 subtask nodes、dependencies、node status、outputs 和 evidence refs。
- Task Lineage Tree：历史任务之间的父子/分支关系，记录从哪个 task、node 或 evidence 继续。
- Session：可变恢复快照，保存 messages、active task、pending actions 和恢复所需的 latest graph state。
- Trace：append-only event ledger，最接近 commit log，但不是用户长期记忆。
- Memory：经过筛选的 durable knowledge，保存 accepted diary/learning/proposal outputs 和 derived indexes，不保存 raw trace dumps。

Session 不做 tree。历史任务分支属于 Task Lineage，长期知识结构属于 Memory Tree/Graph。未来如果需要 Tree Memory，应从 trace events、graph summaries 和 curated memory 中构建可重建索引，而不是把 runtime 主状态替换成完整 Git-like object database。

## Task Lineage

TaskGraph 解决“一个任务内部如何执行”。Task Lineage 解决“历史任务之间如何继续和分支”。

未完成任务应从原 graph state 恢复；已完成任务不改写历史，而是 fork 新 task。新 task 可以记录：

- `parent_id`: 父任务
- `forked_from_node_id`: 可选，旧任务中的 graph node
- `context_refs`: 引用旧 task/node/evidence

这支持类似“从上次分析结果继续写方案”“从某个失败 node 换方向重试”“基于旧 evidence 派生一个新任务”的体验，同时避免把 Session 变成长期任务树。

## Memory

Memory consumes graph summaries; it does not store trace dumps as durable memory. Task completion writes a `GraphMemorySummary` containing task status, node summaries, attempts, failures, evidence refs, selected skills and final output.

Heartbeat/offline distill may further extract subject-relation-object facts, resolve conflicts, and propose durable learning. This higher-level memory processing is separate from runtime execution.

未来 Memory Tree/Graph 是正确方向，但它属于长期知识索引：用户偏好、项目事实、主体-关系-客体、稳定经验和任务经验沉淀。它不能绕过 policy、human gate 或 session recovery。

## Skills

A skill is a local instruction or execution package. A skill is discoverable/executable only after local registration creates `.mateway/metadata.yaml` next to `SKILL.md`.

Metadata v2 should describe minimal planner-facing semantics: `type: prompt|react|script`, stage, granularity, inputs, outputs, allowed tools and safety notes. Full JSON Schema is intentionally deferred until the skill ecosystem stabilizes.

## Agent Nodes

Mateway may later support local agent nodes as execution roles, such as coder, reviewer, tester or domain expert. Agent nodes are TaskGraph node executors and remain subject to node acceptance, trace, policy and verifier.

Mateway is not a distributed multi-agent orchestration platform, a multi-tenant scheduler, or a company-wide workflow system. External schedulers can call Mateway as a runtime kernel.
