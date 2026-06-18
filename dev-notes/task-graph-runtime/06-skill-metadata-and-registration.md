# 06 Skill Metadata 与注册

## 开发前必须先读

OpenCode 开始本阶段前必须先读：

1. `dev-notes/task-graph-runtime/00-architecture-overview.md`
2. `dev-notes/task-graph-runtime/10-integration-gates.md`
3. `dev-notes/task-graph-runtime/03-node-executor-local-react.md`
4. 本文档
5. 当前相关源码：
   - `internal/skill/*`
   - `internal/runtime/skill*.go`
   - `internal/runtime/planner*.go`
   - `internal/runtime/node_executor.go`
   - `internal/config/*`
   - CLI command registration 相关文件
   - default/builtin skills 初始化相关文件

本阶段目标是关闭“裸 `SKILL.md` 临场发现”的缺口。Skill 必须本地注册后才能被 Planner 看到、被 Executor 执行。

## 阶段目标

统一 skill 入口：

```text
skill install/register
  -> write SKILL.md
  -> write .mateway/metadata.yaml
  -> runtime discovery reads metadata only
  -> planner consumes metadata summary
  -> selected skill node executor reads SKILL.md
```

规则：

- 有 `SKILL.md` 但没有 `.mateway/metadata.yaml`：unregistered draft，不可发现、不可执行。
- `.mateway/metadata.yaml` 是 Mateway 本地注册表和 graph planner 索引。
- `SKILL.md` 保留人类可读原始内容。
- Agent-scoped skill 覆盖同名 shared skill。

## 当前代码基线

当前代码可能已经在 executor 层检查 metadata，但 runtime discovery 仍可能扫描裸 `SKILL.md`。本阶段要把 discovery、install/register/doctor、Planner skill summary、Executor skill load 全部统一。

不要只在 `executeSkillNode` 检查 metadata。那只能阻止执行，不能阻止 Planner 看见未注册 skill。

## Metadata v2 最小契约

YAML 示例：

```yaml
adapter_version: "2"
source: "builtin | local | https://..."
installed_at: "2026-06-17T00:00:00Z"
tool_runtime: "mateway"

graph:
  mode: native | adapted | legacy
  type: prompt | react | script
  stage: planning | execution | synthesis
  granularity: subtask
  inputs:
    - repo_path
  outputs:
    - architecture_summary
  allowed_tools:
    - file.read
    - terminal.run
  safety_notes: []
```

字段语义：

- `graph.mode=native`：按 Mateway graph 语义编写。
- `graph.mode=adapted`：公共 skill 已被本地化为 graph metadata。
- `graph.mode=legacy`：只作为提示词背景，不展开复杂执行。
- `graph.type=prompt`：通常走 direct。
- `graph.type=react`：走 node-local ReAct。
- `graph.type=script`：走确定性脚本/API。
- `graph.granularity=subtask`：skill 是一个可验收子任务，不是工具调用。

本阶段不要求完整 JSON Schema validation，但必须能 parse、校验必需字段、拒绝明显非法值。

## 注册流程契约

### `mateway skill install <source>`

要求：

- 读取公共或本地 source。
- 写入 `SKILL.md`。
- 同步生成 `.mateway/metadata.yaml`。
- 安装完成后立即可被 discovery 发现。
- install 前扫描 secret-like 内容和 unsafe prompt markers；失败时不注册。

### `mateway skill register <path|name>`

用于用户手动拷贝的 skill。

要求：

- 找到 `SKILL.md`。
- 校验无 secret-like 内容和 unsafe prompt markers。
- 生成 `.mateway/metadata.yaml`。
- 注册后才可被 discovery 发现。

### `mateway skill doctor`

要求：

- 扫描有 `SKILL.md` 但缺 metadata 的 orphan skills。
- 报告不可发现原因。
- 默认不写文件。
- 可以输出修复建议或 proposal。

### Heartbeat repair

要求：

- 可以报告 orphan skills。
- 默认只提出 proposal，不静默写 metadata。
- 用户确认后可调用 register。

## Discovery 契约

Runtime discovery 只读取 `.mateway/metadata.yaml`。

流程：

```text
scan skill roots
  -> find directories with .mateway/metadata.yaml
  -> require sibling SKILL.md
  -> parse metadata
  -> apply agent-scoped override
  -> expose compact summary to planner
```

Planner 不能扫描 raw `SKILL.md` body。只有 Executor 在某个 skill node 被选中后才能读取 `SKILL.md` 作为 node-local instruction。

## 安全契约

Install/register 必须拒绝：

- secret-like 内容。
- 明确要求绕过 policy/redaction/path validation 的 unsafe prompt marker。
- metadata 中声明未知 tool 或不允许 tool 时，必须报错或降级 blocked，不得默默放开工具池。

Skill 不能绕过：

- tool policy
- path validation
- redaction
- human confirm
- node verifier

## 本阶段必须完成

### TODO 1：实现 metadata parse/write/validate

可能涉及文件：

- `internal/skill/metadata.go`
- `internal/skill/*test.go`

要求：

- 读写 `.mateway/metadata.yaml`。
- 校验必需字段。
- 校验 graph mode/type/granularity。
- 保持 YAML 字段为英文机器 key。

测试：

- valid metadata parse。
- missing required field rejected。
- invalid graph type rejected。

### TODO 2：runtime discovery 改为 metadata-only

可能涉及文件：

- `internal/runtime/skill*.go`
- `internal/skill/discovery*.go`

要求：

