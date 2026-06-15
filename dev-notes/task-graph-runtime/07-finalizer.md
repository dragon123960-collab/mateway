# 07：Graph Finalizer

更新：2026-06-15

## 目标

汇总 completed nodes，生成 final answer、partial answer 或 concrete blocker。

Finalizer 是用户可见出口，不执行工具，不修改 dependency，不重新规划。

## 当前机制参考

- `internal/runtime/runtime.go`
  - final reply handling
  - `CompleteActiveTaskWithSummary`
  - `AwaitUserInputActiveTaskWithSummary`
  - `BlockActiveTask`
- `internal/runtime/completion_evaluator.go`
- `internal/runtime/failure_categories.go`
- response hooks

## 必须接入的位置

Finalizer 不是一个孤立的纯函数阶段。实现时必须接到 graph lifecycle 的末端：

```text
scheduler ready/executed nodes
  -> node verifier
  -> task verifier
  -> graph finalizer
  -> response hook / memory observe / session save
```

第一版可以先提供纯函数 `FinalizeGraph(...)`，但同一阶段必须提供 runtime 调用点或明确的 integration wrapper。不能只新增一个未被 runtime 使用的 helper。

建议输入：

- inbound message/channel metadata。
- active task。
- task graph。
- task-level contract。
- graph/task verification result。
- trace recorder。

建议输出：

- outbound reply text/style。
- final graph status。
- task status update。
- whether to keep `ActiveTask`。
- optional pending action update。

## 输出类型

- `completed`: 全部关键节点完成，生成最终答复。
- `partial`: 部分节点完成，但有非致命缺口，明确说明完成了什么、缺什么。
- `blocked`: 缺工具、权限、用户输入或 policy 阻塞，给 concrete blocker。
- `failed`: 不可恢复失败，给失败原因和可选下一步。

## Final Reply 规则

- 使用 node result summary 和 verified evidence。
- 不直接暴露 raw sensitive trace、secret、token、cookie、私钥。
- 不声称真实动作成功，除非对应 node 有 evidence 且 verifier passed。
- 如果需要用户输入，明确问一个具体问题。
- 如果需要权限/工具，明确缺什么。

## 状态更新规则

- `completed`:
  - 只允许使用 verifier passed 的 node result/evidence。
  - 调用现有 task completion 路径，写 summary，清理 active task。
  - 触发 task completion observe，供 memory 使用。
- `partial`:
  - 不把 graph 标为 completed。
  - 明确列出 verified output 和 missing/blocker。
  - 保持 active task 可 resume，除非用户明确结束。
- `blocked`:
  - 保持 active task/graph。
  - 保留或创建可续接的 pending/blocker 信息。
  - 不重跑 completed node。
- `awaiting_input`:
  - 返回 input-required style。
  - pending action 必须包含 `task_id`、`graph_id`、`node_id`。
- `failed`:
  - 记录 failure reason。
  - 不声称任务完成。

## 禁止捷径

- 不能从 raw transcript 重新猜测任务是否完成。
- 不能用未 verified 的 node summary 生成完成态答复。
- 不能在 finalizer 中执行工具、调用 planner 或改变 dependencies。
- 不能因为 graph status 是 `completed` 就跳过 verification result 检查。
- 不能把 blocked/awaiting_input graph 清掉 active task。

## 实现 TODO

- [ ] 新增 graph finalizer，输入 graph、task contract、verification result 和 active task。
- [ ] 将 finalizer 接到 runtime graph lifecycle 末端，不能只保留未使用纯函数。
- [ ] completed graph 生成 final answer 并完成 task。
- [ ] blocked graph 生成 concrete blocker，保持 active task/graph 可 resume。
- [ ] awaiting_input graph 生成 input-required reply，并创建/保留 pending state。
- [ ] failed graph 记录 failure reason。
- [ ] trace 记录 `graph_finalize_start`、`graph_finalized`、`graph_blocked`。
- [ ] finalizer 输出进入现有 response hooks 和 memory observe。
- [ ] finalizer 后保存 session state。

## 测试 TODO

- [ ] completed graph 只用 verified node results 生成 final。
- [ ] unverified completed-looking node 不会进入 completed final。
- [ ] blocked graph 不清空 active task。
- [ ] failed graph 记录 failure reason。
- [ ] awaiting input graph 返回 input-required style。
- [ ] awaiting input pending 包含 task/graph/node id。
- [ ] final reply 不包含 raw secret-like evidence。
- [ ] 未完成关键 node 时不会 final completed。
- [ ] finalizer 不执行工具、不调用 planner。

## 非目标

- 不执行 node。
- 不调用 scheduler。
- 不重新规划。
- 不把 raw trace dump 发给用户。

## Codex Review 重点

- 用户可见 blocker 是否具体。
- 是否不会夸大未验证动作。
- sensitive evidence 是否脱敏。
- task/graph status 是否和 reply style 一致。
