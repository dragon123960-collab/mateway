# 03 Node Executor 与局部 ReAct

## 开发前必须先读

OpenCode 开始本阶段前必须先读：

1. `dev-notes/task-graph-runtime/00-architecture-overview.md`
2. `dev-notes/task-graph-runtime/10-integration-gates.md`
3. `dev-notes/task-graph-runtime/02-node-model-and-state-machine.md`
4. 本文档
5. 当前相关源码：
   - `internal/runtime/node_executor.go`
   - `internal/runtime/runtime.go`
   - `internal/runtime/scheduler.go`
   - `internal/runtime/tool*.go`
   - `internal/agentcore/*`
   - `internal/session/graph.go`

本阶段不要重新设计 Planner。Planner 已在 01 阶段负责生成 TaskGraphPlan。Executor 只接收一个 ready node 并执行它。

## 阶段目标

把旧的“全局任务 ReAct loop”收敛为 **node-local execution**：

```text
Scheduler 选中 ready node
  -> Node Executor 根据 node mode 分发
  -> direct/react/skill/tool/script/human 路径执行
  -> 写回 node result/evidence/status
  -> Node Verifier 决定是否 completed/retry/failed
```

复杂任务仍然可以有 ReAct，但 ReAct 的作用域降级到单个 node。这样可以减少长 transcript、减少无关上下文、让失败和重试停留在局部。

## 当前代码基线

当前 runtime 已经有 node executor、tool execution helper、scheduler helper、trace/event skeleton。Phase 01 后 Planner 能产生 node mode/type。Phase 02 应已保证 node 字段和状态机能持久化。

本阶段要补齐的是：

- Scheduler 到 Executor 的主链路是否真实接上。
- Executor 是否按 node mode 分发。
- React mode 是否使用 node-local AgentCore loop，而不是回到全局 loop。
- Tool call 是否作为 node 内 evidence/trace，而不是 graph node。
- Direct/skill/human/tool/script 至少有可测试路由。

## 执行模式契约

### `mode=direct`

用于简单问答、摘要、纯生成。

要求：

- 一次模型调用。
- Prompt 只包含 node goal、dependency summaries/outputs、node acceptance、必要的 task context。
- 不调用工具。
- 输出写入 node `result_summary` 和可选 structured `outputs`。
- 之后仍进入 verifier；direct final text 不等于自动完成。

### `mode=react`

用于需要局部多步推理和工具的子任务。

要求：

- 使用现有 AgentCore 能力，构建 node-local transcript。
- 输入包括 node goal、dependency outputs/summaries、allowed_tools、acceptance、attempt feedback。
- 工具池必须按 node `allowed_tools` 限制。
- 复用 tool policy、redaction、tool retry、observe hook、trace。
- ReAct 最终输出写入 node result，但必须经过 verifier。

### `mode=skill`

用于已注册 skill node。

要求：

- Executor 只在 node 已选择 skill 后读取 `SKILL.md`。
- Skill discovery/metadata 规则由 06 完整实现，本阶段可以保留已有 metadata check 或 stub。
- 根据 skill metadata 的 execution type 走 direct/react/script。
- Skill instruction 是 node-local instruction，不污染整个 task prompt。

### `mode=tool` / `mode=script`

确定性特例，不是默认 planner 输出。

要求：

- 只用于低成本、输入输出明确的原子动作。
- 仍受 tool policy/path validation/redaction 约束。
- 结果写入 node output/evidence。

### `mode=human`

用于人工确认、人工审阅、缺权限、需要用户选择。

要求：

- 创建 pending control/pending action。
- node 状态进入 `awaiting_input`。
- Scheduler 不继续执行该 node。
- 用户回复后由 continuation/recovery 路径恢复该 node 或 fork 新 task，具体细节由 05 接。

## Executor 输入契约

Executor 必须接收或构造下列信息：

```text
task_id
graph_id
node_id
attempt
node goal
node type/mode
node acceptance
dependency result summaries
dependency structured outputs
allowed tools
skill ref, if any
attempt feedback, if retrying
```

Executor 禁止：

- 重新调用 Planner 规划整个 task。
- 读取完整历史 transcript 当作默认上下文。
- 绕过 Scheduler 直接执行 blocked/failed/awaiting_input node。
- 根据中文/英文关键词猜用户意图来决定危险动作。

## Trace 与 Evidence 契约

每个 node attempt 至少应有：

```text
node_started
node_final_output 或 node_failed
```

每个工具调用至少应有：

```text
node_tool_call
node_tool_result
```

事件必须带：

```text
task_id
graph_id
node_id
attempt
```

Tool result 进入 `evidence_refs`，不要默认变成 graph node。

## 本阶段必须完成

### TODO 1：打通 Scheduler -> Executor 主链路

可能涉及文件：

- `internal/runtime/scheduler.go`
- `internal/runtime/node_executor.go`
- `internal/runtime/runtime.go`

要求：

