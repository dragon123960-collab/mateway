# 阶段 4：Skill Doctor 与 orphan 报告

## 目标

新增 `mateway skill doctor`，扫描 skill 目录并报告注册状态。doctor 是诊断命令，不静默修复。

## 当前入口

- `cmd/mateway/skill_cmd.go`
- `cmd/mateway/doctor_cmd.go`
- `internal/skill/skill.go`
- `internal/skill/skill_test.go`
- `cmd/mateway/main_test.go`

## 报告范围

`skill doctor` 需要报告：

- registered：同时有 `SKILL.md` 和 `.mateway/metadata.yaml`
- orphan：有 `SKILL.md` 但缺 metadata
- broken：有 metadata 但缺 `SKILL.md`
- invalid：metadata 解析失败或 graph 字段非法

## 实现 TODO

- [ ] 在 `internal/skill` 新增 `DoctorReport`、`DoctorEntry` 和 `Doctor(workspace string)`。
- [ ] 扫描 shared skills 和 agent-scoped skills。
- [ ] 读取 metadata 并调用阶段 1 的校验函数。
- [ ] 不创建、不修改、不删除任何文件。
- [ ] 新增 CLI 子命令 `mateway skill doctor`。
- [ ] CLI 输出包含总数和每个异常条目，例如：
  - `orphan skill foo: run mateway skill register foo`
  - `broken metadata bar: missing SKILL.md`
  - `invalid metadata baz: graph.stage must be planning, execution, or synthesis`

## 测试 TODO

- [ ] registered skill 被统计为 registered。
- [ ] 裸 `SKILL.md` 被报告为 orphan。
- [ ] metadata without `SKILL.md` 被报告为 broken。
- [ ] invalid metadata 被报告为 invalid。
- [ ] doctor 不修改文件，可用文件 mtime 或缺失 metadata 断言。
- [ ] CLI 输出包含修复提示。

## 非目标

- 不把 doctor 接入主 `mateway doctor`，除非用户后续明确要求。
- 不自动执行 register。
- 不创建 proposal。

## Codex Review 重点

- doctor 是否真的是只读。
- 输出是否足够指导 OpenCode/用户下一步执行 `skill register`。
- 是否覆盖 shared 和 agent 两种 scope。

