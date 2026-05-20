# Mateway 开发 TODO

更新时间：2026-05-20

## 说明

这份文档用于承接 [总规划](/Users/dongping/project/mateway/docs/总规划.md) 的执行拆解。

原则只有三句：

```text
总纲定方向；
TODO 定执行；
代码按 TODO 一项项落地。
```

总规划负责回答“系统应该长成什么样”，本文件负责回答：

- 现在要做什么
- 代码应该落在哪里
- 每项的验收标准是什么

默认开发顺序如下，除非用户明确调整优先级：

1. ResponseSanitizer
2. SessionState / TaskState
3. FollowupResolver
4. `project.index` / `file.summary` tools
5. AgentProfile / AgentRegistry / GatewayBinding

---

## P0：单 Agent 内核稳定

### T0.1 AgentLoop 工具调用闭环

状态：已完成第一版

- 目标：把当前 runtime 的“计划 -> 工具 -> 观察 -> 总结”整理成稳定的 AgentLoop，明确 receive -> plan -> policy -> act -> observe -> synthesize -> reply。
- 建议位置：
  - `internal/runtime`
  - `internal/model`
  - `internal/tool`
- 交付物：
  - 统一的 loop 输入输出结构
  - 明确的 step/result/event 模型
  - 一次 repair 的边界仍然受控
- 验收标准：
  - CLI 和 Feishu 走同一条 loop 主链
  - 工具调用、失败、确认、补问都有统一事件
  - `go test ./...` 通过
  - 至少保留一条可读 trace 证明完整闭环

当前结果：

- 已拆出 `internal/runtime/agent_loop.go`
- 已明确 `receive -> plan -> act -> repair -> synthesize -> reply`
- `Runtime.Handle()` 只负责启动 loop
- 后续还可继续收口 step/result/event 模型，但主链已成立

### T0.2 ResponseSanitizer

状态：已完成第一版

- 目标：在最终回复出站前做统一清洗，控制泄漏、冗长、工具噪音和不稳定格式。
- 建议位置：
  - `internal/runtime`
  - 或新增 `internal/response`
- 交付物：
  - sanitizer 接口和默认实现
  - 面向 CLI / Feishu 的统一出站清洗规则
- 验收标准：
  - 默认不泄漏无关内部推理和无用工具细节
  - 长输出会被稳定裁剪而不是随机截断
  - 错误、待确认、待补问三类回复风格清晰可区分

当前结果：

- 已新增 `internal/runtime/sanitizer.go`
- 最终回复、错误回复、待确认回复统一经过 sanitizer
- 已支持移除常见 prompt 回声
- 已支持默认兜底文案、空白收敛和长度限制

### T0.3 SessionState / TaskState

状态：已完成第一版

- 目标：把“本轮请求”和“同一 session 的连续上下文”显式化，不再只靠 inbound message 临时拼装。
- 建议位置：
  - `internal/runtime`
  - `internal/gateway`
  - 可新增 `internal/session`
- 交付物：
  - SessionState 结构
  - TaskState 结构
  - channel scoped session key 的统一读写入口
- 验收标准：
  - `feishu:<thread_id>`、`cli:<session>` 等命名稳定
  - 不同 channel 不串上下文
  - 后续 FollowupResolver 可以直接复用状态

当前结果：

- 已新增 `internal/session`
- 已有可落磁盘的 `SessionState / TaskState` 文件存储
- 默认存储位置为 `~/.mateway/run/sessions`
- `AgentLoop` 已在每轮开始时加载 session，在结束时保存 task
- 已支持 `active_task_id + tasks + task_order`
- 已支持 continuation / pending / artifact 等任务字段
- 已新增 `runtime.session_loaded / runtime.session_saved` trace 事件
- 已新增 artifact 直接回答第一版：
  - 在 planning 前识别“历史产物在哪里/发我链接/文档放哪了”等窄意图
  - 命中 session task artifacts 时直接返回路径或 URL
  - 未命中或请求过泛时回退原主链
  - 已新增 `runtime.artifact_lookup` trace 事件
- 当前已保存：
  - session 基本信息
  - 最近 turns
  - `active_task_id`
  - task map / task order
  - task 状态、工具名、reply preview
  - pending approval / pending questions / artifacts
- 后续仍可补：
  - 更强的恢复策略
  - 更丰富的 artifact 提取与检索

### T0.4 FollowupResolver

状态：已完成第一版

- 目标：让“继续、展开、再试一次、按刚才那个文件改”这类跟进请求能基于 session/task 状态补全。
- 建议位置：
  - `internal/runtime`
  - 或新增 `internal/followup`
- 交付物：
  - followup 判定入口
  - resolved query 结构
  - 需要时把解析结果写入 trace
