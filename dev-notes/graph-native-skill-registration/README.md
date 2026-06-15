# Graph-Native Skill Registration 开发总览

更新：2026-06-15

## 协作方式

本系列文档用于 Codex / OpenCode 分工：

- Codex：维护方案文档、拆分阶段、审查 OpenCode diff、验证测试。
- OpenCode：按单个阶段文档实现代码、补测试、运行验证、报告完成情况。

交付规则：

- 每次只交给 OpenCode 一个阶段文档。
- OpenCode 只实现该文档 TODO，不顺手做后续阶段。
- Codex review 时按该阶段 TODO 逐项核对。
- 若实现中发现阶段边界不合理，先停下报告，不自行扩展架构。

## 总目标

Mateway v2 将 skill discovery 简化为本地注册模型：

```text
A skill is discoverable only after local registration.
Registration creates .mateway/metadata.yaml.
Raw SKILL.md files are treated as unregistered drafts.
```

`SKILL.md` 是人类可读内容；`.mateway/metadata.yaml` 是 Mateway 本地适配层、注册表和 graph planner 索引。

## 阶段顺序

1. [阶段 1：Metadata v2 类型与校验](./01-metadata-v2.md)
2. [阶段 2：安装时注册](./02-install-registration.md)
3. [阶段 3：手动注册命令](./03-register-command.md)
4. [阶段 4：Skill Doctor 与 orphan 报告](./04-skill-doctor.md)
5. [阶段 5：Runtime 只发现已注册 skill](./05-runtime-discovery.md)
6. [阶段 6：Heartbeat 修复建议](./06-heartbeat-repair.md)

## 全局边界

- 不把 skill name 当 tool name。
- 不在普通 runtime discovery 中写 metadata。
- 不引入 multi-agent supervisor、subagent spawning、DAG routing 或 gateway 业务路由。
- 不复制旧实验包，例如 `runtimev2`、`agentv2`、`toolv2`、`workflowv2`。
- `terminal.run` 仍是唯一命令执行工具。
- Tool policy、路径校验、secret 脱敏仍是硬边界。

## 全局验收

- 默认 skills、公共安装 skills、用户手动注册 skills 都走同一套 metadata 注册规则。
- 裸 `SKILL.md` 不再参与 v2 runtime discovery。
- 所有写入 skill 或 metadata 的路径都经过 secret-like 和 unsafe prompt marker 校验。
- 文档中的每个阶段都可以独立 review，并有明确测试命令。

