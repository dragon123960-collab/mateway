# 00 架构总览

## 状态

这是 Mateway 最终主线的 TaskGraph Runtime 设计。它替代旧的 contract/checklist/global loop 心智模型。代码实现过程中可以存在过渡结构，但新的开发应朝这个设计收敛，不为旧语义新增兼容层。

## 目标

把 Mateway 建成一个 local-first agent runtime kernel：

```text
入站消息 -> Planner -> TaskGraph -> Scheduler -> Node Executor -> Verifier -> Finalizer -> Memory Observe
```

Node 是可验收子任务。工具调用只是 node 内部的 action，应写入 trace/evidence，不默认提升成 graph node。

## 核心原则

- Planner 一次输出 task acceptance 和 subtask graph。
- `TaskContract` 语义并入 Planner 输出，不再新增第二个规划阶段。
- 复杂子任务由 node-local ReAct 执行。
- completed 且 verified 的 node 永不重跑。
- Verifier 决定是否完成；工具成功只是 evidence，不等于 node 成功。
- Trace 是事实链。
- Session 是恢复快照。
- Memory 是任务和节点完成后的蒸馏结果，不是 trace dump。
- TaskGraph 表达单个任务内部的 DAG。
- Task Lineage Tree 表达历史任务之间的父子、分支和从某个 node/evidence 继续。
- Memory Tree/Graph 可以作为未来长期知识索引方向，但不进入本轮 runtime 主状态。
- Session 不做 tree；它保持轻量运行态和恢复态。
- Mateway 可以作为 Electron、Bot、CLI、外部调度系统的 runtime kernel。
- 未来可以加入本地 agent node 作为 executor 角色，但不做分布式 multi-agent orchestration。

## 状态层边界

```text
TaskGraph
  单个任务内部的执行 DAG：依赖、并行、验收、恢复。

Task Lineage Tree
  历史任务之间的关系：fork、继续、从旧 node/evidence 派生。

Memory Tree / Memory Graph
  长期知识结构：用户偏好、项目事实、主体-关系-客体、经验沉淀。

Session
  当前对话和恢复快照：messages、active task、pending action、latest graph state。

Trace
  append-only 事实账本：planner、node、tool、verifier、finalizer、memory observe。
```

不要把这些层混成一个 Git-like object store。Mateway 可以借用 Git 的不可变日志、引用、分支思想，但不要实现完整 content-addressed tree database。

## 当前代码现实

当前代码已经有 graph skeleton、node status、scheduler helper、node executor、verifier、finalizer、trace 和 graph memory summary。它也仍然保留旧 contract/checklist/global loop 假设和一些 tool-node-oriented 测试。后续实现可以删除或重写旧测试，不需要为了旧语义保留冗余兼容。

## 非目标

- 不做 heavy workflow platform。
- 不做 distributed workflow engine。
- 不做 multi-tenant company scheduler。
- 不做 distributed multi-agent supervisor 或 subagent spawning。
- 不做 gateway 业务级 routing layer。
- 不新增 `terminal.run` 之外的命令执行工具。
- 不把 Session 做成任务树、知识树或 Git-like store。

## OpenCode 使用方式

实现时必须先读本文件、`10-integration-gates.md`，再读一个编号阶段文档。只实现该阶段 TODO checklist，不要从未来阶段的背景描述里推断额外重构。

每个阶段完成后，不能只按当前阶段文档自测，还必须回到 `10-integration-gates.md` 检查本阶段承诺的跨组件接口是否已经接上。Codex review 也按“当前阶段文档 + 集成闸门”一起验收。
