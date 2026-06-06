# 工具与 Secret

## 命令执行

`terminal.run` 是唯一命令执行工具。

如果外部 skill、README 或安装文档里写的是 Bash、shell、CLI、command-line、terminal command，在 Mateway 中都应理解为使用 `terminal.run` 执行对应命令。不要为这些外部说法新增 `Bash`、`shell.run` 或其他执行工具。

已经删除：

- `script.run`
- `mateway script list`
- `mateway script report`
- `mateway script run`
- script registry

Skill 或 workspace 里的 helper script 都是普通文件。需要执行时，用 `terminal.run` 调用文件路径。

## Secret 注入

Agent 可以用 `secret.set` 写入 secret，但不能读取 secret 明文。

需要凭证的 terminal 命令应使用 `terminal.run.env_secrets` 注入环境变量：

```json
[
  {"id": "service/token", "env": "SERVICE_TOKEN"}
]
```

命令执行时会收到对应环境变量。Trace 和 evidence 只记录 secret id 与 env 名，不记录 secret value。

如果 command 字符串中包含已知 secret 明文，runtime 会拒绝执行。

## 危险命令

Destructive terminal commands 会被直接 block。当前不再有聊天审批 pending 流程，所以不要让 agent 通过 `rm`、`rmdir`、`git clean` 等命令清理产物。

清理需求应该走更窄的机制：

- 优先把临时文件创建在 `~/.mateway/tmp` 或 workspace scratch 目录。
- 对一次性 smoke 文件，优先由脚本自身使用临时文件 API 创建和清理。
- 如果需要 agent 主动删除文件或目录，使用 `file.delete`。它只允许删除 allowed roots 内的目标，目录必须显式 `recursive=true`，并会拒绝 `../../` 越界、symlink 越界、allowed root 本身、runtime state 和版本控制目录。

## Skill 指引

需要稳定执行的 skill 应写清楚具体 `terminal.run` 命令模板。Skill 不应再要求 agent 使用已经删除的 script registry 命令。

如果 skill 需要密钥：

- `SKILL.md` 只记录 secret id 和 env var。
- 用户提供真实密钥后，agent 用 `secret.set` 保存。
- 执行命令时用 `terminal.run.env_secrets` 注入。
- 不在 `SKILL.md`、trace、日志或普通命令参数里写明文 secret。
