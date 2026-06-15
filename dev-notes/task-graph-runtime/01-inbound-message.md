# 阶段 1：Inbound Message 与任务续接入口

更新：2026-06-15

## 目标

实现 Task Graph 主线的入口节点：入站消息进入 runtime 后，先完成 deterministic 的任务归属判断，再进入 graph lifecycle。

本阶段不写大模型提示词。Inbound Message 节点是 runtime 状态机，不是 model node。

Graph Planner 的提示词从阶段 2 开始设计。

## 当前机制参考

当前入口：

- `internal/runtime/runtime.go`
  - `Runtime.Handle`
  - `/new`
  - `handlePending`
  - `shouldStartNewTaskInsteadOfSteering`
- `internal/runtime/task_plan.go`
  - `judgeTaskContinuity`
  - `latestOpenTask`
  - `looksLikeSameTaskFollowup`
- `internal/session/store.go`
  - `State.ActiveTask`
  - `State.Pending`
  - `EnsureTask`
  - `ActivateTask`

相关细节见：

- [01A：任务续接状态机](./01a-continuation-state-machine.md)

## Prompt 设计

本阶段没有 LLM prompt。

原因：

- 入站消息归属必须稳定、可测试、可恢复。
- pending control、human node、blocked node retry 不能依赖模型猜测。
- channel/gateway 仍只做 I/O，不能让模型参与路由。

本阶段只输出 `ContinuationDecision`，供后续 graph planner 或 scheduler 使用。

## 输入

- `channel.InboundMessage`
- `session.State`
- 当前 active task
- 当前 graph state
- pending action
- 用户文本和附件 metadata

## 输出

```go
type ContinuationDecision struct {
    Action      string
    TaskID      string
    GraphID     string
    NodeID      string
    Reason      string
    UserText    string
    ContextRefs []string
}
```

建议 action：

- `new_graph`
- `continue_graph`
- `resume_node`
- `answer_pending`
- `reference_completed`
- `historical_search`

## 实现 TODO

- [ ] 抽出纯函数判断入站消息归属，避免把逻辑散在 `Runtime.Handle` 中。
- [ ] 保持 `/new` 优先级最高。
- [ ] 保持 pending control 优先于普通任务续接。
- [ ] 如果 graph 卡在 human node，用户消息进入该 node。
- [ ] 如果 graph blocked/failed，用户明确“继续/重试/授权了/修复了”时进入 `resume_node`。
- [ ] completed graph 默认不重新激活，只能作为新 graph 的 context refs。
- [ ] historical resume 只返回上下文，不修改 archive。
- [ ] trace 记录 continuation decision。

## 测试 TODO

- [ ] `/new` 总是创建新 graph。
- [ ] pending control 优先处理。
- [ ] human node 等待时短回复进入当前 node。
- [ ] blocked node 收到“继续/授权了/修复了”进入 resume path。
- [ ] unrelated action 在 active graph 下创建 new graph。
- [ ] completed graph 不会自动重新激活。
- [ ] “基于上次继续做 X”创建 new graph，并引用 completed graph。

## 非目标

- 不实现 graph planner prompt。
- 不实现 scheduler。
- 不实现 node executor。
- 不在 gateway/channel 中加入业务路由。

## Codex Review 重点

- 入口判断是否 deterministic 且单元测试充分。
- 是否没有模型调用或 prompt。
- 是否没有把旧 runtime 作为 fallback path。
- 是否避免重跑 completed graph/node。
