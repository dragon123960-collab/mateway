# 执行流程

Mateway 的主要流程如下：

```text
入站消息
  -> 活跃任务导向或新任务
  -> task contract
  -> 可选计划审核
  -> 已选 skill 预检
  -> AgentCore ReAct 循环
  -> 工具 evidence 和 plan item 更新
  -> completion evaluator
  -> 最终答案或 blocker
```

## 1. 消息和任务

Gateway 和渠道适配器归一化入站消息。Runtime 要么将消息路由到现有活跃任务，要么创建新的 `TaskNode`。

简短追问可复用先前任务上下文。独立的新任务不应接收弱化的前置任务提示上下文。

## 2. Contract 规划

Runtime 创建轻量级 `TaskContract`：

- 直接任务获得最小计划形态
- 低风险工具任务可自动执行
- 复杂或高风险任务可暂停等待计划审核

Contract 包含一个工具执行检查清单和一个验收检查清单。必需工具必须是真实工具名称。已选技能单独记录。

## 3. Skill 预检

规划阶段可以发现本地 `SKILL.md` header 并选择相关执行技能。已选技能可在执行前读取并转换为真实工具 plan items。

执行阶段默认不会收到完整技能目录。只接收已选任务技能或显式的 skill/workflow 上下文。

## 4. ReAct 执行

模型在正常的 AgentCore 循环中运行。它可以调用可见工具、接收观察结果，并根据 transcript 上下文决定下一步。

Mateway 不会机械地重放计划。Hooks 和 evaluator 在保持循环简洁的同时强制执行 task contract。

## 5. 工具 Evidence

工具结果会更新任务步骤、执行事件、evidence 摘要和 plan item 状态。大型工具输出可被压缩，后续通过 `toolresult.read` 检索。

类 secret 数据在持久化存储和后续模型轮次之前会被脱敏。

## 6. 完成评估

在最终回答之前，completion evaluator 检查：

- 必需工具已被接受
- 必需 evidence 存在或有有效替代
- 必需 plan items 已完成或已阻塞
- 不可用的工具产生具体 blocker

如果任务未完成，模型会收到简短追问：

```text
Missing: <requirement>. Next required action: call <tool> or state blocker.
```

最终输出应报告结果、交付物路径/URL（如适用）或具体的 blocker。
