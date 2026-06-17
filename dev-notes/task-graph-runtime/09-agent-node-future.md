# 09 Agent Node 未来方向

## 目标

记录未来 multi-agent 路径，同时避免把 Mateway 做成分布式 supervisor。

## 原则

Multi-agent support 指的是本地 agent role 作为 TaskGraph node executor。

```text
TaskGraph Runtime
  -> agent node: reviewer
  -> agent node: tester
  -> agent node: domain expert
```

Agent node 不是独立 orchestration layer。它仍受同一套 node acceptance、allowed tools、trace、session recovery、verifier、memory observe 规则约束。

## 可能形态

```json
{
  "id": "review-implementation",
  "type": "agent",
  "mode": "react",
  "agent_id": "reviewer",
  "depends": ["implement-feature"],
  "allowed_tools": ["file.read", "terminal.run"],
  "acceptance": "Findings are grounded in file/line references or explicitly state no issues"
}
```

## 边界

允许的未来方向：

- local role agents
- agent-specific model/tools/system prompt
- graph node acceptance 和 verifier
- shared trace/session/memory

不进入 Mateway core：

- distributed multi-agent supervisor
- subagent spawning tree
- multi-tenant scheduler
- company-level queue/resource orchestration
- gateway business routing

外部系统可以把 Mateway 当作 runtime kernel，为每个任务调用它。

## 待办

- [ ] 保持当前实现不引入 distributed supervisor 假设。
- [ ] 文档中只为 local agent node roles 预留词汇。
- [ ] 在 subtask-node runtime 稳定前，不实现本阶段。

## 验收标准

- 稳定文档说明 Mateway 可以支持 local agent nodes。
- 稳定文档说明 distributed multi-agent orchestration 不在范围内。
- 当前实现阶段不依赖 agent nodes。
