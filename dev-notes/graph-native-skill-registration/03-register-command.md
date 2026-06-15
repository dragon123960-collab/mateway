# 阶段 3：手动注册命令

## 目标

新增 `mateway skill register <path|name>`，用于把用户手动拷贝到 workspace 的裸 `SKILL.md` 注册成本地 skill。

注册只创建 `.mateway/metadata.yaml`，不修改 `SKILL.md`。

## 当前入口

- `cmd/mateway/skill_cmd.go`
- `internal/skill/skill.go`
- `internal/skill/proposal.go`
  - `ValidateSkillContent`
- `internal/skill/skill_test.go`
- `cmd/mateway/main_test.go`

## 输入解析规则

`mateway skill register <target>` 支持：

- skill 目录：`workspace/skills/foo`
- skill 文件：`workspace/skills/foo/SKILL.md`
- workspace skill 名称：`foo`

第一版只要求 shared skill；agent-scoped skill 可通过显式路径注册。

## 实现 TODO

- [ ] 新增 `skill.RegisterInput` 和 `skill.RegisterResult`。
- [ ] 新增 `skill.Register(input RegisterInput)`：
  - 解析 target 到 `SKILL.md`。
  - 校验路径必须位于 workspace skill 根或 agent skill 根。
  - 读取并校验 `SKILL.md` 内容。
  - 解析 header，生成 metadata v2。
  - `source=local`
  - `graph.mode=legacy`
  - 不修改 `SKILL.md`。
- [ ] 新增 CLI 子命令 `mateway skill register <path|name>`。
- [ ] CLI 输出：
  - `skill: <name>`
  - `path: <SKILL.md path>`
  - `metadata: <metadata path>`
- [ ] 若 metadata 已存在，默认报错；可选 `--force` 覆盖。

## 测试 TODO

- [ ] 注册手动拷贝的 shared skill 会创建 metadata。
- [ ] 用 skill name 注册能解析到 `workspace/skills/<name>/SKILL.md`。
- [ ] 用显式 agent skill 路径注册能创建 agent-scoped metadata。
- [ ] 缺少 `SKILL.md`时报错。
- [ ] target 在 workspace 外时报错。
- [ ] secret-like 或 unsafe content 被拒绝。
- [ ] 默认不覆盖已有 metadata；`--force` 可覆盖。

## 非目标

- 不修改 runtime discovery。
- 不自动扫描所有 orphan skills。
- 不生成 skill proposal。

## Codex Review 重点

- 路径校验必须严格，不能允许 workspace 外文件被注册。
- 注册不能改变 `SKILL.md`。
- 错误信息要明确指出如何修复。

