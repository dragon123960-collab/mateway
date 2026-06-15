# 10：Runtime 主路径替换

更新：2026-06-15

## 目标

用 Task Graph Runtime 替换当前主执行路径。旧机制只保留被新结构吸收后的必要代码，不作为运行时回退路径。

这是最后整合阶段，不是新增 `v1/v2 mode switch`。

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

## 实现 TODO

- [ ] 将 `Runtime.Handle` 主路径改为 graph lifecycle。
- [ ] 删除或内聚旧 `judgeTaskContinuity` / 线性 steering 入口，保留必要 helper。
- [ ] `runTask` 拆为 graph planner/scheduler/node executor/finalizer 调用链。
- [ ] 保持 `/new`、pending control、session reset 行为稳定。
- [ ] 保持 existing hooks/tool policy/redaction 行为。
- [ ] 更新 CLI/TUI progress 展示 graph/node 状态。
- [ ] 更新稳定文档：`docs/architecture.md`、`docs/execution-flow.md`、`README.md`、`README.zh.md`。

## 测试 TODO

- [ ] 现有 runtime 测试迁移到 graph 语义后全绿。
- [ ] 简单问答走单 model node。
- [ ] 工具任务走 tool node + verifier + finalizer。
- [ ] skill 任务走 registered skill node。
- [ ] human confirm 可以 pending/resume。
- [ ] blocked graph 不重跑 completed nodes。
- [ ] completed graph 可以被 context refs 引用但不重新激活。
- [ ] memory、trace、session recovery 全链路可用。
- [ ] `go test ./...` 全绿。

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
