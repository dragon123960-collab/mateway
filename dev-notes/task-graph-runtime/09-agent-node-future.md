# 09 Agent Node 未来方向

## 开发前必须先读

OpenCode 开始本阶段前必须先读：

1. `dev-notes/task-graph-runtime/00-architecture-overview.md`
2. `dev-notes/task-graph-runtime/10-integration-gates.md`
3. 本文档
4. 稳定文档：
   - `docs/task-graph-runtime.md`
   - `docs/architecture.md`
   - `docs/embedding-and-app-runtime.md`

本阶段默认是文档和边界确认阶段。除非 01-08 全部稳定，不要实现 agent node 代码。

## 阶段目标

记录未来多 agent 的正确接入点，避免把 Mateway 误做成分布式 supervisor。

Mateway 可以支持：

```text
TaskGraph node
  -> local agent role executor
  -> reviewer / tester / coder / domain expert
```

Mateway 不做：

```text
distributed multi-agent supervisor
subagent spawning tree
company-level scheduler
multi-tenant queue/resource platform
gateway business routing
```

外部调度系统可以调用 Mateway，把 Mateway 当 local-first runtime kernel。

## 当前代码基线

当前主线不要求实现 agent node。前面阶段的 runtime 应已经稳定为：

- Planner 输出可验收 subtask graph。
- Scheduler 调度 ready nodes。
- Node Executor 执行 direct/react/skill/tool/human modes。
- Verifier、trace、session、memory 都围绕 node 边界工作。

本阶段只检查文档和 schema 边界。除非 01-08 已经全部稳定，并且用户明确要求，否则不要新增 agent node executor。

## Agent Node 的未来语义

Agent node 仍然是 node，不是第二套 runtime。

示例：

```json
{
  "id": "review-implementation",
  "type": "agent",
  "mode": "react",
  "agent_id": "reviewer",
  "goal": "Review the implementation and report grounded findings",
  "depends": ["implement-feature"],
  "allowed_tools": ["file.read", "terminal.run"],
  "acceptance": "Findings are grounded in file/line references, or explicitly state no issues"
}
```

未来 agent node 必须遵守：

- 同一套 TaskGraph depends/status。
- 同一套 node acceptance/verifier。
- 同一套 allowed_tools。
- 同一套 tool policy/redaction/human confirm。
- 同一套 trace/session/recovery/memory observe。
- completed verified node 不重跑。

## 与 Skill Node 的区别

Skill node：

- 绑定一个已注册 skill package。
- 主要提供 domain instruction、allowed tools、inputs/outputs。
- 可是 prompt/react/script。

Agent node：

- 绑定一个本地 role profile。
- 主要改变 model/system prompt/tool subset/review stance。
- 仍执行一个可验收子任务。

两者都不是分布式 agent，也不是 gateway routing。

## 本阶段必须完成

### TODO 1：稳定文档边界检查

检查文件：

- `docs/task-graph-runtime.md`
- `docs/architecture.md`
- `docs/execution-flow.md`
- `docs/embedding-and-app-runtime.md`
- `README.md`
- `README.zh.md`
- `docs/roadmap.md`

要求：

- 说明 Mateway 支持未来 local agent node。
- 说明 distributed multi-agent orchestration 不在 core 范围。
- 删除或修正 “no DAG runtime / 无 DAG runtime” 等过期表述。
- 不承诺 subagent spawn/supervisor。

测试/验证：

- `rg -n "no DAG|无 DAG|distributed multi-agent|subagent|supervisor|multi-tenant" docs README.md README.zh.md`
- 人工确认语义没有冲突。

### TODO 2：Schema 预留检查

如果前面阶段已经允许 node `type=agent`：

- 确认 validator 不会在当前未实现 executor 时静默执行。
- `type=agent` 应 blocked/unsupported，或只在 future doc 中保留。

如果当前 schema 未允许 `type=agent`：

- 本阶段不要强行加入，除非用户明确要求。
- 文档保留未来方向即可。

测试：

- 如果加了 `type=agent`，必须有 unsupported/blocked 测试。
- 如果未加，必须确保现有 runtime 不依赖 agent node。

### TODO 3：防止引入 supervisor 概念

要求：

- 不新增 `supervisor`、`spawn`、`subagent`、`worker pool` 等核心代码概念。
- 不新增 gateway 层业务路由。
- 不新增 distributed queue。

验证：

- `rg -n "supervisor|subagent|spawn|worker queue|multi-tenant|distributed" internal docs dev-notes`
- 如果命中，只能是文档中的“非目标/不做”或已有无关上下文。

## 主链路接入要求

本阶段没有新的 runtime 主链路实现。它只确保未来 agent role 的方向不会破坏当前链路：

```text
Planner -> TaskGraph -> Scheduler -> Node Executor -> Verifier -> Finalizer -> Memory
```

未来 agent node 只能作为 Node Executor 的一种 mode/type 扩展。

## 禁止事项

- 不实现多 agent。
- 不实现 subagent spawning。
- 不实现 distributed supervisor。
- 不实现 gateway business routing。
- 不实现 worker queue。
- 不为了未来 agent node 修改现有 task execution path。
- 不让 agent role 绕过 skill metadata、tool policy、trace、verifier。

## 验收标准

- 稳定文档说明 local agent node 是未来可扩展方向。
- 稳定文档说明 distributed multi-agent orchestration 不在 Mateway core 范围。
- 当前 runtime 不依赖 agent node。
- 没有引入 supervisor/subagent/worker queue 代码概念。
- `go test ./...` 通过；如果本阶段只改文档，可不跑全量测试，但要执行 `rg` 检查。

## 集成闸门检查

对照 `10-integration-gates.md`，本阶段必须满足：

- 不改变 01-08 已建立的主链路。
- 不新增跨组件胶水债务。
- 不把未来 agent node 当作当前阶段 blocker。
- 不把 external scheduler 的职责塞进 Mateway core。

## 交给 OpenCode 的提示词模板

```md
请先读取并遵守根目录 `AGENTS.md`，然后读取：

- dev-notes/task-graph-runtime/00-architecture-overview.md
- dev-notes/task-graph-runtime/10-integration-gates.md
- dev-notes/task-graph-runtime/09-agent-node-future.md

只执行 Phase 09。默认这是文档和边界验证阶段。

TODO checklist:
- [ ] 检查 stable docs 和 README，确认 local agent node 与 distributed orchestration 的边界表述正确。
- [ ] 如果仍有过期的 "no DAG runtime / 无 DAG runtime" 表述，删除或修正。
- [ ] 确认 schema/runtime 不依赖 agent node；如果允许 `type=agent`，未实现 executor 时必须 blocked，并有测试。
- [ ] 运行 rg 检查 supervisor/subagent/spawn/worker queue/multi-tenant/distributed 等词，确保命中只出现在非目标或外部边界说明中。

不要实现 multi-agent、subagent spawning、distributed scheduler、worker queue 或 gateway business routing。
如果看起来需要改代码，先停止并报告，不要直接编辑。
```
