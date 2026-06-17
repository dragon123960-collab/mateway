# 08 Scheduler 并行与真实模型 Dogfood

## 目标

启用本地 ready-node 并行，并用真实模型 dogfood 验证完整架构。

## 并行规则

Scheduler 使用 `max_parallel_nodes` 执行 ready nodes。Ready 的条件是依赖 node 已 completed 或 skipped。并行只在本地进程内发生。

规则：

- 依赖未完成的 node 不运行。
- completed verified node 不重跑。
- 执行前必须先持久化 node status。
- 避免静默并行 mutation 冲突；高风险 mutation node 需要 human gate 或串行约束。

`max_parallel_tools` 仍属于 AgentCore/node 内部，并不等于 `max_parallel_nodes`。

## Dogfood 场景

用真实模型验证：

- 简单问答 -> 一个 direct node
- 仓库分析 -> 一个或多个 react node，内部有工具调用
- 文件/脚本任务 -> react node 或 subtask nodes，不爆炸成 tool-node chain
- 写入前 human confirm
- verifier failure -> retry
- attempts exhausted -> local replan
- running node crash/recovery
- registered skill node
- memory observe with graph summary
- 从历史 task/node/evidence fork 新任务

## 待办

- [ ] 增加 `max_parallel_nodes` 配置并接入 runtime。
- [ ] 本地并行执行独立 ready nodes。
- [ ] 增加 parallel scheduling trace coverage。
- [ ] 编写 dogfood checklist 和预期 trace/session/memory 断言。
- [ ] 运行真实模型 dogfood 并记录 findings。

## 验收标准

- 独立 nodes 可以在限制内并发运行。
- 有依赖的 nodes 会等待。
- Dogfood 验证 planner、executor、verifier、recovery、skill、memory 路径。
- 失败会产生具体 blocker，而不是空错误文本。

## 非目标

- 不做 distributed scheduler。
- 不做 worker queue platform。
