# Mateway Hook & Memory Runtime PRD

更新时间：2026-05-29

状态：v0.1.4 baseline implemented

负责人：Mateway Runtime

---

## 一、背景与目标

Mateway 当前主线是干净重写后的 Go 版小型 agent：

```text
CLI / Feishu-ready gateway
+ transcript-driven AgentCore
+ small ToolRegistry
+ task tree / followup / trace
+ multi-agent profile / binding foundation
+ ~/.mateway config and workspace layout
```

v0.1.4 baseline 已完成：

```text
Hook Skeleton
-> Memory Safe Read
-> Proposal Workflow
-> Self-learning Pipeline
-> Distillation / Heartbeat
-> Channel-neutral Schedule
```

下一阶段转入增强：Multi-Agent Profile 产品化、skill source adapters、script bridge、sandbox runner、只读 workspace visualization。

本 PRD 是 Hook、Memory、自我学习和 HOME 目录的唯一主设计文档。旧的分叉设计内容已合并到本文，不再作为独立实现依据。

目标不是“让 AI 记住更多”，而是构建一个可观察、可审计、可回滚、可演化的认知运行时系统。

---

## 二、核心原则

### 2.1 Hook-first Runtime

Memory 不直接塞进 Runtime 主循环。先完成统一 Hook 骨架，再把 memory 作为 hook provider 接入。

Runtime 主线保持：

```text
receive
-> gateway session claim
-> runtime setup
-> context_hook
-> AgentCore transcript loop
-> tool_policy_hook
-> tool execute
-> observe_hook
-> response_hook
-> reply
```

`followup_hook` 负责 session/task binding，可在模型调用前后参与路由，但不替代 task tree。

### 2.2 Memory Optional

Memory 是 enhancement / side-effect system，不是 Agent 可用性的前置条件。

禁用 memory、index 损坏、Markdown 解析失败、self-learning worker 失败时，Agent 仍必须能完成基础任务。失败只写 trace warning 或 audit event，不应中断 Runtime。

### 2.3 Markdown Source-of-Truth

`~/.mateway/workspace/memory/**/*.md` 是长期认知唯一真相源。

SQLite、向量库、`index.json`、`entity_graph.json` 仅作为：

- 检索加速
- 结构索引
- 可视化辅助
- 统计分析缓存

任何派生层都必须可删除后从 Markdown 重建。

### 2.4 Runtime 与 Memory 解耦

Runtime 负责：

- receive / reply
- session key
- task tree / followup
- transcript loop
- tool orchestration
- trace event generation

Memory 负责：

- safe-read context
- diary / reflection / proposal
- source evidence
- review / commit / reject
- distillation
- index rebuild / lint

Runtime 只能依赖 hook interface 和 event contract，不直接依赖具体 memory storage 实现。

### 2.5 Observation-driven Learning

长期学习必须来自 observation，而不是模型凭空自我总结。

有效 observation 来源包括：

- tool result
- task outcome
- retry / failure
- user confirmation / correction
- trace event
- accepted file / command result

未经 evidence 支撑的内容只能进入低置信 proposal 或 reflection，不能直接进入 active long memory。

### 2.6 Proposal Before Active Memory

默认策略：

- `diary / reflection / proposal` 可自动生成。
- `active long memory / skill / wiki` 默认需要用户确认。
- experience 自动写入、skill 修改确认策略可以通过低心智负担的 preset 配置。

---

## 三、Hook Contract

所有 hook 必须具备：

- no-op 默认实现
- timeout
- panic/error recovery
- trace warning
- 明确输入/输出
- 明确失败策略

Memory hook 失败时，Runtime 使用空结果继续执行。Security / policy hook 可以基于明确风险 deny 或 confirm，但不能因为 memory 故障阻断任务。

### 3.0 当前 Runtime 注入现状

Hook Skeleton 开发必须先兼容当前实现，而不是重写一套平行机制。

当前 agent 选择：

- `config.agents.default` 默认为 `main`。
- `config.agents.profiles[]` 定义可用 agent profile。
- `AgentPool` 初始化时为每个 profile 预制 agent 模板。
- 每次请求按 `sessionKey` 的 channel 前缀选择 agent：例如 `cli:xxx` 取 `cli`。
- 若 `config.agents.bindings[]` 中存在匹配 channel 的 binding，则使用对应 `agent_id`。
- 否则使用默认 agent。
- 取出 agent 时 clone 模板，避免不同 session 共享运行态消息。

