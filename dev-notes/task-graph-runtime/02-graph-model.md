# 02：TaskGraph Model 与 Validator

更新：2026-06-15

## 目标

定义 Task Graph Runtime 的最小持久化模型和 validator，为后续 planner、scheduler、node executor、recovery 和 memory 提供稳定数据结构。

本阶段只做 schema、session 挂载、JSON 持久化和 validator，不接入主执行路径。

OpenCode 开发本阶段时只需要读：

1. [00：Task Graph Runtime 总开发文档](./00-architecture-overview.md)
2. 本文档

不要从 03-10 推断额外功能。

## 当前机制参考

当前可复用结构：

- `internal/session/store.go`
  - `State`
  - `TaskNode`
  - `ExecutionFrame`
  - `TaskContract`
  - `TraceRef`
  - `Store.Save` / `Store.Load`
- `internal/runtime/continuation.go`
  - `ContinuationDecision`
  - `GraphID`
  - `NodeID`
  - `ContextRefs`
- `internal/runtime/task_plan.go`
  - 当前线性 `TaskContract` 和 plan item 可作为迁移参考

当前问题：

- `TaskNode` 只能表达当前线性执行 frame，不能表达 node 依赖。
- `TaskContract` 更像全局验收 checklist，不能稳定表达 node-level status/result/evidence。
- trace 还没有统一 `graph_id` / `node_id` 归属。

## 模型归属

第一版 graph 挂在 `session.TaskNode` 上。

建议：

```go
type TaskNode struct {
    ...
    Graph *TaskGraph `json:"graph,omitempty"`
}
```

原因：

- 一个用户任务对应一个主 graph。
- session 存储已经以 task 为主索引。
- completed graph 可以随 task archive，不需要额外 graph store。
- 后续如需多 graph history，可从 task-level graph 演进为 `State.Graphs`，但本阶段不做。

## 最小 Schema

建议放在 `internal/runtime` 或 `internal/session` 需要谨慎：

- 如果 model 需要被 session JSON 直接引用，优先放在 `internal/session`，避免 `session` 反向 import `runtime`。
- 如果先放 `internal/runtime`，则本阶段不能称为 session 持久化模型。

推荐本阶段放在 `internal/session`：

```go
type TaskGraph struct {
    ID        string          `json:"id"`
    TaskID    string          `json:"task_id"`
    Status    string          `json:"status"`
    Nodes     []TaskGraphNode `json:"nodes"`
    CreatedAt time.Time       `json:"created_at"`
    UpdatedAt time.Time       `json:"updated_at"`
}

type TaskGraphNode struct {
    ID            string         `json:"id"`
    Type          string         `json:"type"`
    Goal          string         `json:"goal"`
    Status        string         `json:"status"`
    Depends       []string       `json:"depends,omitempty"`
    Executor      string         `json:"executor,omitempty"`
    Input         map[string]any `json:"input,omitempty"`
    Output        map[string]any `json:"output,omitempty"`
    Attempts      int            `json:"attempts,omitempty"`
    ResultSummary string         `json:"result_summary,omitempty"`
    EvidenceRefs  []EvidenceRef  `json:"evidence_refs,omitempty"`
    FailureReason string         `json:"failure_reason,omitempty"`
    Acceptance    Acceptance    `json:"acceptance,omitempty"`
    VerifiedAt    time.Time      `json:"verified_at,omitempty"`
    CreatedAt     time.Time      `json:"created_at"`
    UpdatedAt     time.Time      `json:"updated_at"`
}

type EvidenceRef struct {
    Kind      string `json:"kind,omitempty"`
    TraceID   string `json:"trace_id,omitempty"`
    TracePath string `json:"trace_path,omitempty"`
    ToolName  string `json:"tool_name,omitempty"`
    Summary   string `json:"summary,omitempty"`
}

type Acceptance struct {
    Criteria string `json:"criteria,omitempty"`
    Verified bool   `json:"verified,omitempty"`
    Reason   string `json:"reason,omitempty"`
}
```

