# 路线图

Mateway 的主线是成为 local-first agent runtime kernel：一次 Planner 生成 TaskGraph，node 表示可验收子任务，复杂 node 内部使用局部 ReAct，工具调用作为 node 内 action/evidence 记录。

## 当前主线

```text
Planner
  -> TaskGraph
  -> Scheduler
  -> Node-local execution
  -> Node verifier
  -> Graph / task verifier
  -> Finalizer
  -> Memory observe
```

## 近期工作

- Planner + TaskContract 合并：一次 planning call 输出 task acceptance、required capabilities 和 subtask graph。
- Subtask node 模型：node 不默认表示工具调用，tool node 只保留为确定性特例。
- Node-local ReAct：复杂 node 内部使用 AgentCore loop 和 allowed tools。
- Verifier / retry / local replan：node 验收失败先重试，attempts 耗尽后替换 failed node 和 downstream pending nodes。
- Trace / session recovery：用 graph state 从未完成节点继续，不从头执行。
- Skill metadata v2：已注册 skill 才参与发现，metadata 提供 planner-facing inputs/outputs/type。
- Stable docs 与 dev-notes 重建。

## 后续工作

- `max_parallel_nodes` 本地并发调度。
- 更好的 model/deterministic verifier 分层，降低 LLM 验收成本。
- Domain app embedding API：structured input/output、status、trace、progress stream。
- Docker 后端的终端沙箱，用于 `terminal.run`。
- Heartbeat/offline memory distill 持续改进，包括主体-关系-客体整理。

## 非目标

- 不做 heavy workflow platform。
- 不做 distributed workflow engine。
- 不做 multi-tenant company scheduler。
- 不做 distributed multi-agent supervisor 或 subagent spawning。
- 不做 gateway 业务路由层。
- 无飞书/Lark 专用 runtime 分支。
- 除 `terminal.run` 外无其他命令执行工具。

Mateway 可以支持本地 agent node 作为执行角色，但调度系统、分布式 worker 和公司级 orchestration 应作为外部系统单独实现，并调用 Mateway 作为 runtime kernel。