当前提示词注入：

- Runtime system context 在 AgentPool 初始化阶段构建，并追加到模型 `SystemPrompt`。
- 当前注入内容包括运行环境、日期时间、HOME/workspace/security/search provider、freshness policy、connector gap policy、workspace profile context、discovered skills。
- workspace profile context 当前读取：
  - `workspace/agents/<agent>/agent.md`
  - `workspace/agents/<agent>/user.md`
  - `workspace/agents/<agent>/tools.md`
  - `workspace/memory/user/index.md`
- 当前已把 `workspace/agents/<agent>/memory.md` 纳入 prompt-facing memory card；按需检索 `workspace/memory/agents/<agent>/**` 由 `context_hook` 的 memory safe-read 负责，不在 AgentPool 初始化阶段做。
- 当前读取规则是小文件静态注入：文件不存在、为空、超出大小限制或看起来包含敏感字段时跳过。

Hook 迁移要求：

- `context_hook` 第一版应包住并复用当前 `buildRuntimeSystemContext` 的能力。
- 不要一开始改变 AgentPool 的 agent 选择语义。
- 不要把按需 memory search 做进 AgentPool 初始化；AgentPool 只适合静态 profile 级上下文。
- 每轮动态 memory 检索应发生在 `context_hook`，作为 steering/system context section 注入当前 turn。
- Hook 化后，静态 profile context 与动态 memory context 要在 trace 中可区分。

### 3.0.1 多 Agent Profile 边界

Mateway 不是单一固定 agent 项目；当前已经在 config、HOME 目录和 AgentPool 层保留多 agent profile 能力：

- `config.agents.default`
- `config.agents.profiles[]`
- `config.agents.bindings[]`
- `workspace/agents/<agent_id>/{agent,user,tools,memory}.md`
- `workspace/agents/<agent_id>/skills/`
- `workspace/memory/agents/<agent_id>/`

第二阶段的多 agent 工作是 profile 产品化：让用户能创建、查看、检查、绑定和验证多个 agent profile，并确保 prompt、skill、memory 的上下文隔离。

本文中的非目标 `multi-agent supervisor / spawn / DAG routing` 指自主多 agent 编排，不否定已有的 profile/binding 能力。第二阶段不引入 supervisor，不让 gateway 自动做复杂业务分派，不让 memory search 驱动 runtime routing。

### 3.1 context_hook

输入：

- channel/session/task
- user text
- workspace profile
- discovered skills
- optional memory search request/result

输出：

- `system_context_sections[]`
- `memory_refs[]`
- `freshness_policy`

M1 行为：

- 注入 `workspace/agents/main/{agent,user,tools,memory}.md` 的短上下文。
- 对明显 followup、项目问题、用户偏好问题执行 memory safe-read。
- 只注入短摘要和 source refs，不注入完整 wiki。
- 兼容当前静态注入路径，并把 `workspace/agents/<agent>/memory.md` 纳入 prompt-facing memory card。

失败策略：

- 返回空 memory context。
- 写 trace warning。
- Runtime 继续执行。

### 3.1.1 Memory Injection Policy

Memory 的“调用与注入”只由 `context_hook` 决定；其他 hook 不主动检索长期 memory。

每轮默认可注入：

- runtime context：时间、系统、workspace、安全策略、freshness policy。
- agent profile：`agent.md`、`user.md`、`tools.md`。
- prompt-facing memory card：`workspace/agents/<agent>/memory.md`。
- discovered skills 摘要。

按需检索注入：

| 场景 | 检索范围 | 注入内容 |
| --- | --- | --- |
| 用户偏好、表达习惯、长期规则 | `memory/user/long`、`memory/global/preferences` | preference 摘要 + source refs |
| 项目/代码任务 | active project memory、agent experience | project context、decision、相关 experience |
| 工具调用前需要避坑 | tool evidence、agent/project experience | 相关 op_fingerprint、失败模式、确认边界 |
| followup 或分支恢复 | session summary、task tree、project context | session/task 摘要，不替代 followup binding |
| SOP 类任务 | agent/project/shared skills | skill 摘要、适用条件、source refs |
| 抽象策略问题 | patterns、wiki | pattern/wiki 摘要 |

默认不注入：

- 完整 wiki 原文。
- diary/reflection 原文。
- inbox/proposal，除非用户正在 review memory。
- 大段历史 transcript。
- 没有 source evidence 的低置信结论。

注入预算：

