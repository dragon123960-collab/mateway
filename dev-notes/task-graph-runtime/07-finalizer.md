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

## 实现 TODO

- [ ] 新增 graph finalizer，输入 graph、task contract、verification result。
- [ ] completed graph 生成 final answer 并完成 task。
- [ ] blocked graph 生成 concrete blocker，保持 active task/graph 可 resume。
- [ ] awaiting_input graph 生成 input-required reply，并创建/保留 pending state。
- [ ] failed graph 记录 failure reason。
- [ ] trace 记录 `graph_finalize_start`、`graph_finalized`、`graph_blocked`。
- [ ] finalizer 输出进入现有 response hooks 和 memory observe。

## 测试 TODO

- [ ] completed graph 只用 verified node results 生成 final。
- [ ] blocked graph 不清空 active task。
- [ ] failed graph 记录 failure reason。
- [ ] awaiting input graph 返回 input-required style。
- [ ] final reply 不包含 raw secret-like evidence。
- [ ] 未完成关键 node 时不会 final completed。

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
