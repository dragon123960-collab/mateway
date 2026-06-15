# 阶段 1：Metadata v2 类型与校验

## 目标

为 `.mateway/metadata.yaml` 增加 graph-native 注册字段，让后续 install、register、doctor 和 runtime discovery 都消费同一套 typed metadata。

本阶段只改 metadata 类型、读写和校验，不改 runtime discovery 行为。

## 当前入口

- `internal/skill/skill.go`
  - `Metadata`
  - `ReadMetadata`
  - `writeMetadata`
  - `Install`
- `internal/skill/skill_test.go`

## 目标 metadata 形状

```yaml
adapter_version: "2"
source: "builtin | local | https://..."
installed_at: "2026-06-15T00:00:00Z"
tool_runtime: "mateway"

graph:
  mode: native | adapted | legacy
  stage: planning | execution | synthesis
  node_type: skill
  granularity: atomic | workflow
  allowed_tools:
    - web.search
    - web.fetch
  inputs:
    - topic
  outputs:
    - summary
  suggested_nodes: []
  safety_notes: []
```

## 实现 TODO

- [ ] 在 `internal/skill` 增加 `GraphMetadata` 类型，并挂到 `Metadata.Graph`。
- [ ] 增加 graph enum 常量或校验函数：
  - `mode`: `native`、`adapted`、`legacy`
  - `stage`: `planning`、`execution`、`synthesis`
  - `node_type`: 第一版固定 `skill`
  - `granularity`: `atomic`、`workflow`
- [ ] 新增默认构造函数，例如 `DefaultGraphMetadata(header Skill) GraphMetadata`：
  - 默认 `mode=legacy`
  - `stage` 优先使用 `SKILL.md` header 中的 `stage`，为空时默认 `execution`
  - `node_type=skill`
  - `granularity=atomic`
- [ ] 新增 metadata 校验函数，供 install/register/doctor 复用。
- [ ] 保持 v1 metadata 可读：`adapter_version: "1"` 且无 graph 时不报错，但可归一化为 legacy graph metadata。
- [ ] 增加或更新 `internal/skill/skill_test.go` 测试。

## 测试 TODO

- [ ] `ReadMetadata` 能读取带 graph 的 v2 metadata。
- [ ] v1 metadata 无 graph 时仍可读取。
- [ ] 无效 enum 值能被校验函数识别。
- [ ] 默认 graph metadata 会从 header 继承 stage。

## 非目标

- 不改 CLI 子命令。
- 不改 runtime skill discovery。
- 不改默认 assets。

## Codex Review 重点

- 类型是否足够明确，避免用 `map[string]any` 承载 graph 字段。
- v1 metadata 是否仍可读取。
- 校验是否只校验 metadata，不读取 secret 或 runtime state。

