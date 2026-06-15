# 阶段 1：任务续接状态机

更新：2026-06-15

## 目标

把当前依赖 `ActiveTask + Pending + 文本相似度 + 最近任务摘要` 的续接逻辑，升级为 Task Graph 的状态机。

续接任务不再主要靠 transcript 猜测，而是根据 graph、node 和 pending 状态判断下一步：

- graph 是否还 open
- 当前是否卡在 human node
- 是否有 blocked/failed node 可重试
- completed graph 是否只能作为新 graph 的输入引用
- historical resume 是否返回 graph summary 和 node timeline

本阶段只设计和实现续接状态机，不实现完整 scheduler 或 node executor。

## 当前机制参考

当前续接入口：

- `internal/runtime/task_plan.go`
  - `judgeTaskContinuity`
  - `latestOpenTask`
  - `looksLikeSameTaskFollowup`
- `internal/runtime/runtime.go`
  - `Runtime.Handle`
  - `shouldStartNewTaskInsteadOfSteering`
- `internal/session/store.go`
  - `ActiveTask`
  - `Pending`
  - `EnsureTask`
  - `ActivateTask`
- `task.search` / `task.resume`
- `task-recall` skill

当前机制的有效部分：

- pending control 优先级最高。
- active open task 可以被短回复或相似文本续接。
- completed task 默认不重新激活。
- 历史任务通过 `task.search` / `task.resume` 显式恢复上下文。

当前机制的不足：

- 不知道任务具体卡在哪个阶段。
- failed task 只能靠文本判断是否继续或开新任务。
- completed task 的复用主要靠摘要，不知道哪些中间成果可复用。
- 续接后容易重新执行已完成动作。

## 状态模型

### Graph 状态

建议 graph status：

- `planned`: graph 已生成但未执行。
- `running`: graph 正在执行。
- `awaiting_input`: graph 等待用户输入。
- `blocked`: graph 有节点阻塞，等待用户修复、授权或改计划。
- `failed`: graph 无法继续。
- `completed`: graph 已完成。

### Node 状态

建议 node status：

- `pending`: 依赖未满足或尚未执行。
- `ready`: 依赖满足，等待 scheduler 执行。
- `running`: 正在执行。
- `awaiting_input`: human node 等待用户输入。
- `blocked`: 节点被 policy、缺权限、缺工具或验收失败阻塞。
- `failed`: 节点执行失败且不可继续。
- `completed`: 节点通过验收。
- `skipped`: 因 replan 或依赖变化被跳过。

## 续接判定顺序

Runtime 收到新消息后，按以下顺序判断：

1. `/new`：结束当前续接判断，创建新 task + graph。
2. `PendingAction`：优先解释为 pending 控制输入。
3. Active graph 存在 human node：
   - 如果当前 node 是 `human_review` / `human_confirm` / `await_input`，用户消息进入该 node。
   - 若用户明确说“新任务”，则创建新 graph。
4. Active graph blocked/failed：
   - “继续、重试、修复了、授权了、可以了”等进入 blocked node resume/retry。
   - 明显无关的新动作创建新 graph。
5. Active graph running/open：
   - 短确认、同目标补充、同 node 所需输入进入当前 graph。
   - 明显无关的新动作创建新 graph。
6. Completed graph：
   - 默认不重新激活。
   - “基于上次/继续上次做 X”创建新 graph，并引用旧 graph summary/node outputs。
7. Historical resume：
   - 用户提到旧任务、上次、历史任务但当前 session 无明确 graph 时，使用 `task.search` / `task.resume`。
   - resume 返回上下文，不直接修改 archive。

## 输出动作

状态机输出一个明确 decision：

```go
type ContinuationDecision struct {
    Action      string // new_graph | continue_graph | resume_node | answer_pending | reference_completed | historical_search
    TaskID      string
    GraphID     string
    NodeID      string
    Reason      string
    UserText    string
    ContextRefs []string
}
```

行为含义：

- `new_graph`: 创建新 task graph。
- `continue_graph`: 继续当前 graph ready calculation。
- `resume_node`: 针对 blocked/failed/awaiting node 继续。
- `answer_pending`: 处理 pending control。
- `reference_completed`: 新建 graph，但引用已完成 graph 成果。
- `historical_search`: 让 planner/skill 使用 task search 路径。

## 与 Planner / Scheduler 的关系

- 状态机只决定“这条消息属于哪个 task/graph/node”。
- Planner 决定是否需要 replan 或生成新 graph。
- Scheduler 决定哪些 ready nodes 执行。
- Node executor 不重新判断任务归属。

## 实现 TODO

- [ ] 在 runtime 中设计 `ContinuationDecision` 纯函数。
- [ ] 用 graph/node state 替代仅靠 `ActiveTask` 和文本相似度的判断。
- [ ] human node 输入优先于普通任务文本。
- [ ] blocked node 支持 retry/resume，不重跑 completed nodes。
- [ ] completed graph 只能作为新 graph 的 input refs，不自动重新激活。
- [ ] `task.resume` 返回 graph summary、node timeline、failed nodes、reusable outputs。
- [ ] trace 记录 continuation decision：task_id、graph_id、node_id、reason。

## 测试 TODO

- [ ] pending control 优先于普通续接。
- [ ] human node 等待时，短回复进入该 node。
- [ ] blocked node 收到“授权了/修复了/继续”后进入 resume_node。
- [ ] unrelated action 在 active graph 下创建 new_graph。
- [ ] completed graph 不被自动激活。
- [ ] “基于上次继续做 X”创建 new_graph，并带 completed graph refs。
- [ ] historical resume 返回 graph/node summary，不修改 archive。

## 非目标

- 不实现完整 graph scheduler。
- 不实现 node executor。
- 不引入 gateway 业务路由。
- 不让 completed graph 被隐式重新打开。

## Codex Review 重点

- 状态机是否是纯判断逻辑，方便单元测试。
- 是否避免重跑 completed nodes。
- 是否清晰区分 active graph 续接、completed graph 引用和 historical resume。
- 是否没有把续接逻辑放进 channel/gateway。

