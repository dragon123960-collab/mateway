# 10：Runtime 主路径替换

更新：2026-06-15

## 目标

用 Task Graph Runtime 替换当前主执行路径。旧机制只保留被新结构吸收后的必要代码，不作为运行时回退路径。

这是最后整合阶段，不是新增 `v1/v2 mode switch`。

本阶段的重点不是“快速删除 `runTask`”，而是把旧主循环里已经稳定的用户体验、安全边界和 observe 行为逐项迁移到 graph lifecycle。只有 parity tests 覆盖后，才能把 graph runtime 设为唯一主入口。

## 当前机制参考

- `internal/runtime/runtime.go`
  - `Runtime.Handle`
  - `runTask`
- 阶段 01-09 所有 graph runtime 组件
- 现有 runtime 测试套件

## 替换策略

所有新任务进入 graph lifecycle：

```text
Handle
  -> continuation decision
  -> graph planner or graph resume
  -> validation
  -> scheduler
  -> node executor
  -> verifier
  -> finalizer
```

任务复杂度由 graph 表达：

- 简单问答：单 `model` node。
- 低风险动作：少量 atomic nodes。
- 复杂任务：多节点 graph。
- 高风险任务：graph 中含 human node。

旧机制处理方式：

- `TaskContract` 保留为 task acceptance 兼容层。
- AgentCore loop 被 node executor 复用。
- tool registry、policy、redaction、observe hooks 复用。
- 不保留 “旧 runtime fallback”。

## 整合顺序

不要一次性重写 `Runtime.Handle`。按以下顺序接入，保证每一步可 review：

1. 保留 inbound/pending/session reset 行为，插入 graph lifecycle wrapper。
2. 新任务创建 task + graph，简单问答也生成单 `model` node。
3. existing graph resume 走 scheduler ready calculation，不重新规划 completed graph。
4. ready node 执行前保存状态，执行后保存 evidence/result。
5. node executor 后必须调用 node verifier，不允许 executor 直接决定 completed。
6. scheduler 根据 verified node status 推进 graph。
7. task verifier 判断 graph 是否满足 task contract。
8. finalizer 生成 reply，并调用 response hook / memory observe。
9. 保存 session，写 trace done。
10. 删除或内聚旧线性 runTask 入口，保留可复用 helper。

每一步都必须有 focused tests。不能通过削弱旧 runtime tests 来换取全绿。

## 分段开发计划

阶段 10 拆成 5 个 reviewable slices。OpenCode 每次只实现一个 slice；Codex review 通过后再进入下一 slice。

### 10A：Graph lifecycle wrapper，不改变行为

目标：

- 在 `Runtime.Handle` 内建立 graph lifecycle 的解析入口，但不让它绕过当前稳定的 pending、reset、continuation、context 注入和 response 流程。
- 对已有 graph task 使用 `runGraphTask` 继续执行；对没有 graph 的旧任务保持当前行为，直到后续 slice 明确接管。
- 写 trace 表明本次消息是否进入 graph lifecycle、继续旧 task，或只是 pending control。

必须迁移/保持：

- `/new`、session reset、pending memory proposal、task plan confirm 仍在 graph execution 前短路。
- completed graph 只能作为 context reference，不重新执行。
- blocked/awaiting-input graph 按 01A 状态机续接。

测试：

- pending memory proposal 不进入 graph scheduler。
- completed graph 被引用但不重新激活。
- awaiting human node 能 resume 到对应 node。
- `go test ./internal/runtime ./internal/session`。

非目标：

- 不创建新 graph。
- 不删除 `runTask`。
- 不改变简单问答或工具任务的现有执行结果。

### 10B：把旧 AgentCore loop 行为迁移到 node executor

目标：

- 让 `model` / `tool` / `skill` node executor 具备旧主循环中必要的运行时行为，避免切主路径后丢功能。

必须迁移/保持：

- context budget telemetry。
- model thinking/progress sink。
- long-running tool progress start/end。
- stored `TaskStep` compatibility 或 graph-to-progress adapter。
- tool result redaction 后再写 trace/session/transcript。
- promise repair / unexecuted commitment gate。
- completion evaluator / task contract satisfied trace。
- previous task context injection。
- observe hook 仍拿到 tool call/result 和 task/node refs。

测试：

- 旧 runtime 中覆盖以上行为的 tests 不能削弱。
- 增加 node executor focused tests，证明这些行为在 graph node 内触发。
- `go test ./internal/runtime ./internal/tool ./internal/session`。

非目标：

- 不切换所有新任务到 graph。
- 不新增工具或 gateway/channel 行为。

### 10C：简单问答进入单 `model` node

目标：

- 对 simple Q&A 创建一个 graph，包含一个 atomic `model` node。
- finalizer 对单 model node 保持直接、自然的回答，不把 graph 结构暴露给用户。

必须迁移/保持：

- response hook 和 secret redaction。
- task acceptance 和 node acceptance 都能记录。
- memory observe 记录 task + node timeline。

测试：

- 简单问答生成一个 `model` node 并完成。
- final reply 使用 node verified result，不重复执行 node。
- diary / learning JSONL 含 node record。
- `go test ./internal/runtime ./internal/memory ./internal/session`。

非目标：

- 不让 planner 生成复杂 graph。
- 不迁移真实工具任务。

### 10D：工具/skill/action task 进入 graph nodes

目标：

- 低风险动作和 registered skill 任务走 graph planner -> scheduler -> atomic node executor。
- skill node 只加载已注册 skill 的 `SKILL.md` 作为 node-local instruction；真实动作仍展开为 tool node 或由 node 内 ReAct 调用真实工具。

必须迁移/保持：