- 裸 `SKILL.md` 不可发现。
- 缺 sibling `SKILL.md` 的 metadata 不可发现。
- Agent-scoped skill 覆盖 shared skill。
- Planner 只收到 metadata summary。

测试：

- raw `SKILL.md` ignored。
- registered shared skill discovered。
- registered agent-scoped same name overrides shared。

### TODO 3：skill install/register/doctor 接入 metadata

可能涉及文件：

- `internal/skill/install*.go`
- `internal/skill/register*.go`
- `internal/skill/doctor*.go`
- CLI command files

要求：

- install 写 `SKILL.md` 和 metadata。
- register 将 copied local skill 转为 registered skill。
- doctor 只报告，不改变 runtime state。

测试：

- install writes both files。
- register writes metadata。
- doctor reports orphan and does not mutate。

### TODO 4：Executor 延迟读取 SKILL.md

可能涉及文件：

- `internal/runtime/node_executor.go`
- `internal/runtime/skill*.go`

要求：

- Planner/discovery 阶段不读 skill body。
- selected skill node 执行时才读取 `SKILL.md`。
- 缺 metadata 或 metadata invalid 时 blocked。
- skill type prompt/react/script 正确路由。

测试：

- Planner sees summary without reading body，可用 fake reader 断言。
- execute selected skill reads body once。
- missing metadata skill node rejected。

### TODO 5：默认/builtin skills 生成 metadata

要求：

- `mateway init` 或默认 skill 安装路径写 metadata。
- 仓库内自带 skill fixtures 更新。
- 已有默认 skills、用户制作 skills、安装的 `agent-browser` 等应能通过 doctor 或 register 进入注册态。

测试：

- init 后默认 skills 可发现。
- doctor 对未注册旧 skill 给出明确报告。

## 主链路接入要求

完成本阶段后：

```text
registered skills metadata
  -> planner skill summary
  -> skill node selected
  -> executor reads SKILL.md
  -> node result/evidence
```

裸 `SKILL.md` 在这条链路中不可见。

## 禁止事项

- 不做 marketplace。
- 不做完整 JSON Schema engine。
- 不在 discovery 时读取 `SKILL.md` body。
- 不让 skill 声明自动扩大工具权限。
- 不为手动复制的裸 skill 自动静默注册。
- 不把 skill 当 tool name。

## 验收标准

- Discovery ignores `SKILL.md` without `.mateway/metadata.yaml`。
- Discovery includes registered shared and agent-scoped skills。
- Agent-scoped skill overrides shared skill with same name。
- `skill install` writes both `SKILL.md` and metadata。
- `skill register` converts copied local skill into discoverable skill。
- `skill doctor` reports orphan skills without mutating runtime state。
- Runtime graph planner only receives registered skills。
- Secret-like or unsafe skill content is rejected during install/register。
- `go test ./internal/skill ./internal/runtime` 通过。

## 当前实现状态

本阶段已完成最小闭环：

- `internal/skill` 支持 metadata v2 read/write/validation，包含 `graph.mode`、`graph.type`、`graph.stage`、`graph.granularity`、inputs/outputs/allowed tools。
- `skill.Install` 会写入 `SKILL.md` 和 `.mateway/metadata.yaml`，并复用 secret-like / unsafe prompt marker 校验。
- `skill.Register` 可将已有本地 `SKILL.md` 注册为 discoverable skill。
- `skill.Doctor` 可报告缺 metadata 或 metadata invalid 的 orphan skill，默认不写文件。
- runtime skill discovery 改为 metadata-only：裸 `SKILL.md` 不进入 Planner；缺 sibling `SKILL.md` 的 metadata 也不可发现。
- agent-scoped skill 在 discovery 顺序中覆盖 shared skill。
- executor 仍在选中 skill node 后才读取 `SKILL.md`，并按 metadata `graph.type` 路由 prompt/react/script。
- `mateway init` 会为默认 builtin skills 补 `.mateway/metadata.yaml`。

本阶段仍不做：

- marketplace / remote catalog installer adapter。
- 完整 JSON Schema engine。
- heartbeat 静默自动注册 orphan skill。
- deterministic script skill 的完整执行路径，仍留给后续 skill/script executor 收口。

## 集成闸门检查

对照 `10-integration-gates.md`，本阶段必须满足：

- Planner skill input 来自 metadata summary。
- Executor 选中 skill node 后才读 body。
- Skill node 仍走 node acceptance、trace、session、verifier。
- Skill 不能绕过 policy/redaction/human confirm。

## 交给 OpenCode 的提示词模板

```md
请先读取并遵守根目录 `AGENTS.md`，然后读取：

- dev-notes/task-graph-runtime/00-architecture-overview.md
- dev-notes/task-graph-runtime/10-integration-gates.md
- dev-notes/task-graph-runtime/03-node-executor-local-react.md
- dev-notes/task-graph-runtime/06-skill-metadata-and-registration.md

只实现 Phase 06。

TODO checklist:
- [ ] 增加 metadata v2 的 read/write/validation helpers。
- [ ] 将 skill discovery 改为 metadata-only；裸 `SKILL.md` 是不可见 draft。
- [ ] 实现或更新 skill install/register/doctor，使其按本文档创建或报告 metadata。
- [ ] 确保 planner 只消费 metadata summary，executor 只有选中 skill node 后才读取 `SKILL.md`。
- [ ] 确保 builtin/default skills 在 init/install 时生成 metadata。

必须包含 raw skill ignored、registered skill discovered、agent-scoped override、install/register writes metadata、doctor non-mutating、unsafe content rejection 的测试。
不要做 marketplace、JSON Schema engine 或 heartbeat 静默自动注册。
```
