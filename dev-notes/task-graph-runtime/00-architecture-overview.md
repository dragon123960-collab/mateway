# 00：Task Graph Runtime 总开发文档

更新：2026-06-15

## 给 OpenCode 的阅读方式

每次开发只需要读两个文档：

1. 本文档：理解整体架构、边界和阶段顺序。
2. 当前阶段文档：只实现该文档 TODO。

不要从后续阶段文档推断额外工作。当前阶段没有列出的改动，一律不做。

## 架构目标

Task Graph 是 Mateway 的主线替换式架构。旧机制是当前仓库已有实现和迁移参考，不作为新架构运行时回退路径。

所有任务最终进入同一条 graph lifecycle：

```text
inbound message
  -> task lifecycle
  -> graph planner
  -> TaskGraph validation
  -> scheduler
  -> atomic node executor
  -> node verifier
  -> task verifier
  -> graph finalizer
  -> final answer or blocker
```

任务复杂度由 graph 形态表达：

- 简单问答：一个 `model` node。
- 简单安全动作：少量 atomic `tool` / `model` nodes。
- 复杂任务：多节点 graph，由 scheduler 按依赖推进。
- 高风险任务：graph 内插入 `human_review` / `human_confirm` node。

## 全局边界

- Channel 只做 I/O，不做 graph routing。
- Gateway 只做会话路由、去重和发送，不做业务级 agent routing。
- Runtime 拥有 task/graph lifecycle。
- Tool policy、路径校验、secret redaction 是硬边界。
- Skill name 不是 tool name。
- 不引入 multi-agent supervisor、subagent spawning、旧实验包或 heavy workflow platform。
- 不新增 `terminal.run` 之外的命令执行工具。

## 阶段顺序

1. [01：Inbound Message 与任务续接入口](./01-inbound-message.md)
2. [02：TaskGraph Model 与 Validator](./02-graph-model.md)
3. [03：Graph Planner 与提示词](./03-graph-planner.md)
4. [04：Scheduler](./04-scheduler.md)
5. [05：Atomic Node Executor](./05-node-executor.md)
6. [06：Node Verifier 与 Task Verifier](./06-verifier.md)
7. [07：Graph Finalizer](./07-finalizer.md)
8. [08：Trace / Session / Recovery](./08-trace-session-recovery.md)
9. [09：Memory 集成](./09-memory-integration.md)
10. [10：Runtime 主路径替换](./10-runtime-replacement.md)
11. [11：真实模型端到端 Dogfood](./11-real-model-dogfood.md)

配套参考：

- [01A：任务续接状态机](./01a-continuation-state-machine.md)
- [Graph-Native Skill Registration](../graph-native-skill-registration/README.md)

## Prompt 规则

- 不是每个阶段都有 prompt。
- deterministic runtime logic 不写 prompt，例如阶段 01 Inbound Message。
- 需要模型参与的阶段才写 prompt，例如阶段 03 Graph Planner、阶段 05 中的 `model` node executor。
- Prompt 必须服务结构化输出，不让模型决定 runtime 调度、policy 或安全边界。

## 开发规则

- 每个阶段必须包含 focused tests。
- 保留旧机制测试作为迁移保护线，但不代表可回退到旧 runtime。
- 每次改动保持仓库可测试。
- 阶段完成后由 Codex 按该阶段文档 review。
- 阶段 10 必须按 10A-10E 分段开发和 review，不能一次性替换 `Runtime.Handle` 或删除旧主循环。