- 静态 profile context 保持短小。
- memory search 只返回 top snippets 和 source refs。
- 同一轮内优先级为：session context -> active project -> user preference -> relevant skills -> experience -> pattern/wiki。
- 如果预算不足，保留 source refs 和最短 actionable summary。

时机：

- AgentPool 初始化只构建静态 profile prompt。
- 每次 `Runtime.Handle` 进入 task 后，模型调用前执行 `context_hook`。
- tool loop 中如需工具前经验召回，可在下一轮模型调用前由 `context_hook` 根据上轮 tool intent/result 补充，不在 `tool_policy_hook` 内做长期检索。

### 3.2 tool_policy_hook

输入：

- tool call
- tool risk
- security config
- path policy
- dangerous command classifier
- tool evidence / confirmation boundary

输出：

- allow / confirm / deny
- confirmation message
- policy evidence

M1 行为：

- 集中处理 path policy、危险命令确认、binary/size guard。
- 后续把 OS sandbox 作为 `terminal.run` wrapper 接入。

失败策略：

- policy hook 自身异常时按保守策略处理高风险工具。
- 低风险只读工具可继续。
- memory evidence 缺失不能单独导致 deny。

### 3.3 observe_hook

输入：

- model event
- tool result
- task step
- retry / failure
- acceptance
- final reply
- trace id/path

输出：

- trace JSONL event
- task step evidence
- diary candidate
- reflection candidate
- memory proposal candidate

M1 行为：

- 继续写 trace 和 task step。
- 任务结束后异步触发 self-learning worker。
- 成功任务可写 diary；高价值发现可生成 proposal。

失败策略：

- observe 写入失败不影响最终回复。
- self-learning worker 失败只写 warning/audit。

### 3.4 response_hook

输入：

- raw assistant reply
- tool traces
- channel type
- memory review candidates

输出：

- sanitized reply
- channel-specific formatting
- optional memory review block

M1 行为：

- 保留 response sanitizer。
- Feishu 默认输出干净最终回复，不刷中间过程。
- 如有高价值 memory candidate，在最终回复列出具体候选内容供用户拍板。

失败策略：

- sanitizer 失败时使用安全 fallback 文本。
- memory review 渲染失败不影响基础回复。

### 3.5 followup_hook

输入：

- session task tree
- user text
- pending state
- optional memory refs

输出：

- active task id
- rewritten user text
- clarify prompt
- branch/session route

M1 行为：

- 保留当前规则型 resolver。
- memory 只辅助历史背景，不替代 session binding。
- 新开 session 会触发旧 session 的 `session_ended` event，但不自动判定旧任务成功完成。

失败策略：

- 使用现有 task tree fallback。
- 不因 memory 检索失败而改写 task route。

---

## 四、Memory Availability Contract

Memory 必须满足：

- 可禁用：关闭 memory 后 CLI、Feishu、tool loop、followup 仍正常。
- 可降级：safe-read 失败时返回空上下文。
- 可追踪：所有 mutation 记录 source、时间、actor、trace/task。
- 可审计：proposal、commit、reject、skill patch 都写 audit。
- 可回滚：自动或手动修改必须保留旧版本或 diff。
- 可重建：index/sqlite/vector/entity graph 不保存唯一事实。

禁止：

- Runtime 主循环直接依赖 memory package 才能启动。
- memory index 损坏导致 Agent 不可用。
- self-learning 失败导致最终回复失败。
- 未经 evidence 的内容直接进入 active long memory。
- 自动写入 secrets、token、密码、私钥、cookie。

---

## 五、HOME 目录目标结构

默认 HOME 是 `~/.mateway`。

```text
~/.mateway/
  config/                         # M1
    config.yaml
    models/
    channels/
    memory.yaml                   # M1: learning presets / thresholds

  runtime/                        # M1 target, may migrate from current sessions/trace/run
    sessions/
      sess_xxx/
        meta.json
        transcript.jsonl
        task_tree.json
        wip.md
    trace/
    run/

  observe/                        # Memory M4
    diary/
    reflections/
    proposals/
    audit/

  workspace/                      # M1
    agents/
      main/
        agent.md
        soul.md
        user.md
        tools.md
        memory.md                 # prompt-facing short memory
        skills/
          README.md
          <agent-specific skills>/

    skills/                       # M1
      <shared skill>/SKILL.md

    memory/                       # Memory M1+
      README.md
      schema.md
      log.md

      global/
        preferences/
        experience/
        skills/
        patterns/

      user/
        long/
        inbox/

      org/
        long/
        inbox/

      agents/
        main/
          memory.md               # long-term memory wiki entry
          experience/
          skills/
          patterns/
          wiki/
          inbox/
          archive/

      projects/
        <project_id>/
          context/
          decisions/
          experience/
          skills/
          patterns/
          wiki/
          inbox/
          archive/

  indexes/                        # Future / rebuildable
    memory_index.json
    entity_graph.json
    sqlite/
    embeddings/
```

