# 临时产物与清理机制

## 为什么测试 Markdown 会落在项目里

如果 agent 在 `terminal.run` 里执行类似：

```bash
printf 'smoke test...' > .feishu-docs-smoke.md
```

这个相对路径会落到当前工作目录。Mateway 项目调试时，当前工作目录通常就是仓库根目录 `/Users/dongping/project/mateway`，所以 smoke test Markdown 会出现在项目里。

这类文件不是功能产物，也不是 skill 版本文件，只是 agent 做真实测试时临时创建的输入文件。

## 为什么不能直接删除

当前 `tool_policy_hook` 会直接 block destructive terminal command，例如 `rm`、`rmdir`、`git clean`。这是故意保留的硬边界：Mateway 已经去掉聊天审批 pending，所以危险操作不能靠“再问用户一次”来放行。

因此，agent 如果创建了临时文件，再尝试用 `rm` 清理，会被工具策略拒绝。这不是模型不愿清理，而是当前工具边界没有给它一个安全删除通道。

## 当前建议

默认做法仍然是减少临时残留：

- 测试输入优先放到 `~/.mateway/tmp` 或明确的 scratch 目录。
- Skill wrapper 内部优先使用语言自带临时文件机制，并在脚本内部完成清理。
- 不要把 smoke test、trace 摘要、临时 Markdown 放到项目根目录。
- 如果确实需要清理 agent 产生的文件或目录，使用 `file.delete`，不要绕过 destructive terminal block。

## `file.delete`

Mateway 提供 `file.delete` 作为窄边界清理工具，而不是放开 `terminal.run` 的删除命令。

- 可以删除普通文件。
- 可以删除目录，但必须显式传入 `recursive=true`。
- 路径必须先通过 `security.enforce_workspace_paths` / `accessible_paths` 这一套 allowed roots 校验。
- 删除工具会额外验证真实路径，防止 `../../`、symlink、`/var` 到 `/private/var` 这类路径转换造成越界。
- 拒绝删除 allowed root 本身。
- 拒绝删除受保护的 runtime state：`config`、`run`、`secrets`、`trace`、`sessions`、`schedules`、`indexes`、`logs`、`observe`、`memory`。
- 拒绝删除 `.git`、`.hg`、`.svn` 这类版本控制目录。
- Evidence 会记录 resolved path、kind、recursive、bytes 或 entries、deleted=true。

这样 agent 可以处理自己产生的临时残留，同时不会获得“删除任意文件”的宽能力。