- 验收标准：
  - 至少覆盖继续执行、继续总结、继续编辑三类常见 followup
  - 不依赖 channel 特殊逻辑
  - 无法判定时安全退回普通请求

当前结果：

- 已新增 task binding 阶段，位于 planning 前
- 已不再单独先判 `is_followup`
- 已支持：
  - `approval_reply`
  - `slot_fill`
  - `active_followup`
  - `open_task_followup`
  - `historical_continuation`
  - `new_task`
  - `ambiguous`
- 已把批准/补参保留为代码强规则
- 已把复杂 followup 接入专门模型解析
- 已新增 trace：
  - `runtime.task_binding_started`
  - `runtime.followup_resolved`
  - `runtime.task_activated`
  - `runtime.task_pending_input`
  - `runtime.task_pending_approval`
  - `runtime.task_continuation_created`
- 当前仍可继续补：
  - 更强的澄清策略
  - 更丰富的历史任务候选压缩

---

## P1：核心 Skills + 项目理解工具 + 多 Agent 共存

### T1.0 ToolRegistry / SkillRegistry 稳定化

状态：已完成第一版

- 目标：在 AgentLoop 之后，先把“工具注册集”和“skills 注册集”的边界固定住，避免后续中文总结、搜索后处理、项目理解能力继续散落在 runtime 里。
- 建议位置：
  - `internal/tool`
  - 可新增 `internal/skill`
  - `internal/runtime`
- 交付物：
  - 明确的 tool registry 装配入口
  - 明确的 skill registry 装配入口
  - runtime 如何消费 registry 的接口
- 验收标准：
  - 新工具和新 skill 不需要继续改主 loop 结构
  - 搜索后处理能力可以通过 skill 接入，而不是塞进搜索工具本体
  - 为 `chinese-summary` 这类后处理 skill 留出稳定挂点

当前结果：

- 已有 `tool.NewBuiltinRegistry()`
- 已有 `skill.LoadRegistry()` 与目录扫描
- runtime 已通过 registry 消费 tools / skills

### T1.1 SkillDiscovery

状态：已完成第一版，待增强

- 目标：把能力扩展从“硬编码提示词”转成可发现、可装配的 skill 机制。
- 建议位置：
  - `internal/runtime`
  - `internal/tool`
  - 可新增 `internal/skill`
- 交付物：
  - skill 元数据
  - discovery / match 入口
  - prompt 注入边界
- 验收标准：
  - skill 不侵入主 loop
  - 可按请求内容和上下文选择 skill
- 关闭 skill 后主 loop 仍可运行

当前结果：

- 已支持扫描 `workspace/skills`
- 已支持扫描 `workspace/agents/main/skills`
- 已支持 `SKILL.md` frontmatter 元数据
- 已有第一版 `SkillSelector + SkillInjector`

### T1.2 真实模型回归测试命令

状态：待完成

- 目标：在 `cmd` 中提供一个独立测试入口，执行后直接调用真实模型跑完整流程，并把每个任务的结果保存为 Markdown 文档。
- 建议位置：
  - `cmd/mateway`
  - `testdata/YYYY-MM-DD/`
- 交付物：
  - `mateway test` 子命令
  - 单任务单文档的 Markdown 报告
  - 报告中包含问题、结果、结论、执行过程和参数
- 验收标准：
  - 执行命令后会真实调用模型和 runtime 主链
  - 文档标题以任务名标注
  - 文档按日期分目录保存到 `testdata`
  - 用户或 Codex 能一眼看出任务是否正确执行，以及问题出在哪里
- 已支持按 `scope` 做冲突去重
- 已支持按 stage 做差异化预算控制
- 当前仍缺更强的多 skill 协同与组合策略

### T1.1a Agent Prompt Context

状态：已完成第一版

- 目标：把普通任务执行前需要给 agent 的稳定上下文固定下来，避免时间、环境、用户偏好和 agent 核心文档散落在 skill 或硬编码提示中。
- 建议位置：
  - `internal/runtime`
  - `internal/tool`
- 交付物：
  - 统一的 prompt context builder
  - agent 核心文档读取与注入
  - 基础环境摘要注入
  - 普通任务与 heartbeat 上下文边界
- 验收标准：
  - `planning / planning_repair / synthesis` 三个阶段走同一套注入结构
  - 默认注入 `soul.md / agent.md / user.md / memory.md / tools.md`
  - 默认注入当前日期、时区、OS、shell、workspace、project root 和常用命令摘要
  - `heartbeat.md` 不进入普通任务 prompt

当前结果：

- 已把 `user.md` 纳入 `workspace/agents/main`
- 已在普通任务 prompt 中注入时间、时区和基础环境摘要
- 已支持注入 `soul.md / agent.md / user.md / memory.md / tools.md`
- 已明确 `heartbeat.md` 只为后续主动任务流程保留

### T1.2 首批 Skills

状态：部分完成

