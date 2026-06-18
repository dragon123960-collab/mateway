# 配置

Mateway 默认在 `~/.mateway` 下存储本地运行时数据。`mateway init` 从 `assets/init` 创建初始配置、workspace、skills 和 memory 模板。

## 主目录布局

```text
~/.mateway/
  config/
    config.yaml
    mateway.env
    models/
    channels/
  workspace/
    agents/
    skills/
    memory/
  sessions/
  trace/
  observe/
  indexes/
  run/
```

## 主配置

`config/config.yaml` 选择默认值和本地运行时行为：

- `app.home`: Mateway 主目录。
- `app.workspace`: 用于 profiles、skills 和 memory 的工作区根目录。
- `model`: 默认模型、备用模型和角色模型。
- `agents`: agent profiles 和渠道绑定。
- `channels`: 飞书和微信配置。
- `security`: 工作区路径强制、可访问路径和终端沙箱设置。
- `search`: 网页搜索供应商和预算。
- `execution`: TaskGraph / AgentCore 执行预算和上下文预算。
- `memory`: 任务完成后的 memory observe 和 proposal nudge 设置。
- `learning`: learning distill 和 skill crystallization 设置。
- `skills`: 公共 skill catalog 搜索入口。
- `remote`: 终端远程 profile 白名单。
- `scheduler`: heartbeat 本地调度循环设置。

模型定义位于 `config/models/*.yaml`。渠道定义位于 `config/channels/*.yaml`。

## Execution

`execution` 当前稳定字段：

- `max_parallel_nodes`: TaskGraph Scheduler 每个 tick 最多同时执行多少个 ready nodes。默认 `1`，需要真实 node 并行时可调大。
- `max_parallel_tools`: 单个 node-local AgentCore loop 内部可用的工具并发预算。它不是 graph node 并发。
- `max_iterations`: node-local AgentCore loop 的最大迭代次数。
- `inactivity_timeout`: runtime 活动看门狗超时。
- `max_contract_followups`: observe/completion follow-up 的上限，保留用于旧 contract hook 兼容。
- `model_verifier`: node 验收时模型 verifier 的调用策略。默认 `fallback`，deterministic verifier 已通过时不再调用模型；可设为 `always` 强制每个带 acceptance 的已通过 node 再做模型语义验收；`off`/`never` 表示关闭模型 verifier。
- `context_budget`: 输入上下文裁剪和工具结果压缩预算。

`max_parallel_nodes` 和 `max_parallel_tools` 不要混用。Node 是可验收子任务；tool call 只是 node 内 action/evidence。

## Memory And Learning

`memory` 当前稳定字段：

- `enabled`: 是否在任务完成后运行 memory observe。
- `root`: memory root，空值表示默认 workspace memory。
- `recent_days`: memory context 默认近期待检索窗口。
- `proposal_nudge`: proposal 提醒节奏、渠道和数量。

`learning` 当前稳定字段：

- `enabled`: learning observe 总开关。
- `skill_crystallization.enabled`: 是否允许 heartbeat 从重复经验中提出 skill proposal。
- `skill_crystallization.success_threshold`: 触发 skill proposal 的重复成功次数阈值。
- `skill_crystallization.min_confidence`: proposal 最低置信度。

旧字段 `memory.auto_propose`、`memory.auto_commit_low_risk` 和 `scheduler.state_dir` 不再写入新默认配置。结构体仍能读取旧配置，以便旧用户配置文件继续加载。

## Agents

Agent profiles 可以设置：

- profile id 和名称
- 工作区根目录和 agent 目录
- 模型选择
- 心跳任务
- 技能允许/拒绝列表
- 工具允许/拒绝列表
- 渠道绑定

当名称冲突时，agent 特定的技能会覆盖共享工作区的技能。

## Skills

默认技能作为可编辑的工作区资产安装：

```text
workspace/skills/<skill_name>/SKILL.md
workspace/skills/<skill_name>/.mateway/metadata.yaml
workspace/agents/<agent_id>/skills/<skill_name>/SKILL.md
workspace/agents/<agent_id>/skills/<skill_name>/.mateway/metadata.yaml
```

它们应保持为技能，而非嵌入到 runtime 代码中。Runtime 代码拥有硬边界；技能拥有用户可编辑的工作流指导。

技能发现和执行以本地注册为准：

- 只有带 `.mateway/metadata.yaml` 的 skill 才参与发现和执行。
- Planner 读取 metadata 摘要，用于判断 skill 是否适合作为 subtask node。
- Node Executor 只有在 skill node 被选中后才读取对应 `SKILL.md`。
- Skill 名称不是 tool 名称；真实工具调用仍受 node allowed tools、tool policy 和 trace 约束。

## Secrets

使用本地 secret store，不要将凭证写入配置、技能、脚本、trace 或提示中。

```bash
mateway secret set <secret_id>
mateway secret list
```

`terminal.run` 接受 `env_secrets` 条目，例如：

```json
{"id":"service/token","env":"SERVICE_TOKEN"}
```

Trace 和 evidence store 只记录 secret id 和环境变量名称。

## 安全说明

`terminal.run` 仍然是唯一的命令执行工具。破坏性命令被 tool policy 阻断。文件工具强制执行路径验证。类 secret 值会从 trace、存储的 transcript、任务步骤和最终回复中脱敏。

Docker 沙箱工作另行跟踪，不属于当前非沙箱计划的一部分。
