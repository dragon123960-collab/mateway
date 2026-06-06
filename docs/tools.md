# 默认工具清单

这份文档记录 Mateway 默认 registry 中 agent 能看到的工具。模型实际接收的工具说明来自代码里的 `Description`、`Schema` 和 `ToolContract`；本文是便于开发者检查的中文索引。

## 文件与项目

| 工具 | 用途 | 关键边界 |
| --- | --- | --- |
| `file.read` | 读取本地文本文件 | 受 allowed roots 限制；拒绝目录、大文件和二进制文件。 |
| `file.write` | 创建或替换本地文本文件 | 受 allowed roots 限制；写 core agent profile 时走 proposal；不用于回答问题。 |
| `file.delete` | 删除本地文件或目录 | 删除工具单独强校验 allowed roots 和真实路径；目录必须 `recursive=true`；拒绝 runtime state、secret、trace、allowed root 和 VCS 目录。 |
| `project.index` | 列出项目目录下文件 | 只做目录索引；跳过 `.git`、`node_modules`、`dist`、`build`。 |

## 命令与 Secret

| 工具 | 用途 | 关键边界 |
| --- | --- | --- |
| `terminal.run` | 执行本地 shell 命令 | 唯一命令执行工具；外部 skill/README 里写 Bash、shell、CLI、command-line、terminal command 时，都映射到 `terminal.run`；destructive terminal command 被直接 block；可用 `env_secrets` 注入 secret。 |
| `secret.set` | 保存用户提供的 secret | 只能写入，不能读明文；拒绝占位符和空值；结果不回显 secret。 |

## Web

| 工具 | 用途 | 关键边界 |
| --- | --- | --- |
| `web.search` | 搜索网页 | 用于新鲜、当前、未知事实；返回结果摘要和 provider evidence。 |
| `web.fetch` | 抓取 URL 正文 | 用于读取已知页面；限制响应大小，返回正文摘要。 |

## 定时任务

| 工具 | 用途 | 关键边界 |
| --- | --- | --- |
| `schedule.create` | 创建本地定时任务 | 直接创建，不产生聊天审批 pending。 |
| `schedule.list` | 列出本地定时任务 | 只读。 |
| `schedule.update` | 更新任务文本、时间、间隔或状态 | 直接修改 scheduler state。 |
| `schedule.pause` | 暂停定时任务 | 可逆 mutation。 |
| `schedule.resume` | 恢复定时任务 | 可逆 mutation。 |
| `schedule.delete` | 删除定时任务 | 仅在用户明确要求删除时使用。 |
| `schedule.run_now` | 将任务标记为立即运行 | 只更新 scheduler due time，不代表当前对话直接执行任务内容。 |

## 任务找回

| 工具 | 用途 | 关键边界 |
| --- | --- | --- |
| `task.search` | 搜索当前和 archived session tasks | 找旧任务时先搜索；候选多个时让用户选择。 |
| `task.resume` | 加载历史 task context | 只读历史上下文，不修改 archive。 |

## 非默认工具

以下能力不在默认 registry 中：

- `script.run`
- `remote.profile.create`

Helper scripts 只是普通文件，需要执行时通过 `terminal.run` 调用文件路径；需要凭证时通过 `terminal.run.env_secrets` 注入。

## 外部 Skill 适配

从其他源安装的 skill 应尽量保留原始 `SKILL.md`，不要为了 Mateway 改写源内容。Mateway 专属适配信息放在：

```text
workspace/skills/<skill_name>/.mateway/metadata.yaml
```

典型字段：

```yaml
adapter_version: "1"
source: external
tool_runtime: mateway
notes:
  - Original SKILL.md is preserved for source compatibility and upgrade diffing.
```

如果外部 skill 的 frontmatter 里有其他 agent 生态的 `allowed-tools`，doctor 会优先看 `.mateway/metadata.yaml`；有 Mateway metadata 时保留原文，不把它当作需要改写的错误。
