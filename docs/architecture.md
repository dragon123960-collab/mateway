# 架构

Mateway 是一个 local-first agent runtime kernel。它使用 TaskGraph 执行真实工作区任务，同时保持小型、可审计、可恢复的 runtime 边界。Mateway 可以作为 CLI、Bot、Electron 应用、本地服务或外部调度系统的底座，但不做分布式 workflow 平台。

## 包地图

- `cmd/mateway`: CLI 入口。
- `internal/cli`: 命令处理器、TUI 渲染、trace 展示和本地诊断。
- `internal/runtime`: TaskGraph 任务生命周期、Planner、node execution、verifier、finalizer、trace 和 observe hooks。
- `internal/agentcore`: 模型/工具循环。它是 node-local ReAct executor 的基础能力，不是全局任务控制流。
- `internal/tool`: 内置工具，包括 file、terminal、web、schedule、task 和 secret 工具。
- `internal/session`: 持久化 session、TaskGraph、node 状态、pending actions、recovery state 和 transcript 摘要。
- `internal/gateway`: 渠道路由、去重、session keys、异步 runtime 执行和回复分发。
- `internal/channel`: 平台 I/O 适配器和消息归一化。
- `internal/config`: 配置加载、默认值、初始化资产、models、agents、channels、skills 和安全设置。
- `internal/memory`: Markdown memory、proposals、lint/search/index、learning distill 和 skill usage evidence。它消费 GraphMemorySummary，不保存 trace dump 作为长期事实。
- `internal/skill`: 技能目录、metadata、注册/安装/doctor、验证和 secret 扫描。

## Runtime 边界

Runtime 拥有 TaskGraph 状态和执行策略，但不成为 heavy workflow engine。

- Planner 一次输出 task acceptance 和 subtask graph。
- Scheduler 只做本地 ready-node 调度。
- Node Executor 执行 direct、react、skill、tool 或 human node；skill `script` 执行仍在规划中，runtime 尚未实现。
- AgentCore 在 node 内部提供局部 ReAct loop。
- 工具实现真实动作并返回 evidence。
- Tool policy、path validation 和 secret redaction 是硬安全边界。
- Gateway 负责会话路由和渠道分发，不做业务级多 agent 路由。

## TaskGraph 模型

TaskGraph node 是可验收子任务，不是一次工具调用。复杂 node 内部可以调用多个工具、尝试、观察和修正；最终只产出 node result、outputs、evidence refs 和 verifier result。

`tool` node 可以作为确定性低成本特例保留，但 Planner 默认不应把复杂任务拆成 tool-node graph。

历史 `TaskContract` 的角色会并入 Planner 输出的 task acceptance、required capabilities 和 final output contract。它不再代表第二套计划。

## Trace, Session, Memory

三者边界固定：

- Trace: 事实证据链，记录发生了什么。
- Session graph state: 恢复快照，记录当前执行到哪里；它不是历史任务树。
- Task Lineage Tree: 历史任务之间的父子/分支关系，用于从旧 task、node 或 evidence 继续。
- Memory: 任务完成后的蒸馏结果，记录未来值得记住什么。

Runtime 不把 trace dump 直接当长期记忆。Memory observe 接收 GraphMemorySummary，Heartbeat/offline distill 可以继续做关系化整理。

未来 Memory Tree/Graph 是长期知识索引方向，不是 runtime recovery store。Session 保持轻量，TaskGraph 管单任务执行，Task Lineage 管历史分支，Memory 管长期知识。

## Skills

Skill 是本地可编辑能力包：

```text
workspace/agents/<agent_id>/skills/<skill_name>/SKILL.md
workspace/agents/<agent_id>/skills/<skill_name>/.mateway/metadata.yaml
workspace/skills/<skill_name>/SKILL.md
workspace/skills/<skill_name>/.mateway/metadata.yaml
```

只有注册并带 `.mateway/metadata.yaml` 的 skill 才参与发现和执行。Skill metadata 面向 Planner 描述 type、stage、granularity、inputs、outputs、allowed tools 和 safety notes。

## Multi-Agent Boundary

Mateway 可支持本地 agent node 作为执行角色，例如 coder、reviewer、tester 或 domain expert。Agent node 仍是 TaskGraph node executor，受 node acceptance、tool policy、trace、session recovery 和 verifier 约束。

Mateway 不做分布式 multi-agent supervisor、multi-tenant scheduler 或公司级任务调度平台。外部调度系统可以把 Mateway 作为 runtime kernel 调用。