- 范围：
  - `fresh-search`
  - `chinese-summary`
  - `source-evaluation`
  - `project-review`
- 目标：把常用工作流沉淀成 skills，而不是塞进 runtime 核心。
- 建议位置：
  - `skills/` 或 `internal/skill`
- 验收标准：
  - 每个 skill 都写清输入、输出、触发条件
  - skill 能组合现有工具，不直接破坏 runtime 边界
  - 至少各有一条 smoke case

当前结果：

- `fresh-search`：已提供默认模板，已接入通用 selector/injector
- `chinese-summary`：已提供默认模板，已接入通用 selector/injector
- `source-evaluation`：已提供默认模板，已接入通用 selector/injector
- `project-review`：尚未落地
- 当前重点已从“补 skill 挂点”转为“打磨执行效果和规则”

### T1.3 项目理解工具

状态：已完成第一版

- 范围：
  - `project.index`
  - `file.summary`
- 目标：提供轻量项目理解能力，优先服务 codebase 导航和总结。
- 建议位置：
  - `internal/tool`
- 验收标准：
  - 可以针对项目目录生成摘要性证据
  - 输出受预算约束
  - 明确路径安全边界

当前结果：

- 已新增 `project.index`
- 已新增 `file.summary`
- 两者都走现有 `tool.Registry`
- 两者都默认受 allowed roots 和输出预算约束
- `project.index` 当前可输出：
  - 目录/文件总数
  - 主要扩展名分布
  - 一份受深度和数量限制的 sample tree
- `file.summary` 当前可输出：
  - 文件基础元信息
  - heading / 结构线索
  - 受行数限制的 preview
- 后续仍可继续补：
  - 更强的代码结构摘要
  - 多文件组合总结

### T1.4 AgentProfile / AgentRegistry / GatewayBinding

状态：未开始

- 目标：支持多个长期共存 agent，但暂不进入 supervisor / spawn。
- 建议位置：
  - `internal/gateway`
  - `internal/config`
  - 可新增 `internal/agent`
- 交付物：
  - AgentProfile
  - AgentRegistry
  - channel/account/peer 到 agentId 的绑定
- 验收标准：
  - Gateway 可按绑定选择 agent
  - 每个 agent 有独立上下文目录和 session 命名空间
  - 不引入复杂 router

---

## P2：Agent 间通信

### T2.1 Agent Message Contract

- 目标：先定义 agent 间消息和权限边界，再考虑是否真正执行跨 agent 协作。
- 建议位置：
  - `internal/agent`
  - `internal/gateway`
- 验收标准：
  - 有明确 message schema
  - 有权限、来源、证据标记
  - 默认关闭，不影响单 agent 主链

---

## P3：planning-mode / 局部 DAG

### T3.1 Planning Mode

- 目标：仅在复杂任务中提供显式 planning mode，而不是默认主流程。
- 建议位置：
  - `internal/runtime`
  - `internal/model`
- 验收标准：
  - 默认仍是简单 AgentLoop
  - planning mode 为可选能力
  - 不要求全局 workflow engine

### T3.2 局部 DAG

- 目标：只支持局部可验证的依赖图，不做大而全编排系统。
- 建议位置：
  - `internal/runtime`
- 验收标准：
  - 仅用于少量工具步骤依赖
  - 保持 trace 可观测
  - 失败能退回线性执行

---

## P4：spawn / subagent

### T4.1 Spawn Boundary

- 目标：先定义 subagent 的触发条件、资源边界、上下文裁剪，再决定是否实现。
- 验收标准：
  - 不和多 Agent 共存概念混淆
  - 不引入 supervisor 式复杂度
  - 默认关闭，必须显式触发

---

## 当前目录映射观察

结合 2026-05-20 的仓库状态，当前代码大致对应如下：

- `cmd/mateway`：CLI 入口
- `internal/config`：配置读取
- `internal/model`：MiniMax 与 planner
- `internal/runtime`：当前单 Agent 主循环雏形
- `internal/tool`：工具注册、策略、预算、内置工具
- `internal/gateway`：会话键、Feishu 编排、单实例锁、服务管理
- `internal/channel/feishu`：飞书收发与事件归一化
- `internal/observer`：trace 与日志

这说明：

- `AgentLoop` 主链已经成立，后续主要是细节收口
- `ToolRegistry / SkillRegistry / SkillDiscovery` 已有第一版
- `ResponseSanitizer` 还没有独立层
- `SessionState / TaskState` 还没有清晰建模
- `FollowupResolver / Multi-Agent Profiles` 还基本未落地

因此下一步建议顺序是：

1. 先进入 `SessionState / TaskState`
2. 然后补 `FollowupResolver`
3. 再做 `project.index / file.summary`
4. skills 这条线继续做“规则打磨”，而不是回退去重做挂点