说明：

- 当前已有 `sessions/trace/run` 可逐步迁移到 `runtime/`，不强行一次性搬。
- `observe/` 是 self-learning 工作区，不是长期事实源。
- `workspace/agents/main/memory.md` 是 prompt-facing 短摘要。
- `workspace/memory/agents/main/memory.md` 是长期 memory wiki 入口。
- `indexes/` 可删除重建，不提交 runtime state 或 secrets。

---

## 六、Markdown Schema

所有长期 memory 条目使用 Markdown frontmatter + 用户友好正文。

最小 frontmatter：

```yaml
---
type: preference | decision | experience | skill | pattern | wiki | diary | reflection | proposal
scope: global | user | org | agent | project
owner_agent: main
project_id:
visibility: private | shared-user | shared-org
status: proposed | active | rejected | deprecated | archived
tags: []
aliases: []
op_fingerprint:
sources:
  - trace:<trace_id>
  - task:<task_id>
  - file:/path/to/file.md:12-20
  - https://example.com/source
confidence: high | medium | low
created_at: 2026-05-29
updated_at: 2026-05-29
review_after:
schema_version: 1
---
```

原则：

- 具体事实必须有 source，尤其是数字、日期、配置、账号、路径、外部结论。
- 自动生成内容先进入 `observe/` 或 `inbox/`，不直接进入 wiki/skill。
- 人类能直接读懂和修改正文。
- schema 字段为未来 SQLite / vector index 预留，但 Markdown 仍是真相源。

---

## 七、Memory Layer

### 7.1 Experience

Experience 是局部、场景化、可能过时的经验。

来源：

- 成功工具使用
- retry 后解决
- 用户纠正
- 任务验收

默认进入 proposal；在 `balanced` 或 `autonomous` preset 下，低风险 agent/project experience 可按阈值自动写入 active。

### 7.2 Skill

Skill 是稳定 SOP。

进入条件：

- 多次成功 experience 聚合后生成 candidate。
- 或用户明确确认。
- 或手动从 proposal promote。

修改 skill 默认必须展示 diff/proposal。修改 SOP 步骤正文、风险边界、适用范围时必须确认，除非用户显式选择更自动的 preset。

### 7.3 Pattern

Pattern 是架构级策略，不绑定单个工具。

示例：

- retry-with-validation
- tool-fallback
- branch-recovery

Pattern 默认只进入 proposal，不自动 active。

### 7.4 Wiki

Wiki 是跨项目稳定知识。

建议进入条件：

- 至少 2 个项目复用
- 多次验证
- 稳定周期足够长
- 用户确认

Wiki 禁止自动写入。

### 7.5 Tool Contract / Evidence

工具参数、schema、确认边界、失败模式、重试策略优先沉淀到 tool contract/evidence，而不是频繁作为普通 memory 打扰用户。

只有当工具经验变成人类可复用 SOP 时，才生成 skill candidate。

---

## 八、Self-learning Pipeline

Self-learning 属于 Memory 系统，不属于 Runtime 主循环。

任务完成后，Runtime 发出 event，self-learning worker 异步消费：

```text
task/session/project event
-> collect trace/task/transcript/tool evidence
-> write diary/reflection
-> generate proposal or skill patch
-> surface high-value candidates in response/review queue
```

默认行为：

- 不阻塞最终回复。
- 失败只写 trace warning/audit。
- 不直接写 active long memory / skill / wiki。
- 高价值候选在最终回复展示具体内容，让用户保存、忽略、改写、提升为 skill。

### 8.1 Memory Review UX

Memory review 不能只显示数量，必须展示具体内容：

```text
可沉淀记忆候选：

1. Experience
   场景：飞书 webhook 连续 401 后恢复
   建议记忆：遇到飞书 webhook 连续 401 时，优先检查 tenant token 刷新状态。
   来源：trace:xxx, task:yyy
   操作：保存 / 改写 / 忽略 / 提升为 skill

2. Skill Patch
   Skill：lark-webhook-debug
   触发原因：本次任务发现原 SOP 缺少 token 刷新检查。
   变更：在步骤 2 后追加 tenant token 检查。
   来源：trace:xxx
   操作：应用 / 改写 / 忽略
```

