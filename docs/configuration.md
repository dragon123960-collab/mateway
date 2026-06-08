# 配置说明

Mateway 从 `~/.mateway/config/` 读取本地配置。配置目标是保持小而稳定：runtime 只保留执行所需的通用开关，不再保留 followup/review/approval/i18n/script 这类已经删除的运行层配置。

## 根配置

当前稳定保留的根配置：

- `app.name`
- `app.home`
- `app.workspace`
- `model.default`
- `model.fallbacks`
- `model.roles.vision`
- `model.roles.strong`
- `execution.max_parallel_tools`
- `execution.max_iterations`
- `execution.inactivity_timeout`
- `memory.enabled`
- `memory.root`
- `memory.recent_days`
- `memory.auto_propose`
- `memory.proposal_nudge`
- `learning.enabled`
- `learning.skill_crystallization`
- `skills.catalogs`
- `scheduler`
- `security.enforce_workspace_paths`
- `security.accessible_paths`
- `security.terminal_sandbox`
- search、provider、channel、model-specific configs

已经删除或不应再出现在默认配置中的键：

- `app.locale`
- `app.message_catalog_dir`
- `model.roles.followup`
- `model.roles.review`
- completion-review / no-progress execution knobs
- chat approval config
- `scripts`
- remote profile confirmation config

## 语言策略

Prompt guidance、few-shot examples、config keys、trace keys、tool names 和 machine-readable output 保持英文。用户可见回复由模型根据用户消息自然决定语言。

Runtime 不再使用本地化短语 alias 来触发 action。尤其不要通过“保存/忽略/save/ignore”等短语让 runtime 分支；需要机器判定的 pending 控制只接受明确的数字或结构化字段。

## 已存在的 HOME 目录

`mateway init` 会补齐缺失的默认文件，但不会覆盖已有 workspace skills 或用户编辑过的文件。已经存在的 `~/.mateway` 在 runtime 精简后可能仍保留旧 skill guidance、旧配置片段或旧 trace，需要显式检查和同步。

`mateway doctor` 会报告：

- 默认工具 registry 是否仍包含已删除工具。
- 本地 skills 是否还引用 `script.run` 或 `mateway script`。
- 外部 skills 是否缺少 `.mateway/metadata.yaml` 适配信息。
- config 是否还包含已删除配置键。
- 默认模型、agent、目录、tool contract、workspace skills 是否存在明显漂移。