- tool policy、路径校验、secret redaction 是硬边界。
- graph state 在真实工具动作前保存。
- tool success 只是 evidence，不能直接代表 node/task completed。
- workflow granularity skill 不允许作为单个执行 node。

测试：

- tool task 走 tool node + verifier + finalizer。
- skill task 走 registered skill node。
- unregistered skill 被拒绝。
- verifier failed 时不会 final completed。
- `go test ./internal/runtime ./internal/skill ./internal/tool`。

非目标：

- 不新增 multi-agent supervisor。
- 不新增 `terminal.run` 之外的命令执行工具。

### 10E：移除旧主执行入口

目标：

- 当 10A-10D parity tests 通过后，删除或内聚旧线性 `runTask` 主入口。
- 保留可复用 helper，但不保留 graph -> old runtime fallback。

必须迁移/保持：

- 所有新任务统一进入 graph lifecycle。
- 旧 session/archive JSON 可读。
- trace 文件仍可阅读；新增 graph/node refs。
- README/docs 在稳定后同步更新。

测试：

- 现有 runtime tests 迁移到 graph 语义后全绿。
- `go test ./...`。
- 运行 11 dogfood，验证真实模型端到端链路。

非目标：

- 不保留 `execution.mode: loop | graph`。
- 不保留新旧 runtime mode switch。
- 不复制旧实验包。

## 主路径伪代码

```go
func Handle(msg) {
    state := loadSession()
    if reset { ... }
    if pending { ... }

    decision := determineContinuation(state, msg)
    task, graph := resolveOrCreateGraph(decision, msg)

    if graph needs planning {
        graph = planGraph(task, msg)
        validateGraph(graph)
        saveState()
    }

    ready := scheduler.ReadyNodes(graph, maxParallel)
    for _, node := range ready {
        markRunningAndSave(node)
        executeNode(node)
        saveState()
        verifyNode(node)
        saveState()
    }

    graphVerification := verifyTaskGraph(task, graph)
    reply := finalizeGraph(task, graph, graphVerification)
    observeMemory(reply, graph)
    saveState()
    return reply
}
```

This pseudocode is normative for sequencing. Implementations can split functions differently, but must preserve the ordering.

## Compatibility Requirements

- `/new` behavior remains stable.
- pending memory proposal and task plan confirm remain handled before new graph execution.
- existing tool policy and path validation remain hard boundaries.
- response hook still sanitizes final text.
- existing trace files remain readable.
- existing session/archive JSON can load without graph fields.
- graph state is saved before any real tool action.
- completed node is never re-executed after resume.

## Forbidden Outcomes

- No `execution.mode: loop | graph`.
- No fallback from graph runtime to old `runTask`.
- No duplicate `runtimev2` / `agentv2` package.
- No gateway/channel business routing.
- No final answer generated from unverified node outputs.
- No hidden old checklist completion gate deciding graph completion.

## 实现 TODO

- [ ] 10A：接入 graph lifecycle wrapper，不改变没有 graph 的任务行为。
- [ ] 10B：迁移旧 AgentCore loop 的 progress、context、redaction、observe、completion parity 行为到 node executor。
- [ ] 10C：简单问答创建单 `model` node，并通过 graph finalizer 返回自然回答。
- [ ] 10D：工具、skill、action task 进入 graph nodes，保持 tool policy/path validation/verifier 边界。
- [ ] 10E：parity tests 全绿后删除或内聚旧线性 `runTask` 主入口。
- [ ] 保存点覆盖 graph planned、node running、node result、node verified、graph finalized。
- [ ] 更新 CLI/TUI progress 展示 graph/node 状态。
- [ ] 稳定后更新 `docs/architecture.md`、`docs/execution-flow.md`、`README.md`、`README.zh.md`。

## 测试 TODO

- [ ] 10A focused tests：pending/reset/completed graph/awaiting input continuation 不回归。
- [ ] 10B parity tests：旧 loop 的 progress、context、redaction、observe、completion evaluator 行为在 node executor 内可用。
- [ ] 简单问答走单 model node。
- [ ] 工具任务走 tool node + verifier + finalizer。
- [ ] skill 任务走 registered skill node。
- [ ] human confirm 可以 pending/resume。
- [ ] blocked graph 不重跑 completed nodes。
- [ ] completed graph 可以被 context refs 引用但不重新激活。
- [ ] memory、trace、session recovery 全链路可用。
- [ ] tool success 但 verifier failed 不会 final completed。
- [ ] process crash after node result but before finalizer can resume without re-running completed node.
- [ ] pending memory proposal path 不进入 graph execution。
- [ ] `go test ./...` 全绿。

## Parity Checklist

切换主路径前，必须逐项确认旧主循环中的以下能力没有丢失：

- Pending controls: memory proposal、task plan confirm、human input。
- Session controls: `/new`、active task reset、completed task reference。
- Context: previous task refs、context budget event、stored transcript 注入。
- Safety: tool policy、path validation、secret redaction、unsafe content rejection。
- Progress: model thinking、tool start/end、long-running tool notice。
- Trace: task id、graph id、node id、tool call/result、verifier decision、finalizer decision。
- Recovery: running node crash 后回到 pending，不重跑 completed node。
- Learning: task completion、failed/blocked graph、skill usage、node evidence refs。
- Final reply: response hook、redaction、blocker style、input-required style。

## 非目标

- 不保留新旧 runtime mode switch。
- 不新增 gateway 业务路由。
- 不引入 multi-agent supervisor。
- 不引入 workflow platform。
- 不复制旧实验包。

## Codex Review 重点

- 是否所有新任务都进入 graph lifecycle。
- 是否没有 fallback 双轨。
- 是否复用现有安全边界。
- 是否旧测试语义被正确迁移，而不是削弱。
- README/docs 是否在稳定后同步更新。
