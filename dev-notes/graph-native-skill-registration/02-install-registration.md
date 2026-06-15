# 阶段 2：安装时注册

## 目标

更新 `mateway skill install <source>`：安装公共或本地 skill 时，始终写入 `SKILL.md` 和 `.mateway/metadata.yaml` v2，让安装后的 skill 立即成为已注册 skill。

## 当前入口

- `cmd/mateway/skill_cmd.go`
  - `runSkill`
  - `skill install`
- `internal/skill/skill.go`
  - `Install`
  - `InstallInput`
  - `InstallResult`
  - `writeMetadata`
- `internal/skill/skill_test.go`
- `cmd/mateway/main_test.go`

## 实现 TODO

- [ ] 将 `Install` 写出的 metadata `adapter_version` 从 `"1"` 升级为 `"2"`。
- [ ] 安装时解析 `SKILL.md` header，并写入 graph metadata：
  - `source` 保留原始 source。
  - `tool_runtime=mateway`
  - `graph.mode=adapted` 或 `legacy`，第一版推荐本地/公共安装默认 `legacy`。
  - `graph.stage` 来自 header，缺失则默认 `execution`。
  - `graph.node_type=skill`
  - `graph.granularity=atomic`
- [ ] 保留当前 secret-like 校验和 unsafe prompt marker 校验，不降低安全边界。
- [ ] CLI 输出继续打印 `metadata:` 路径。
- [ ] 更新 install 相关测试，断言 metadata v2 graph 字段存在。

## 测试 TODO

- [ ] `skill.Install` 从本地 `SKILL.md` 安装后写入 metadata v2。
- [ ] metadata graph stage 从 header 继承。
- [ ] 安装重复 skill 仍然报错，`--force` 行为保持原样。
- [ ] secret-like skill 内容仍被拒绝，且不产生部分文件。
- [ ] CLI `mateway skill install` 输出包含 skill path 和 metadata path。

## 非目标

- 不新增 `skill register`。
- 不改变 `skill list` 的发现逻辑。
- 不改 runtime discovery。

## Codex Review 重点

- 安装流程是否在写入前完成安全校验。
- 是否避免重写或改造原始 `SKILL.md` 内容。
- metadata 默认值是否保守，不能把 legacy skill 自动当作复杂 graph-native workflow。

