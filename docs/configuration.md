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
- `scheduler`: 本地调度循环设置。

模型定义位于 `config/models/*.yaml`。渠道定义位于 `config/channels/*.yaml`。

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
```

它们应保持为技能，而非嵌入到 runtime 代码中。Runtime 代码拥有硬边界；技能拥有用户可编辑的工作流指导。

执行提示上下文是有门的：

- 规划阶段可以发现 skill header
- contract 可以选择所需技能
- 执行阶段只接收已选技能或显式的 skill/workflow 上下文

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