- Scheduler 选出的 ready node 能交给 Executor。
- Executor 开始前将 node 标记为 `running` 并增加/记录 attempt。
- Executor 完成后写 result/evidence，并进入 `verifying` 或交给 verifier。
- 不允许只在单测里直接调用 executor helper 而主 runtime 未接入。

测试：

- fake graph 有一个 ready direct node，runtime tick/run 后 node 被执行并写回 result。

### TODO 2：实现 direct node 路由

可能涉及文件：

- `internal/runtime/node_executor.go`
- `internal/runtime/model*.go`

要求：

- Direct node 只调用模型一次。
- 模型输入不包含完整历史 transcript。
- 输出进入 node result summary/outputs。

测试：

- fake model 记录调用次数，断言 direct node 只调用一次。
- direct node 无工具调用 trace。

### TODO 3：实现 react node 路由

可能涉及文件：

- `internal/runtime/node_executor.go`
- `internal/agentcore/*`
- `internal/runtime/tool*.go`

要求：

- React node 使用 node-local AgentCore loop。
- allowed tools 限制生效。
- tool policy、redaction、observe hook、tool retry 仍然走现有管道。
- tool result 进入 node evidence refs。

测试：

- fake model + fake tool：react node 内部调用工具并产出 final。
- 不在 allowed_tools 的工具被拒绝。
- tool call trace 带 task_id/graph_id/node_id/attempt。

### TODO 4：实现 skill/human/tool/script 路由骨架

可能涉及文件：

- `internal/runtime/node_executor.go`
- `internal/runtime/skill*.go`
- `internal/runtime/human*.go`

要求：

- `mode=skill` 能路由到现有 skill executor 或明确 stub，不得静默当 direct。
- `mode=human` 创建 pending action，并标 `awaiting_input`。
- `mode=tool/script` 走确定性执行路径或明确返回 unsupported blocker。
- Unsupported mode 要产生 concrete blocker，不要 panic 或空失败。

测试：

- skill node 缺 metadata 或缺 skill ref 时 blocked。
- human node 产生 pending action。
- unsupported mode 返回 blocked/failed with reason。

### TODO 5：清理全局 ReAct fallback

要求：

- 新任务不应在 graph runtime 失败后回到旧全局 ReAct loop。
- 如果旧函数仍存在，只能作为内部能力或待删除代码，不能是主执行 fallback。
- 旧测试如果依赖 fallback 完成任务，应改为 graph node executor 测试。

## 主链路接入要求

完成本阶段后，至少能走通：

```text
Planner output -> Session graph -> Scheduler ready node -> Executor direct/react -> node result/evidence -> Verifier stub or real verifier
```

如果 04 verifier 尚未完整实现，可以保留简单 verifier stub，但 node result 必须已经写回 session。

## 禁止事项

- 不实现 local replan；属于 04。
- 不实现并行调度；属于 08。
- 不把 skill discovery 完整改为 metadata-only；属于 06。
- 不新增多 agent supervisor/subagent。
- 不新增 tool 作为默认 graph node 的 planner 逻辑。
- 不用中文关键词判断执行路径，测试里可以有中文用例。

## 验收标准

- direct node 一次模型调用后写回 node result。
- react node 在 node-local context 内跑 AgentCore loop。
- allowed_tools 生效。
- tool calls 记录为 node evidence/trace。
- human node 能暂停 graph。
- Executor 不重新规划整个 task。
- `go test ./internal/runtime ./internal/session` 通过。

## 集成闸门检查

对照 `10-integration-gates.md`，本阶段必须满足：

- Scheduler -> Node Executor：输入包含 task/graph/node/attempt/mode/dependency outputs。
- Node Executor -> Trace/Session：写回 status、attempts、result、evidence refs。
- 工具调用不是 graph node。
- Executor 不能绕过 policy/redaction/observe。

## 交给 OpenCode 的提示词模板

```md
请先读取并遵守根目录 `AGENTS.md`，然后读取：

- dev-notes/task-graph-runtime/00-architecture-overview.md
- dev-notes/task-graph-runtime/10-integration-gates.md
- dev-notes/task-graph-runtime/02-node-model-and-state-machine.md
- dev-notes/task-graph-runtime/03-node-executor-local-react.md

只实现 Phase 03。

TODO checklist:
- [ ] 在 runtime 主链路中，把 scheduler 选出的 ready node 接到 node executor。
- [ ] 实现 direct node：只能调用模型一次，不能调用工具。
- [ ] 实现 react node：使用 node-local AgentCore context，并限制 allowed_tools。
- [ ] 确保 tool call 产生 node-scoped trace/evidence refs，包含 task_id/graph_id/node_id/attempt。
- [ ] 补齐 skill、human、tool/script、unsupported mode 的路由测试。
- [ ] 删除或改写依赖 global ReAct fallback 的测试。

必须包含使用 fake model/tool 的 focused runtime tests。
不要实现 local replan、parallel scheduling、distributed agents，也不要完整实现 metadata-only skill discovery，除非只是必要 stub。
遇到 unsupported path 要返回明确 blocked/failed，不要静默映射成 direct mode。
```
