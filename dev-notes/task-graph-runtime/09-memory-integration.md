# 09：Memory 集成

更新：2026-06-15

## 目标

以 Task + Node 粒度接入 memory：任务完成时统一写 diary/learning，同时记录 node timeline、失败点、attempts 和 evidence refs。

Memory 不直接驱动工具动作，不绕过用户确认和 tool policy。

## 当前机制参考

- `internal/memory`
  - diary markdown
  - learning JSONL
  - skill usage JSONL
  - proposal 机制
- `internal/runtime/hooks.go`
- `internal/runtime/memory_proposal.go`
- 当前 task completion observe

## Memory 输入

任务完成时写入：

- task goal/status/final text。
- graph summary。
- node timeline。
- failed/retried/blocked nodes。
- selected skills。
- trace graph_id/node_id refs。

Node 级摘要包括：

- `id`
- `type`
- `goal`
- `status`
- `attempts`
- `result_summary`
- `evidence_refs`
- `failure_reason`
- `verified_at`

## 集成点

- Node observe：节点完成/失败时更新 graph state，不直接写长期 memory。
- Task completion observe：任务完成时调用 memory，写 diary/learning/skill usage/proposal。
- Heartbeat distill：离线从 task + node evidence 中蒸馏长期经验。

第一版不做每个节点独立长期 memory proposal。

## 实现 TODO

- [ ] 扩展 `LearningEvent` 或新增 graph summary 输入结构。
- [ ] task completed 时传入 node timeline。
- [ ] failed/blocked task 写入失败 node 和 blocker reason。
- [ ] skill usage 关联到 skill node result，而不是只记录读过 `SKILL.md`。
- [ ] diary 中记录 attempts 和关键 evidence summary。
- [ ] memory proposal 保持用户确认流程。
- [ ] heartbeat distill 默认只读 graph evidence，不写 runtime state。

## 测试 TODO

- [ ] 简单单节点任务 diary 正常写入。
- [ ] 复杂 graph diary 包含 node timeline。
- [ ] 节点重试后 memory 记录 attempts。
- [ ] 失败任务 memory 能指出 failed node。
- [ ] skill node 完成后 skill usage 关联 node result。
- [ ] memory 不包含 raw secret-like evidence。
- [ ] memory proposal 仍需用户确认。

## 非目标

- 第一版不做每个节点独立长期记忆 proposal。
- 不让 memory 自动执行工具。
- 不让 memory 绕过 human confirm。
- 不把 raw trace dump 写入 diary。

## Codex Review 重点

- memory 是否是 task completion 后的观察结果。
- 是否记录 node 粒度而不是 transcript 猜测。
- 是否避免 secret/raw trace 泄漏。
- skill usage 是否和 node result 关联。
