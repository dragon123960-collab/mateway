# 11：真实模型端到端 Dogfood

更新：2026-06-15

## 目标

在阶段 01-10 完成后，用真实模型跑完整 Task Graph Runtime，验证新主链路能顺利完成任务，并确认 trace、session、verifier、finalizer、memory 都按预期记录。

这不是替代单元测试，而是 release 前的系统级验收。

## 当前机制参考

- `internal/runtime/runtime.go`
- `internal/runtime/graph_planner.go`
- `internal/runtime/node_executor.go`
- `internal/session/scheduler.go`
- `internal/session/node_verifier.go`
- `internal/memory`
- trace/session store

## Dogfood 原则

- 使用真实模型，不使用 heuristic/static model。
- 使用真实 workspace，但测试产物必须写到明确 scratch 目录。
- 不读取 secrets、sessions、trace raw dump 或 `~/.mateway/secrets`。
- 不执行危险命令。
- 所有真实动作必须经过 tool policy。
- 失败也算有效结果，但必须形成 concrete blocker 和可追踪 evidence。

## 必测任务

### 1. 简单问答

输入：

```text
用一句话解释 Mateway 的 Task Graph Runtime 是什么。
```

期望：

- planner 生成一个 `model` node。
- node verifier passed。
- graph finalizer 返回 completed。
- session 中 graph/node 状态完整。
- trace 包含 graph/node/verifier/finalizer 事件。

### 2. 本地文件工具任务

输入：

```text
在 scratch 目录创建一个 hello.txt，内容为 hello task graph，然后读取它并总结。
```

期望：

- graph 至少包含 `file.write`、`file.read`、`model`/synthesis node。
- 写入和读取是不同 atomic tool nodes。
- tool success 只是 evidence，node completed 来自 verifier。
- final reply 不包含 raw trace。
- session 记录 evidence refs。

### 3. 开发脚本并执行脚本

输入：

```text
在 scratch 目录写一个脚本，统计 input.txt 的行数和单词数，创建 input.txt 后运行脚本，并汇报结果。
```

期望 graph 形态：

```text
model node: design script
tool node: file.write input.txt
tool node: file.write script
tool node: terminal.run execute script
tool node: file.read or terminal.run collect output
model node: summarize result
```

验收：

- 脚本开发和脚本执行不能被吞进一个大 model/skill node。
- `terminal.run` 只执行明确脚本命令，不执行危险清理。
- verifier 检查脚本文件存在、执行成功、输出包含行数和单词数。
- finalizer 只声明 verified evidence 支撑的结果。

### 4. Registered Skill 任务

前置：

- 准备一个带 `.mateway/metadata.yaml` 的 atomic test skill。

输入：

```text
使用测试 skill 完成一个小任务，并说明它产出了什么。
```

期望：

- planner 只发现 registered skill。
- raw `SKILL.md` without metadata 不可发现或不可执行。
- skill node 读取 `SKILL.md` 作为 node-local instruction。
- skill usage memory 关联 verified skill node result。

### 5. Human Confirm / Blocked Resume

输入：

```text
准备一个会修改 scratch 文件的操作计划，执行前先让我确认。
```

期望：

- graph 中含 `human_confirm` node。
- runtime 返回 input-required reply。
- pending 包含 `task_id`、`graph_id`、`node_id`。
- 用户确认后 resume 同 node/graph。
- completed nodes 不重跑。

## 检查项

每个 dogfood case 都要检查：

- final reply style 和 graph status 一致。
- trace 包含 `continuation_decision`、`graph_planned`、`node_execute_start`、`node_verified`、`graph_finalized`。
- session graph 包含 node attempts、result summary、failure reason、evidence refs、verified_at。
- memory diary/learning 只在 task completion 后写入。
- skill usage 只记录 verified skill node。
- no raw secret-like content。

## Model Verifier 检查

如果 semantic acceptance 已接入 model verifier，还要测试：

- 中文 acceptance criteria 可以被正确判断。
- 英文 acceptance criteria 可以被正确判断。
- verifier malformed output 时 conservative blocked/failed，不 passed。
- model verifier 不执行工具，不修改 graph dependencies。

如果 semantic acceptance 仍是 deterministic fallback，则 dogfood 结果必须标记该限制，不能把它当最终验收。

## 测试脚本建议

后续可新增手动或半自动命令：

```text
mateway dogfood task-graph --case simple-qa
mateway dogfood task-graph --case file-tool
mateway dogfood task-graph --case script-build-run
mateway dogfood task-graph --case registered-skill
mateway dogfood task-graph --case human-confirm
```

第一版可以先写成手动 checklist，不强制新增 CLI。

## 验收标准

- 五类 dogfood case 至少各跑一次。
- 至少一个 case 使用真实工具写入 scratch 文件。
- 至少一个 case 使用 `terminal.run` 执行用户生成的脚本。
- 至少一个 case 产生 blocked 或 awaiting_input，并能 resume。
- trace/session/memory 记录完整。
- `go test ./...` 仍全绿。

## 非目标

- 不跑危险命令。
- 不访问真实 secrets。
- 不把 dogfood 产物写项目根目录。
- 不要求引入外部 workflow 平台。
- 不要求 dogfood 自动覆盖所有 edge cases。

## Codex Review 重点

- dogfood 是否真的走 graph runtime 主链路。
- 是否使用真实模型而不是 static/heuristic model。
- 记录是否完整且无敏感泄漏。
- 脚本开发/执行是否拆成 atomic nodes。
- blocked/resume 是否不重跑 completed nodes。
