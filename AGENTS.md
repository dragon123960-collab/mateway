# AGENTS

## 基本规则

- 先读当前任务相关代码和文档，再判断要改什么；不要只凭记忆或猜测改动。
- 只做用户当前要求的事，不顺手扩展功能、不做无关重构、不改无关格式。
- 优先沿用仓库已有架构、包边界、命名和测试风格。
- 修改代码前先确认工作区状态；遇到别人已有改动，不要覆盖、回滚或整理。
- 除非用户明确要求，不要执行破坏性 Git 操作，例如 `reset --hard`、强制 checkout、删除分支。
- 新增或修改 Go 代码后必须 `gofmt`。
- 能用窄范围测试验证的，先跑相关包测试；影响 runtime、tool、session、gateway 等共享行为时，再扩大测试范围。
- 代码中不要包含中文关键词等中文内容，除非是测试。

## 架构限制

- 当前主线是小型 local-first Go agent runtime，不要扩成 heavy workflow platform。
- 不要实现 multi-agent supervisor、spawn/subagent、DAG routing 或 gateway 业务级多 agent 路由，除非用户明确要求。
- 不要复制旧实验代码，例如 `runtimev2`、`agentv2`、`toolv2`、`workflowv2`。
- AgentCore 循环保持 transcript-driven：模型回合、工具调用、观察、最终回复。
- 工具层负责真实动作和硬安全边界；runtime hook 负责 context、tool policy、observe、response。
- Channel 包只做 I/O：接收、归一化、发送、反应；runtime 调用、session routing、reaction policy 放在 `internal/gateway`。

## 文档规则

- 稳定说明放在 `docs/`，临时观察、dogfood、当前状态和 TODO 放在 `dev-notes/`。
- 修改公开介绍、安装、命令、配置、功能列表、使用方式时，必须同步更新 `README.md` 和 `README.zh.md`。
- 修改稳定机制说明时，必须同步检查 `docs/README.md` 的文档地图是否需要更新。
- 不要让文档保留已失效的工具名、trace key、配置项、路径或测试结论。
- 中文说明可以写在文档里；tool name、trace key、config key、命令、机器可读示例尽量保持英文。

## 安全限制

- 不要把 secret、token、cookie、私钥、运行态配置、trace dump 或本地 session 数据提交到仓库。
- secret-like 内容必须在 trace、stored transcript、task step 和 final reply 中脱敏。
- `~/.mateway/secrets` 永远不可读；`sessions`、`trace`、`run`、`observe` 等运行态目录默认按敏感数据处理。
- 测试临时文件不要写到项目根目录；优先使用 `~/.mateway/tmp` 或明确的 workspace scratch 路径。
- 不要用 `rm` 做清理；需要清理时优先使用带路径校验的 `file.delete`。
- destructive 操作必须依赖工具 policy 和路径校验，不能只依赖聊天确认。

## 回答限制

- 不要声称已经完成实际动作，除非有工具结果、文件 diff、测试输出或明确 blocker。
- 如果模型只是计划“我会检查/运行/修改”，必须继续执行工具或说明无法执行的具体原因。
- 用户问 Mateway 自身配置、工具、安全或架构时，先读本地 `docs/`、`dev-notes/` 和源码，再考虑 web search。
- 最终回复要简短说明改了什么、验证了什么、还有什么未做；不要堆无关过程。
