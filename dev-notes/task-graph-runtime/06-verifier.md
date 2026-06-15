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

## Verification Strategy

第一版 verifier 可以使用 deterministic checks 作为安全底线，但不能把关键词匹配当作最终可靠验收。

分层策略：

```text
hard checks
  -> model verifier
  -> deterministic apply result
```

Hard checks 负责不可协商条件：

- tool 是否有 evidence refs。
- tool 是否被 policy blocked。
- node 是否缺 result summary。
- human node 是否仍 awaiting input。
- secret/raw trace 是否不能进入结果。

Model verifier 负责语义判断：

- result summary 是否满足 acceptance criteria。
- evidence 是否足以支撑 node goal。
- task contract 是否已被 completed nodes 满足。
- blocker 是否具体、是否可恢复。

模型只能输出结构化 verification result，不能执行工具、改变 dependency、修改 graph plan、绕过 human confirm 或 tool policy。

建议模型输出：

```json
{
  "status": "passed | failed | blocked | needs_input",
  "reason": "short reason",
  "missing": ["..."],
  "confidence": "low | medium | high"
}
```

代码中不要加入中文关键词规则。若需要 deterministic keyword extraction，也只作为辅助 guard，并应使用语言无关或配置化逻辑；中文验收语义应交给 model verifier。

如果本阶段暂不接 model verifier，必须在代码和测试中明确这是 conservative fallback：有 acceptance criteria 但证据不足时返回 blocked，而不是 passed。

## 实现 TODO

- [ ] 新增 node verifier，优先 deterministic evidence checks，必要时复用 completion evaluator 模型判断。
- [ ] 为 semantic acceptance 预留 model verifier 接口；模型只判断，不执行动作。
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
- [ ] 中文 acceptance criteria 不依赖硬编码中文关键词。
- [ ] model verifier 输出 malformed 时保守 blocked/failed，不 passed。

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
