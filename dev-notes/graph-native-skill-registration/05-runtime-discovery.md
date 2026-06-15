# 阶段 5：Runtime 只发现已注册 skill

## 目标

将 runtime skill discovery 从扫描裸 `SKILL.md` header 改为只扫描已注册 metadata。裸 `SKILL.md` 变成 unregistered draft，不参与 runtime planning。

这是行为变化阶段，需要重点测试 runtime 和 contract 相关用例。

## 当前入口

- `internal/runtime/skill_discovery.go`
  - `discoverSkillsForAgent`
  - `discoverSkillsInRoot`
  - `skillsPrompt`
  - `readSelectedSkillBodies`
- `internal/runtime/task_contract.go`
- `internal/runtime/system_context.go`
- `internal/runtime/runtime_test.go`
- `internal/runtime/delivery_regression_test.go`

## 目标行为

- Discovery 只返回同时存在 `SKILL.md` 和 `.mateway/metadata.yaml` 的 skills。
- Planner prompt 使用 metadata 摘要，不直接从裸 `SKILL.md` header 推断。
- 选中 skill 后，仍读取 `SKILL.md` body 作为 node-local 或 contract-selected skill context。
- Agent-scoped registered skill 覆盖 shared registered skill。
- Runtime discovery 不写文件。

## 实现 TODO

- [ ] 修改 `discoverSkillsInRoot`：先找 metadata，再确认同目录 `SKILL.md` 存在。
- [ ] `discoveredSkill` 增加必要 metadata 字段，例如 graph mode、granularity、metadata path。
- [ ] 从 metadata 填充 name、stage、description、priority；缺失时可读取 header 作为补充，但只有 metadata 存在才允许。
- [ ] invalid metadata 不应 panic；第一版可跳过，并在 trace 或测试辅助中暴露原因。
- [ ] 更新 `skillsPrompt`，强调 registered skill 和 graph metadata。
- [ ] 更新 runtime tests 中创建 skill fixture 的 helper，让测试 skill 同时写 metadata。
- [ ] 新增测试证明裸 `SKILL.md` 不再被发现。

## 测试 TODO

- [ ] `discoverSkillsForAgent` 忽略无 metadata 的 `SKILL.md`。
- [ ] 已注册 shared skill 可被发现。
- [ ] 已注册 agent skill 可被发现，并覆盖同名 shared skill。
- [ ] metadata 缺 `SKILL.md` 时不发现。
- [ ] contract prompt 只包含已注册 skill。
- [ ] 现有 skill gating、selected skill trace、skill body read 相关测试仍通过。
- [ ] 运行聚焦测试：`go test ./internal/runtime ./internal/skill`。

## 非目标

- 不实现 TaskGraph runtime。
- 不改变 skill read gate 的核心安全语义。
- 不把 skill name 放入 required_tools 或 plan_items[].tool。

## Codex Review 重点

- Runtime discovery 是否保持只读。
- 旧测试 fixture 是否只是补 metadata，而不是削弱断言。
- 是否没有扩大 runtime 对 skill body 的默认注入范围。

