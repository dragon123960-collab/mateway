# 04 Verifier、Retry 与 Local Replan

## 目标

在不重跑整个任务的前提下提升完成质量。Node verifier 检查子任务输出，node retry 修复局部错误，local replan 修复失败的 graph 区段。

## 验证策略

优先使用确定性检查：

- script/tool exit status 和 evidence
- required structured outputs 是否存在
- human confirmation 状态
- 已知 artifact path 或 ref

只有语义验收需要时才调用 model verifier。

## 重试策略

Node retry 在 replan 前发生：

- execution failure：带 error feedback 重试
- verifier failure：带 missing requirements 和 verifier reason 重试
- attempts exhausted：标记 failed/blocked，并考虑 local replan

默认实现应支持 max node attempts。Tool-level retry 是更底层机制，和 node retry 分开。

## Local Replan

最小 local replan 只保留 completed upstream nodes，并替换：

- failed node
- 依赖 failed node 的 downstream pending nodes

Planner 输入包括 task goal、current graph、failed node、failure reason、attempt summaries、available tools/skills 和 upstream outputs。Planner 不得改写 completed verified nodes。

## 待办

- [ ] 增加带 verifier feedback 的 node retry。
- [ ] 将 attempt summaries 和 failure reasons 写入 trace/session。
- [ ] 实现最小 local replan entrypoint。
- [ ] 替换 failed node 和 downstream pending nodes，同时保留 completed upstream nodes。
- [ ] 为缺失 final output 增加 task acceptance repair path。

## 验收标准

- Verifier failure 会先重试同一 node，而不是直接 graph failure。
- attempts exhausted 可以触发 local replan。
- local replan 后 completed upstream nodes 不重跑。
- task final acceptance 不满足时，产生 repair/replan，而不是空失败文本。

## 非目标

- 不做无限 replan。
- 不在部分完成后重写整个 graph。
- 不做自动多分支规划竞赛。
