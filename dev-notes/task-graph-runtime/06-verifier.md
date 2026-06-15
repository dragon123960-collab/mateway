# 06：Node Verifier 与 Task Verifier

更新：2026-06-15

## 目标

实现 node acceptance 和 task acceptance 两层验证。工具成功只是 evidence，不等于节点完成。

当前 completion evaluator 迁移为：

- node-level verifier：判断单个 node 是否达成 goal/acceptance。
- graph/task-level verifier：判断整个 graph 是否满足用户任务。

## 当前机制参考

- `internal/runtime/completion_evaluator.go`
- `internal/runtime/task_contract.go`
- `internal/runtime/failure_categories.go`
- `internal/runtime/agent_hooks.go`
- `TaskContract` 和 plan item evidence

## 验证输入

Node verifier 输入：

- node goal。
- node acceptance criteria。
- node output/result summary。
- evidence refs。
- tool observations。
- attempts/failure reason。

Task verifier 输入：

- task goal。
- task-level `TaskContract`。
- graph node timeline。
- completed/failed/blocked nodes。
- final draft。

## 验证输出

建议：

```go
type VerificationResult struct {
    Status        string // passed | failed | blocked | needs_input
    Reason        string
    Missing       []string
    EvidenceRefs  []EvidenceRef
    VerifiedAt    time.Time
}
```

## 实现 TODO

- [ ] 新增 node verifier，优先 deterministic evidence checks，必要时复用 completion evaluator 模型判断。
- [ ] 工具调用成功后不直接 completed，必须经过 node verifier。
- [ ] verifier passed 时设置 node `completed`、`Acceptance.Verified=true`、`VerifiedAt`。
- [ ] verifier failed 且可重试时保留 node status，交给 executor/scheduler 重试策略。
- [ ] verifier blocked 时写 failure reason 和 missing evidence。
- [ ] task verifier 汇总 completed nodes 和 `TaskContract`，决定 finalizer 是否可完成。
- [ ] trace 记录 `node_verify_start`、`node_verified`、`task_verified`。

## 测试 TODO

- [ ] tool success 但缺 acceptance evidence 时 node 不 completed。
- [ ] model output 满足 criteria 时 node completed。
- [ ] verifier 失败记录 missing 和 reason。
- [ ] blocked node 可被后续 resume/retry。
- [ ] task verifier 在关键 node 未完成时不允许 final answer。
- [ ] task verifier 在全部关键 node completed 时 passed。
- [ ] verifier 不绕过 tool policy 或 human confirm。

## 非目标

- 不执行工具。
- 不生成 final reply。
- 不把 verifier 做成 planner。
- 不让模型改变 graph dependency。

## Codex Review 重点

- 是否清晰区分 evidence、node completion、task completion。
- 是否保留当前 completion evaluator 的安全边界。
- 是否不会因为工具成功而误标完成。
- failure/blocker 是否可恢复、可追踪。
