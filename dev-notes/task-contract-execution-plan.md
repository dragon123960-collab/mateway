# P0 TaskContract 闭环加固开发计划

## 背景

工具精简后，非默认工具不再靠自然语言关键词触发。`TaskContract` 成为单任务执行里最重要的结构化约束：它声明当前任务需要哪些工具、哪些证据，以及什么时候可以完成。

本计划只加固单 agent 的执行闭环，不把 `TaskContract` 扩展成 workflow router、DAG planner 或 gateway 业务路由。

## 当前基线

- default visible 工具保持为 `file.read`、`file.write`、`file.edit`、`terminal.run`、`web.search`、`web.fetch`、`toolresult.read`。
- 非默认工具通过 `TaskContract.RequiredTools` / `PlanItems[].Tool`、明确 runtime control path 或 recent tool 延续拉入。
- contract tools 优先进入可见工具集，不应被 `max_visible_tools` 截断。
- `ShouldStopAfterTurn` 会在 contract unsatisfied 时注入 follow-up，但重复缺证据的上限、trace 和最终 blocker 还需要加固。

## 目标

- contract 生成只表达当前任务最小必需工具和 evidence。
- contract tools 在模型工具列表中稳定可见。
- contract 要求不可用工具时，有稳定 trace 和用户可理解的 blocker。
- 模型空承诺、缺证据、重复 follow-up 都不会让任务假完成或无限循环。

## 实现要点

### 1. Contract 生成边界

- 保持 `TaskContract` 为 completion gate，不新增 DAG 节点、依赖边、分支条件。
- `RequiredTools` 只放当前任务必要工具，不把“可能有用”的工具塞进去。
- `PlanItems[].Tool` 只用于单步工具意图，不用于表达 workflow routing。
- contract 生成失败或为空时，runtime 应降级到现有 default visible 工具面，而不是猜测非默认工具。

### 2. Contract 工具可见性

- contract tools 必须先于非 contract 工具进入 visible set。
- `max_visible_tools` 只限制非 contract/default 工具预算，不限制 contract tools。
- contract tool 被 profile deny 时，不应静默消失；trace 写入 deny 原因。
- contract tool 不存在时，trace 写 `context_budget_missing_contract_tools`，并保留 missing tool name。

### 3. 缺证据 Follow-up

- `validateTaskContract` 输出的 missing evidence 应可映射到具体 tool/evidence requirement。
- follow-up 文案只要求“最小下一步工具调用或具体 blocker”，避免重新规划一大段。
- 模型连续空承诺时，deliverable gate 和 contract gate 不应互相覆盖，应保留最具体的缺口原因。

### 4. 最终 Blocker

- repeated missing evidence 到达上限后，最终回复必须说明：
  - 缺哪个 tool/evidence。
  - 已经尝试 follow-up 的次数。
  - 是 missing tool、profile deny、工具失败，还是模型没有提供证据。
- 当前 open task 状态体系只有 `running`、`await_user_input`、`failed`、`resuming`。除非单独修改 task schema、CLI/TUI 渲染和恢复逻辑，否则不要直接新增 `blocked` status；可以先用 `failed` + blocker evidence/trace 表达 blocked with reason。

## 验收标准

- contract 要求多个工具且超过 `max_visible_tools` 时，所有 contract tools 仍可见。
- contract 要求不存在工具时，trace 写 missing contract tools，最终回复包含具体 blocker。
- contract tool 被 profile deny 时，trace 和 final 都能说明 deny 原因。
- 模型只说“我会执行”但没有工具证据时，不允许 completed。
- repeated missing evidence 不无限循环，到达上限后有明确 blocker。
- completed task 清空 active；failed/open task 能继续 steering；`/new` 明确重置。

## 测试清单

- ✅ `TestContractToolsBypassVisibleToolBudget` — `max_visible_tools=2`，contract 要 3 个工具 → 3 个全可见
- ✅ `TestMissingContractToolProducesBlocker` — contract 要求不存在工具 → 立刻 `contract_tool_unavailable` blocker，不走 follow-up loop
- ✅ `TestProfileDeniedContractToolProducesBlocker` — contract 工具被 profile deny → 立刻 blocker
- ✅ `TestUnexecutedPromiseDoesNotCompleteTask` — 模型空承诺不完成；deliverable gate follow-up + 最终标记 failed
- ✅ `TestRepeatedMissingEvidenceStopsWithBlocker` — `TestContractFollowupLimitProducesBlockedTask` 已实现
- ✅ `TestCompletedTaskClearsActiveState` — task 完成后 `ActiveTask` 为空，状态 completed

## 非目标

- 不做 multi-agent supervisor。
- 不做 DAG routing / workflow platform。
- 不恢复关键词工具触发。
- 不新增工具。
- 不改变 channel/gateway 的 session routing 边界。

## 开发顺序

1. 先补 contract tools visible budget 测试。
2. 再补 missing/denied contract tool trace 和 blocker。
3. 然后加 repeated missing evidence 上限的可配置和测试。
4. 最后回归 active task 状态和 `/new` 行为。
