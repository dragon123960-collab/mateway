# 06 Skill Metadata 与注册

## 目标

通过本地注册让 skills 可以被 Planner 消费，并在 runtime 中安全执行。裸 `SKILL.md` 是草稿；已注册 skill 必须带 `.mateway/metadata.yaml`。

## 规则

Skill 只有同时存在以下文件时才可发现/可执行：

```text
SKILL.md
.mateway/metadata.yaml
```

Agent-scoped skill 覆盖同名 shared skill。

## Metadata V2 最小形态

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

完整 JSON Schema 输入/输出验证暂缓。

## 执行含义

- `prompt`：通常是 direct model call，并注入 skill instruction。
- `react`：使用 skill instruction 和 allowed tools 跑 node-local ReAct。
- `script`：执行 skill package 中的确定性脚本/API。
- `legacy`：可以作为 prompt 背景，但不应当作复杂可执行 node。

## 待办

- [ ] 实现 install/register/doctor 的 metadata 生成或 repair proposal。
- [ ] runtime discovery 忽略未注册 skills。
- [ ] Planner 只消费 metadata summary，不扫描 raw skill body。
- [ ] Node executor 只有选中 skill node 后才读取 `SKILL.md`。
- [ ] 增加 shared 和 agent-scoped registered skill 测试。

## 验收标准

- 手动复制的裸 `SKILL.md` 注册前不可发现。
- `skill install` 会写 metadata。
- `skill register` 会校验并写 metadata。
- Planner 能看到 skill inputs/outputs/type/allowed_tools。
- Skill node 缺 metadata 时拒绝执行。

## 非目标

- 不做完整 marketplace。
- 不做完整 JSON Schema validation。