字段命名规则：

- Go 字段用清晰名词。
- JSON key 用 snake_case。
- 文档和 trace key 保持英文。

## 状态枚举

Graph status：

- `planned`
- `running`
- `awaiting_input`
- `blocked`
- `failed`
- `completed`

Node status：

- `pending`
- `ready`
- `running`
- `awaiting_input`
- `blocked`
- `failed`
- `completed`
- `skipped`

Node type：

- `model`
- `tool`
- `skill`
- `human_review`
- `human_confirm`

后续可扩展，但本阶段 validator 必须拒绝未知值。

## Validator 规则

`ValidateTaskGraph(graph)` 至少检查：

- graph 非 nil。
- `graph.ID` 非空。
- `graph.TaskID` 非空。
- `graph.Status` 是合法 graph status。
- graph 至少有一个 node。
- node ID 非空且唯一。
- node type 合法。
- node status 合法。
- node goal 非空。
- `depends` 中每个 ID 必须存在。
- node 不允许依赖自己。
- graph 不能有环。
- `tool` node 必须有 executor 或 tool name。
- `skill` node 必须有 executor、skill ref 或 input 中可定位 skill 的字段。
- `human_review` / `human_confirm` node 必须有 goal 或 acceptance criteria。

Validator 不做：

- 不检查真实 tool registry 是否存在。
- 不读取 skill 文件。
- 不调用模型。
- 不推断依赖。
- 不修改 graph。

## 与 TaskContract 的关系

`TaskContract` 不删除。

迁移含义：

- graph 是执行结构。
- node acceptance 是节点验收。
- task-level `TaskContract` 可以继续作为全局验收和旧机制兼容层。
- 后续 verifier 会把 node evidence 汇总到 task acceptance。

本阶段只建立字段关系，不改 completion evaluator 行为。

## 实现 TODO

- [ ] 将 `TaskGraph` / `TaskGraphNode` / evidence / acceptance 类型放到不会产生 import cycle 的包中，优先 `internal/session`。
- [ ] 在 `session.TaskNode` 增加 `Graph *TaskGraph` 持久化字段。
- [ ] 定义 graph status、node status、node type 常量和合法性函数。
- [ ] 实现 `ValidateTaskGraph`，覆盖 graph ID、task ID、status、node ID、type、status、goal、dependency、cycle 和基础 node-specific 字段。
- [ ] 增加只读 helper：`NodeByID`、`NodeIDs`、`ReadyCandidates` 不在本阶段实现。
- [ ] 保持当前 runtime 主路径不接 graph scheduler。
- [ ] 旧 `TaskContract` 行为不变。

## 测试 TODO

- [ ] JSON round-trip 保留 `TaskNode.Graph`、nodes、acceptance、evidence refs。
- [ ] `session.Store.Save` / `Load` 能持久化 graph。
- [ ] validator 接受单节点 `model` graph。
- [ ] validator 接受 diamond dependency graph。
- [ ] validator 拒绝 nil graph、空 graph ID、空 task ID、空 nodes。
- [ ] validator 拒绝非法 graph status 和 node status。
- [ ] validator 拒绝空 node ID、重复 node ID、空 goal、非法 node type。
- [ ] validator 拒绝未知 dependency、自依赖和 cycle。
- [ ] validator 拒绝缺 executor/tool name 的 `tool` node。
- [ ] validator 不修改输入 graph。

## 非目标

- 不实现 graph planner prompt。
- 不实现 scheduler。
- 不实现 node executor。
- 不把 graph 接入 `Runtime.Handle` 主路径。
- 不删除 `TaskContract`。
- 不引入 `runtimev2`、`workflowv2` 或新 runtime mode switch。

## Codex Review 重点

- graph model 是否真正随 session 持久化。
- 是否避免 `session` import `runtime` 的循环。
- validator 是否覆盖 status 和 dependency/cycle。
- 是否没有越界实现 scheduler/planner。
- 是否保持当前测试和旧 runtime 行为不变。
