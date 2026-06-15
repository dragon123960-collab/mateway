# 03：Graph Planner 与提示词

更新：2026-06-15

## 目标

把当前 `ensureTaskContract` 演进为 graph planner：模型输出 task list with `depends`，runtime 负责校验并转换为 `TaskGraph`。

Planner 不直接生成复杂 DAG 执行计划。它只提出节点、依赖、输入输出、验收标准和风险提示。

## 当前机制参考

- `internal/runtime/task_plan.go`
  - `ensureTaskContract`
  - `TaskContract`
  - plan review pending
- `internal/runtime/task_contract.go`
  - contract JSON schema
  - evidence requirements
  - tool/skill validation
- `internal/runtime/skill_discovery.go`
  - runtime skill summary
- `internal/tool`
  - tool registry 和 policy 边界

## Planner 输入

- 用户原始请求。
- `ContinuationDecision`。
- 当前 session/task summary。
- 已注册 skill metadata 摘要。
- 可用 tool 摘要。
- 风险/权限上下文。
- 如果是 `reference_completed`，传入 context refs 的 task/graph 摘要。

不要把完整 transcript 或裸 `SKILL.md` 全量塞给 planner。

## Planner 输出

Planner 输出 graph draft，不直接修改 session。

建议结构：

```json
{
  "goal": "user-visible task goal",
  "risk": "low|medium|high",
  "nodes": [
    {
      "id": "collect",
      "type": "tool|model|skill|human_review|human_confirm",
      "goal": "collect repository files",
      "depends": [],
      "executor": "repo.read",
      "inputs": ["workspace"],
      "outputs": ["file_summary"],
      "acceptance": "repository files are summarized"
    }
  ],
  "task_acceptance": "final answer includes architecture summary and risks"
}
```

Runtime 负责：

- normalize IDs。
- reject invalid node types。
- validate dependencies。
- map planner fields into `TaskGraph`.
- decide whether plan review/human confirm is required.

## Prompt 要求

Planner prompt 必须强调：

- 输出 JSON only。
- 不输出 shell script。
- 不把 skill name 当 tool name。
- 一个 node 是 atomic 目标，不是隐藏 workflow。
- 如果 skill metadata `granularity=workflow`，必须拆成多个 atomic nodes。
- 如果任务高风险，插入 `human_review` 或 `human_confirm` node。
- 简单问答可以输出单个 `model` node。
- Runtime 决定并行，不要让模型写 scheduler 指令。

## 实现 TODO

- [ ] 新增 graph planner 函数，返回 graph draft 或 `TaskGraph`。
- [ ] 复用当前 model routing 和 JSON parsing 习惯，不新增 planner 专用 runtime mode。
- [ ] 复用 tool/skill summary，不读取未注册裸 skill。
- [ ] 将 planner 输出转换为阶段 02 的 `TaskGraph`。
- [ ] 调用 `ValidateTaskGraph`，失败时生成 concrete blocker 或 replan request。
- [ ] 保留当前 plan review pending 机制作为 human confirmation 的承载方式，语义迁移到 graph plan review。
- [ ] trace 记录 `graph_planner_start`、`graph_planner_output`、`graph_validation_failed`、`graph_planned`。

## 测试 TODO

- [ ] 简单问答生成单个 `model` node。
- [ ] 安全工具任务生成 atomic `tool` + `model` nodes。
- [ ] 复杂任务生成带 depends 的 graph。
- [ ] 高风险任务包含 `human_confirm` 或 plan review pending。
- [ ] workflow skill 不允许作为单个大节点吞掉多工具流程。
- [ ] planner 输出非法 dependency 时 validator 拒绝。
- [ ] planner 输出未知 tool/skill 时生成 blocker，不执行。
- [ ] planner prompt 不包含裸 secret 或完整 sensitive trace。

## 非目标

- 不实现 scheduler。
- 不执行 node。
- 不让模型决定并行数量。
- 不让模型绕过 tool policy。
- 不把旧 runtime 作为 fallback mode。

## Codex Review 重点

- prompt 是否把模型限制在规划意图和依赖，不让它控制 runtime。
- planner 输出是否全部经过 validator。
- skill/tool 边界是否清晰。
- 高风险任务是否进入 human confirmation。
- 是否没有引入双 runtime mode switch。
