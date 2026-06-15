# 阶段 6：Heartbeat 修复建议

## 目标

让 heartbeat 或维护任务发现 orphan skills，并提出注册建议。默认不静默写 metadata。

本阶段依赖阶段 3 的 register 能力和阶段 4 的 doctor 报告。

## 当前入口

- `internal/memory/heartbeat.go`
- `internal/memory/learning_heartbeat.go`
- `cmd/mateway/memory_cmd.go`
- `internal/skill/proposal.go`
- `internal/skill/skill.go`

## 目标行为

- Heartbeat 可以复用 `skill.Doctor` 识别 orphan skills。
- 默认结果是报告或 proposal，不直接写 `.mateway/metadata.yaml`。
- 用户确认后才调用 register path 或 promote proposal。
- 普通 runtime task execution 不触发自动注册。

## 实现 TODO

- [ ] 在 heartbeat skill learning 结果中加入 orphan skill 统计。
- [ ] 当发现 orphan skill 时，生成清晰建议：
  - `mateway skill register <name>`
  - 或生成 metadata proposal，取决于现有 proposal 机制是否适合。
- [ ] 保持 heartbeat 默认只读；不要在 background run 中直接注册。
- [ ] 如果实现 proposal，proposal 内容只覆盖 `.mateway/metadata.yaml`，不修改 `SKILL.md`。
- [ ] CLI 输出 heartbeat 结果时展示 orphan skill 数量和建议。

## 测试 TODO

- [ ] heartbeat 发现 orphan skill 并报告。
- [ ] heartbeat 不写 metadata。
- [ ] 已注册 skill 不产生修复建议。
- [ ] 如果生成 proposal，proposal 不包含 secret-like 内容。
- [ ] 聚焦测试：`go test ./internal/memory ./internal/skill ./cmd/mateway`。

## 非目标

- 不在 runtime discovery 中修复。
- 不静默注册。
- 不改变 memory distill 或 skill learning 的核心行为。

## Codex Review 重点

- background 任务是否保持非侵入。
- 建议是否足够明确，能导向 `skill register`。
- 是否避免把 skill body 或 secret-like 内容写入 proposal/trace。

