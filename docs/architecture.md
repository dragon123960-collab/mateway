# 架构

Mateway 是一个小型 local-first 的 Go runtime。核心设计约束是保持单一 transcript-driven 的 AgentCore 循环，并通过 hooks、tool contracts、task contracts、evidence 和 memory 来增加可靠性。

## 包地图

- `cmd/mateway`: CLI 入口。
- `internal/cli`: 命令处理器、TUI 渲染、trace 展示和本地诊断。
- `internal/runtime`: 任务生命周期、task contracts、hooks、context budget、completion 评估、进度事件和 trace 写入。
- `internal/agentcore`: 模型/工具循环、工具注册表、tool contracts、风险分类和工具调用执行。
- `internal/tool`: 内置工具，包括 file、terminal、web、schedule、task 和 secret 工具。
- `internal/session`: 持久化的会话状态、任务节点、task contracts、待处理动作、归档和压缩后的 transcript。
- `internal/gateway`: 渠道路由、去重、session keys、异步 runtime 执行和回复分发。
- `internal/channel`: 平台 I/O 适配器和消息归一化。
- `internal/config`: 配置加载、默认值、初始化资产、models、agents、channels、skills 和安全设置。
- `internal/memory`: Markdown 记忆、proposals、lint/搜索/索引、学习蒸馏和技能学习 evidence。
- `internal/skill`: 技能目录、验证、安装/proposal 辅助和 secret 扫描。

## Runtime 边界

Runtime 拥有任务状态和执行策略，但不会变成 workflow 引擎。

- `AgentCore` 保持为模型/工具循环。
- Runtime hooks 添加上下文、策略、观察、回复清理和完成检查。
- 工具实现执行真实动作并返回 evidence。
- Channel 包只负责接收、归一化、发送和响应平台消息。
- Gateway 负责会话路由和渠道分发，而非业务级的多 agent 路由。

## 任务和 Evidence 模型

动作任务由 `TaskContract` 表示：

- `required_tools`: 仅限真实工具名称。
- `required_skills`: 已选技能，绝不能是工具名称。
- `required_evidence`: 验收条件。
- `plan_items`: 带有工具和状态的可执行检查清单。
- `completion_policy`: 简洁的完成规则。

执行循环仍然是 ReAct。Mateway 不会机械地重放检查清单。相反，工具结果会更新 plan item 状态，由 completion evaluator 决定是否允许最终输出。

## Skills

Skill 是位于以下路径的可编辑 `SKILL.md` 文件：

```text
workspace/agents/<agent_id>/skills/<skill_name>/SKILL.md
workspace/skills/<skill_name>/SKILL.md
```

规划阶段可以发现 skill header 并选择相关的技能。执行阶段只接收已选技能上下文。技能名称不是可接受的工具；工作仍必须通过真实工具（如 `terminal.run`、`file.read`、`web.search`）来完成。

## Memory

长期记忆以 Markdown 格式存储在 `workspace/memory` 下。Runtime 观察可以产生 proposals，但持久的记忆变更需要用户明确操作。派生的索引可重建，不作为唯一事实来源。

## 非目标

- 无 PlanExecute 框架。
- 无 DAG runtime。
- 无 multi-agent supervisor。
- 无 gateway 业务路由。
- 除 `terminal.run` 外无其他命令执行工具。
