# 01 Planner 与计划 Schema

## 目标

把旧的 `TaskContract -> Graph Planner` 两段式模型改为一次 Planner 输出：`TaskGraphPlan`。Planner 在一次 LLM call 中同时产出任务级验收和子任务 graph。

## 必需行为

Planner 输出必须包含：

- task goal、risk、acceptance、final output shape
- required capabilities：tools、skills、human gates、structured outputs
- nodes：id、goal、type、mode、depends、inputs、outputs、allowed_tools、skill、risk、acceptance
- 当用户要求确认或风险需要时，插入 `human_confirm` / `human_review` node

Planner 不输出复杂工作的工具调用序列。复杂任务应输出子任务 node，以及 node-local ReAct 可使用的 allowed tools。

## 概念形态

Go 类型可以不同，但语义应匹配：

```json
{
  "task": {
    "goal": "...",
    "risk": "low|medium|high",
    "acceptance": "...",
    "required_capabilities": {
      "tools": ["file.read"],
      "skills": ["repo-analyzer"],
      "human_gates": ["confirm-before-write"]
    },
    "final_output": {
      "text": true,
      "structured": ["summary", "artifacts"]
    }
  },
  "nodes": [
    {
      "id": "analyze-codebase",
      "goal": "Analyze the repository structure and entrypoints",
      "type": "subtask",
      "mode": "react",
      "depends": [],
      "inputs": ["repo_path"],
      "outputs": ["architecture_summary"],
      "allowed_tools": ["file.read", "terminal.run"],
      "acceptance": "Includes entrypoints, modules, run commands, and risks"
    }
  ]
}
```

## 实现说明

- `TaskContract` 可以在迁移期作为内部兼容结构保留，但必须由 Planner 输出填充，不再单独调用模型生成。
- Planner prompt 必须明确：node 是子任务，tool call 是 node 内部 action。
- 只有确定性、低成本动作才允许规划为 tool/script 特例 node。
- 未知 tool/skill 必须在执行前验证失败并给出 blocker。
- Planner 可以引用历史任务，但历史任务分支不属于当前 TaskGraph 内部依赖，应通过 Task Lineage/ContextRefs 表达。

## 待办

- [ ] 定义或适配 `TaskGraphPlan` 和 task-level acceptance 的 Go 结构。
- [ ] 用一次 planning call 替代独立 contract generation。
- [ ] 将 Planner 输出转换为 `session.TaskGraph` 和任务验收状态。
- [ ] 更新 mode、human gate、tool、skill 的验证逻辑。
- [ ] 更新测试：复杂任务应生成 subtask/react node，而不是 tool-node chain。

## 验收标准

- 简单问答生成一个 direct node。
- 代码分析任务生成一个或多个 react subtask node，而不是 `file.read` 链。
- 明确要求确认的高风险写操作，会在 mutation 前生成 human_confirm node。
- 需要工具但规划失败时，返回具体 blocker，不伪造工具参数。

## 非目标

- 本阶段不实现 node-local ReAct。
- 本阶段不实现 local replan。
- 本阶段不做完整 JSON Schema 输入/输出验证。