### 8.2 Candidate Value Gate

只有满足以下条件之一，才在最终回复展示候选：

- 用户明确表达长期偏好或规则。
- 任务中出现重复失败、retry，并最终解决。
- 工具调用发现稳定坑点。
- 用户确认了项目决策。
- 同类 experience 多次出现，值得升 skill。
- 产出了可复用流程。

普通低价值内容只进入 diary/trace，不打扰用户。

---

## 九、Learning Policy Presets

不把复杂 policy 暴露给普通用户。M1 使用少量 preset 加高级阈值配置。

### 9.1 Presets

`conservative` 默认：

- 自动写 diary/reflection/proposal。
- active memory 写入需要确认。
- skill 修改必须确认。
- wiki 禁止自动写。

`balanced`：

- 低风险 agent/project experience 达到阈值后可自动 active。
- skill candidate 自动生成，但应用仍需确认。
- 可自动追加 skill evidence、last_verified、usage_count。

`autonomous`：

- 允许更多自动沉淀和低风险 skill metadata 更新。
- 仍必须保留 audit log、diff、rollback。
- 修改 SOP 步骤、风险边界、适用范围默认仍建议确认，除非配置显式关闭确认。

### 9.2 Advanced Thresholds

高级配置项示例：

```yaml
memory:
  learning:
    preset: conservative
    experience:
      auto_active_after_successes: 3
      auto_active_scopes: ["agent", "project"]
      require_confirmation_scopes: ["user", "org", "global"]
    skill:
      candidate_after_experiences: 3
      mutation_mode: propose_patch # propose_patch | auto_metadata | auto_patch
      auto_metadata_fields:
        - last_verified
        - evidence
        - failure_case
        - usage_count
    distill:
      task_completed: async_light
      session_ended: async_summary
      project_closed: full_review
      heartbeat_full_distill: disabled
```

实现可在内部抽象为 learning policy，但文档和配置只暴露 preset 与少量阈值，降低用户心智负担。

---

## 十、Skill Mutation Rules

自我学习可以基于任务执行问题提出已有 skill 修改。

默认行为：

- 生成 skill patch proposal。
- 展示新旧对比、触发原因、source evidence。
- 用户确认后修改 skill。

可配置自动追加低风险字段：

- `last_verified`
- `evidence`
- `failure_case`
- `usage_count`

必须确认的变更：

- SOP 步骤正文
- 风险边界
- 适用范围
- 删除或废弃已有步骤
- 用户偏好相关描述

所有 skill 修改必须进入 audit log，并支持 rollback。

---

## 十一、Distillation Boundaries

Self-learning 有三个蒸馏边界：

```text
task_completed
session_ended
project_closed
```

### 11.1 task_completed

任务完成，可做任务级轻量总结。

判定条件：

- 用户明确确认完成。
- pending task 清空，并且最终回复已交付结果。
- followup resolver 判断用户进入新任务。

输出：

- diary
- reflection
- experience proposal
- skill candidate proposal
- task outcome summary

### 11.2 session_ended

新开 session 或分支结束时触发 session-level distill。

规则：

- `session_ended` 不等同于 `task_completed`。
- 新开 session 会结束旧 session，但不自动判定旧任务成功。
- 未完成任务保留 pending / summary，不自动清空 WIP。

输出：

- session summary
- pending task summary
- branch context
- proposal candidates

### 11.3 project_closed

项目结束必须显式触发。

触发方式：

- 用户说关闭/归档项目。
- 命令：`project close` 或 `memory distill --project`。
- 未来 milestone close。

不把长期无活动自动视为 project closed。

输出：

- project final summary
- decisions consolidation
- experience aggregation
- skill candidate aggregation
- pattern candidate
- stale/conflict memory warning
- diary/reflection archive proposal

---

## 十二、Session Tree Isolation

Session 是认知执行节点。

每个 session：

- 拥有独立 WIP。
- 拥有父节点关系。
- 可形成 branch tree。
- 可 fork / merge / rewind / complete / fail。

原则：

