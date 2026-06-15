# 路线图

Mateway 当前的方向是为小型 local-first agent runtime 做 loop engineering。

## 当前主线

```text
planning contract
  -> selected skill preflight
  -> executable checklist
  -> transcript-driven ReAct execution
  -> evidence evaluator
  -> final answer or blocker
```

## 近期工作

- 提示精简：保持常驻 runtime 上下文小巧，只在触发时注入 freshness、connector 和 self-knowledge 部分。
- Skill 上下文门控：保持默认技能作为可编辑资产，但执行时只注入已选技能。
- Contract 检查清单上下文：给执行阶段提供紧凑的检查清单，而非完整的 JSON contract 加重复规则。
- 文档重构：保持 README 简短，将稳定的架构、配置、执行流程和路线图说明移至 `docs/`。

## 后续工作

- Docker 后端的终端沙箱，用于 `terminal.run`。
- 当规划遗漏相关 skill 时，更精确的 contract 修复。
- 更完善的临时重试和抓取失败预算，关联到 plan items 和 required evidence。
- 长期会话、大型工具输出和保留 task contracts 时更安全的上下文经济。
- 基于重复成功工作流的技能和记忆结晶。

## 非目标

- 无 PlanExecute 框架。
- 无 DAG runtime。
- 无 multi-agent supervisor 或子 agent 派生。
- 无 gateway 业务路由层。
- 无飞书/Lark 专用 runtime 分支。
- 除 `terminal.run` 外无其他命令执行工具。

这些边界是有意设置的。Mateway 应通过使循环更可观察、更 evidence 感知和更本地有用来成长，而非变成一个沉重的 workflow 平台。
