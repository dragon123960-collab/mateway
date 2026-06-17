# 07 Memory 与 Heartbeat Distill

## 目标

保留当前 memory/heartbeat 架构，但输入改为 graph-aware summaries。

## Runtime Memory 输入

任务完成时，memory observe 接收 `GraphMemorySummary`：

- task goal、status、final text
- graph id 和 node timeline
- node id、goal、type/mode、status
- attempts、failure reason、verifier result
- result summary 和 structured outputs
- evidence refs 和 selected skills

工具调用默认不是长期记忆。工具调用仍是 node 下的 trace/evidence refs。

## Heartbeat Distill

Heartbeat/offline memory distill 继续处理 durable memory、diary、learning 和 proposals。主体-关系-客体抽取属于这里，不属于 node execution。

Heartbeat 可以使用 graph summaries 改进：

- failed node learning
- 重复 workflow 的 skill proposal
- 用户偏好和项目事实更新
- relation distill 和 conflict handling

Memory 不是恢复存储。Session 和 TaskGraph 决定任务如何恢复；Memory 只在有意义的 task/node 结果后接收 graph-aware summary。用户可编辑 Markdown memory 是 source of truth，JSONL/index 是 append-only 或可重建派生数据。

## Memory Tree / Graph 方向

未来可以做 Memory Tree 或 Memory Graph，但它是长期知识索引，不是当前任务恢复机制：

- 短期：当前 session/transcript summary
- 中期：按任务、项目、主题组织的 task/node summary
- 长期：用户偏好、项目事实、稳定经验、主体-关系-客体

原始证据不能丢。结构化关系、摘要、embedding 都是索引；需要通过 evidence refs 回到 trace/task/node 事实链。

## 待办

- [ ] 确保 GraphMemorySummary 包含 node attempts、failures、evidence refs、selected skills。
- [ ] 保持 diary/learning/skill usage/proposal 输出格式稳定。
- [ ] 将 skill usage 关联到成功的 skill node result，而不只是发现/读取过 skill。
- [ ] 将 heartbeat S-R-O distill 记录为离线层。
- [ ] 为未来 Memory Tree/Graph 预留从 GraphMemorySummary 构建索引的字段。

## 验收标准

- 完成任务的 memory 能指出 failed/retried nodes。
- Skill usage 关联到 node result。
- Heartbeat 可读取 graph-aware summaries，不依赖 runtime trace dumps。
- 用户编辑的 Markdown memory 保持 source of truth；derived indexes 可重建。
- Memory Tree/Graph 的方向被记录清楚，但不会改变本阶段 runtime 执行路径。

## 非目标

- 本阶段不实现完整 Tree Memory store。
- 不在 node execution 同步抽取关系。
- 不让 memory 直接驱动工具动作或绕过 policy。