- 分支互不污染。
- Followup 继承父分支认知，但不直接写回父分支。
- WIP 属于 session local state。
- 长期 memory 不直接绑定 WIP。
- merge 不合并 WIP 原文，只同步明确产物、accepted decision、committed proposal、task outcome。
- 失败分支默认只保留 trace/diary/reflection，不污染父分支 active context。

Session schema：

```yaml
session_id:
root_session:
parent_session:
branch_reason:
status:
created_at:
completed_at:
```

---

## 十三、Index Layer

### 13.1 memory_index.json

作用：

- metadata cache
- snippet index
- rebuild acceleration

不是 source-of-truth。

### 13.2 entity_graph.json

维护：

- entity relation
- project relation
- tool relation
- source relation

用于：

- retrieval expansion
- relation discovery
- visualization

### 13.3 SQLite / Embeddings

未来可选：

- SQLite 保存结构索引和查询加速。
- Embeddings 保存语义检索向量。

两者都必须可由 Markdown + trace/source 重建。

---

## 十四、Memory Retrieval

M1 不依赖向量库。

采用：

- scope filter
- type filter
- keyword search
- op_fingerprint
- source relation
- entity relation

检索优先级：

```text
Session Context
-> Active Project
-> Agent Skills
-> Experience
-> Pattern
-> Wiki
```

只注入短摘要和 source refs，不注入整篇 memory。

---

## 十五、Lifecycle

```text
Observation
-> Diary / Reflection
-> Proposal
-> Review
-> Commit / Reject
-> Experience / Skill / Pattern / Wiki
-> Index Rebuild
-> Review / Archive / Rollback
```

清理策略：

| 类型 | 策略 |
| --- | --- |
| stale proposal | archive |
| outdated experience | downgrade |
| dead branch | cleanup |
| orphan diary | compact |
| conflicting memory | review proposal |
| skill regression | rollback proposal |

---

## 十六、Visualization

第一阶段只做只读透明系统。

包括：

- Trace Timeline
- Task Tree
- Memory Ledger
- Skill Shelf
- Workspace Health

原则：

- 服务调试
- 服务审计
- 服务信任
- 不先做复杂产品 UI

---

## 十七、Roadmap

### M1：Hook Skeleton

- 定义 hook types 和 provider registry。
- 接入 Runtime pipeline，默认 no-op。
- 增加 timeout、panic/error recovery、trace warning。
- 验证禁用 memory 时 Runtime 正常。

### M2：Memory Safe Read

- Markdown schema/frontmatter/source。
- index rebuild。
- lint。
- keyword search。
- `memory.search` safe-read。
- 通过 `context_hook` 注入短摘要和 source refs。

### M3：Proposal Workflow

- proposal / commit / reject。
- Memory Review UX。
- audit log。
- active memory 写入 confirmation boundary。

### M4：Self-learning Worker

- task_completed 异步轻量沉淀。
- diary / reflection / proposal。
- high-value candidate gate。
- skill patch proposal。

### M5：Distillation

- task/session/project distill boundaries。
- session-level summary。
- project close full distill。
- configurable learning presets and thresholds。

### M6：Heartbeat / Schedule

- memory.index_rebuild heartbeat：已实现。
- memory.lint heartbeat：已实现。
- heartbeat foreground runner：已实现。
- explicit schedule create/list/test/activate/pause/run-due/serve：已实现。
- schedule 默认 pending，试跑成功后 active：已实现。
- 飞书入口创建 schedule 后可通过“执行”完成试跑和激活：已实现。
- optional full distill heartbeat：仍默认关闭，后续增强。

### M7：Visualization / Optional Indexes

- 只读 report / local HTML。
- Memory Ledger。
- optional SQLite。
- optional embeddings。

---

## 十八、非目标

当前阶段不做：

- multi-agent supervisor
- spawn/subagent
- DAG routing
- memory-driven runtime routing
- embedding-first architecture
- always-on autonomous mutation
- self-modifying prompt
- 自动写 wiki
- 未经审计的 skill rewrite

---

## 十九、验收标准

- 禁用 memory 后 Agent 仍可运行。
- Hook provider 失败时 Runtime 不失败。
- trace 能看到 hook event/warning。
- 删除 index 后可从 Markdown 重建。
- 任务完成后可异步生成 diary/reflection/proposal。
- 高价值 proposal 在最终回复中展示具体内容。
- Skill 修改默认以 diff/proposal 方式确认。
- 新开 session 触发 session-level distill，但不误判 task completed。
- 项目级 full distill 只在显式 project close 时触发。
- 旧的分叉设计文档不再存在，也无引用残留。
