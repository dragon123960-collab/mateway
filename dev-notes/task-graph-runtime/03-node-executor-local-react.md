# 03 Node Executor 与局部 ReAct

## 目标

实现 node execution modes。复杂子任务通过 AgentCore 跑 node-local ReAct；简单子任务使用 direct model call；skill 和确定性动作都通过同一个 node executor 边界分发。

## 执行模式

- `direct`：渲染 node prompt，调用模型一次。
- `react`：以 node goal、node inputs、依赖输出、allowed tools、acceptance 启动 AgentCore loop。
- `skill`：加载已注册 skill metadata 和 `SKILL.md`，再按 skill execution type 分发。
- `script` / `tool`：确定性执行路径，只用于低成本原子动作。
- `human`：创建 pending action，暂停 graph，等待用户回复。

## Node-Local ReAct 规则

- ReAct 只接收 node-local context 和依赖 summary/output。
- Allowed tools 由 node metadata 限定。
- 复用现有 tool policy、redaction、observe hook、tool retry 和 trace。
- Tool calls 产出 node evidence refs 和 trace events。
- ReAct final answer 不等于完成；必须通过 node verifier。

## 待办

- [ ] 增加 `executeReactNode`，使用 AgentCore 和 node-scoped prompt/tools。
- [ ] 按 node mode 路由 direct/react/skill/script/tool/human。
- [ ] 保存 node final output 和 structured outputs。
- [ ] 将 tool calls 附加为 evidence refs，而不是 graph nodes。
- [ ] 更新 dogfood 测试：一个 react node 内部可完成多工具工作。

## 验收标准

- react node 能在内部读文件、跑命令，然后产出一个 node result。
- direct node 使用一次模型调用。
- human node 创建明确的 pending control。
- react node 内部仍然执行 tool policy 和 redaction。
- trace 展示带 `graph_id` / `node_id` / `attempt` 的 node-local tool calls。

## 非目标

- 不建立 global ReAct fallback。
- 不允许 skill 或 agent role 绕过 tool policy。
