# 02 Node 模型与状态机

## 目标

让 TaskGraph node 语义符合最终架构：node 是可验收子任务，包含执行 mode、outputs、attempts、evidence refs 和 acceptance。

## Node 语义

Node 是恢复边界和验收边界。一个 node 内部可以包含多轮模型调用和多个工具调用。下游 node 默认消费上游 node 的 outputs 和 summary，不消费完整 transcript。

建议概念字段：

- `id`
- `goal`
- `type`: `subtask | skill | human_confirm | human_review | tool`
- `mode`: `direct | react | skill | script | tool | human`
- `depends`
- `inputs`
- `outputs`
- `allowed_tools`
- `skill`
- `status`
- `attempts`
- `result_summary`
- `evidence_refs`
- `failure_reason`
- `acceptance`
- `verified_at`

## 状态机

必需语义：

```text
pending -> ready -> running -> verifying -> completed
running -> awaiting_input
running/verifying -> retrying -> running
running/verifying -> failed
failed -> blocked 或 local replan
completed -> 后续恢复时跳过
```

当前代码不一定立即持久化所有 transient state，但文档、trace 和测试必须共享同一套语义。

## 恢复规则

- completed + verified：永不重跑
- awaiting_input：保留 pending action
- running 时崩溃：恢复为 pending/retryable，attempts 保留
- pending：等待依赖
- ready：重新进入调度
- blocked/failed：等待用户、retry policy 或 local replan

## Task Lineage 边界

Node 状态机只管理单个 TaskGraph 内部状态。历史任务分支不要塞进 node status。需要从历史任务继续时，用 Task Lineage 字段表达：

- `ParentID`: 从哪个历史任务派生
- `ForkedFromNodeID`: 可选，从旧任务哪个 graph node 继续
- `ContextRefs`: 引用旧 task/node/evidence

本阶段可以只预留语义，不要求完整实现任务树索引。

## 待办

- [ ] 给 node model 或等价内部结构增加 execution mode。
- [ ] 统一 type/mode 验证。
- [ ] 确保 attempts 和 verifier result 按 node 持久化。
- [ ] 更新恢复测试：completed skip、running reset、awaiting_input preservation。
- [ ] 更新暗示 node 等于 tool call 的文档和测试。

## 验收标准

- Node model 能表达 direct、react、skill、script/tool 和 human node。
- completed verified node 不会再次被调度。
- running node 恢复后能继续，且不丢失 attempt history。
- 工具调用写入 trace/evidence refs，不默认成为 graph 结构。

## 非目标

- 本阶段不实现并行调度。
- 本阶段不实现完整 Tree Memory。
- 本阶段不把 Session 做成树。
