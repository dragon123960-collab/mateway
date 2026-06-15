# 05：Atomic Node Executor

更新：2026-06-15

## 目标

实现 atomic node executor：`model`、`tool`、`skill`、`human_review`、`human_confirm`。

ReAct 从“全局任务循环”迁移为“节点执行循环”。每个 node 只完成一个明确目标，不能在一个 node 内吞掉多工具 workflow。

## 当前机制参考

- `internal/runtime/runtime.go`
  - `runTask`
  - model/tool loop
  - progress/trace
- `internal/agentcore`
  - transcript-driven AgentCore loop
- `internal/tool`
  - tool registry 和 policy
- `internal/runtime/skill_discovery.go`
  - skill discovery
- graph-native skill metadata 文档

## Node 类型语义

- `model`: 只调用模型生成、总结、分类或撰写，不直接执行真实动作。
- `tool`: 调用一个真实工具。工具成功只是 evidence，不等于 node 完成。
- `skill`: 加载已注册 skill 的 `SKILL.md` 作为 node-local instruction；真实动作仍由 node 内 executor 调用真实工具。
- `human_review`: 等待用户审阅，不自动继续危险动作。
- `human_confirm`: 等待用户确认，不把聊天确认当作 tool policy 替代品。

## Skill Node 规则

- Runtime discovery 只使用已注册 skill metadata。
- 选中 skill node 后，executor 再读取对应 `SKILL.md`。
- `granularity=atomic` 的 skill 可作为单个 skill node。
- `granularity=workflow` 的 skill 必须由 planner 拆成多个 atomic nodes。
- Skill name 不是 tool name。

## 实现 TODO

- [ ] 新增 node executor dispatch，按 node type 分派。
- [ ] `model` node 复用当前 model routing，输出 node result summary。
- [ ] `tool` node 复用现有 tool registry、policy、redaction、observe hooks。
- [ ] `skill` node 只读取已注册 skill 的 `SKILL.md`，并作为 node-local instruction。
- [ ] human node 创建 pending action，保存 `graph_id` / `node_id`。
- [ ] 每次执行增加 node attempts，写 result/evidence/failure。
- [ ] trace 记录 `node_execute_start`、`node_tool_call`、`node_execute_result`。
- [ ] 执行失败时只标记当前 node，不直接失败整个 graph，交给 scheduler/finalizer 判断。

## 测试 TODO

- [ ] `model` node 只调用模型并写 result summary。
- [ ] `tool` node 调用一个工具并记录 evidence ref。
- [ ] tool policy 拒绝时 node 标记 blocked/failed，并带 failure reason。
- [ ] `skill` node 会读取已注册 skill，本地裸 `SKILL.md` 不参与。
- [ ] workflow skill 不会被 executor 当作单个大 node 执行。
- [ ] human node 创建 pending action，用户回复后可 resume node。
- [ ] node attempts 累加。
- [ ] node executor 不修改其他 completed nodes。

## 非目标

- 不实现 planner。
- 不实现 scheduler 并行。
- 不实现 verifier 的最终判断。
- 不绕过 tool policy。
- 不新增命令执行工具。

## Codex Review 重点

- node 是否 atomic。
- ReAct 是否被限制在 node-local loop。
- tool policy/redaction/observe 是否复用现有机制。
- skill 是否走注册 metadata + node-local instruction。
- human node 是否保存明确 pending 状态。
