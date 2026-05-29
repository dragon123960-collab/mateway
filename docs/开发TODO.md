# Mateway 开发 TODO

更新时间：2026-05-29

## 当前主线

当前正在做：

```text
第二轮开发准备：Script Bridge / Skill Search / Skill Proposal
```

总路线：

```text
Hook Skeleton
-> Memory Safe Read
-> Proposal Workflow
-> Self-learning Worker
-> Distillation Boundaries
-> Heartbeat / Schedule
-> Visualization
```

详细设计以 `docs/记忆prd.md` 为准。

---

## P0：干净重写骨架

状态：已完成第一轮

目标：

- 保留 Git 仓库不变。
- 旧工作树备份到 `/Users/dongping/project/mateway1`。
- 新 `mateway` 目录只放必要骨架。
- 首先让项目能 `go test ./...`。

已完成：

- 旧工作树已备份到 `/Users/dongping/project/mateway1`。
- 新工作树已重建最小 Go 项目。
- 已接回完整 config loader / init 模板。
- init 已建立当前 HOME/workspace 目录契约。
- 已接回 Feishu channel normalize / sender / card renderer。
- 已接回 build/reset/restart 脚本。
- `go test ./...` 已通过。

---

## P1：AgentCore

状态：第一版完成，进入 hook 化整理

目标：

- transcript 是运行事实源。
- model 输出 assistant message。
- assistant message 可包含 tool calls。
- loop 执行 tool calls 并追加 tool results。
- max iteration 生效。
- hook 留好：context、policy、observe、response、followup。

已完成：

- 普通回答、单个 tool call、unknown tool、invalid args、max iteration 已覆盖。
- 真实 model provider adapter 已接第一版。
- `AgentPool` 已接第一版。
- malformed tool-call 会进入修复/合成路径。
- 工具预算耗尽后会做 synthesize-only 总结。

下一步：

- 把现有 hook 语义整理成清晰的 `context / policy / observe / response / followup` 层。

---

## P2：ToolRegistry

状态：完成第四轮基础工具契约、搜索压缩与安全边界

默认工具：

- `file.read`
- `file.write`
- `project.index`
- `terminal.run`
- `web.search`
- `web.fetch`

已完成：

- 每个默认工具声明 `when_to_use / when_not_to_use / evidence / acceptance / confirmation_boundary`。
- tool result 会写入 task step，并按最小验收规则标记 `accepted / suspect / failed`。
- file/project tools 已接 workspace path policy。
- terminal dangerous command policy 已接入。
- `web.search` 已接 provider 链并压缩为结构化摘要。
- `web.fetch` 对 Hacker News 首页类 URL 有 HN Algolia fallback。

后续：

- `memory.search` 放到 P4.2。
- `schedule.*` 已完成基础闭环：create/list/test/activate/pause/run-due/serve；后续只增强通知脚本、运行报告和托管安装。
- OS-level sandbox 后置，可作为 `terminal.run` wrapper。

---

## P2.5：Task Tree / Followup

状态：完成第三轮

已完成：

- session state 保存 `tasks`、`active_task`、`pending`。
- 每轮新任务会创建 task node。
- tool execution 会写入 task step 和 acceptance evidence。
- guarded mutation 会保存 pending tool call。
- “确认/取消”优先处理 pending。
- assistant 追问会保存 pending input。
- completed task 后的新输入默认创建新 task。
- 明确 followup cue 会复用最近 task。
- 明确序号引用会重新激活对应历史 task。
- 历史引用多候选或无候选时进入澄清 pending。
- 不同 session 不共享 followup 上下文。

下一步：

- 在 `followup_hook` 中保留现有规则型 resolver。
- memory 只辅助历史背景，不替代 session binding。

---

## P2.6：真实任务测试入口

状态：完成第三轮

已完成：

- `mateway test` 已接回，直接走真实 runtime/config/model/tools/session。
- 支持 `--case read-readme|project-index|web-search`。
- 支持 `--message <task>`。
- 输出 session、message、最终回复、task step/acceptance 概览。
- 输出 trace JSONL 路径。
- 默认把测试结果写入 `testdata/runs/*.json`。

---

## P2.7：第一版闭环剩余项

状态：基本完成

第一版闭环已经具备：

- CLI 真实模型任务入口。
- Feishu WebSocket gateway 入口。
- AgentCore transcript tool loop。
- 基础工具：file/project/terminal/web。
- task tree / followup / pending confirmation。
- testdata 测试记录。
- model fallback 多模型链。
- system prompt 运行上下文。
- gateway `serve/start/restart/stop/status`。
- `gateway serve` 单实例锁。
- Feishu inbound 快速 ack、去重、忽略 self/app/bot。
- trace JSONL。
- response sanitizer。
- terminal dangerous command policy。
- `file.read` 目录、大小、binary guard。
- Skill discovery 第一版。
- HOME 目录结构文档。
- 真实任务测试清单。

继续观察：

- Feishu 真实消息端到端耗时、reaction 体验、错误友好度。

---

## P3：Channel / Gateway

状态：保留现有接口，轻量维护

目标：

- 保持 channel 只做 I/O 和消息归一化。
- gateway 负责 session key、dedupe、异步运行和 channel serving。
- 不引入 gateway 多 agent 业务路由。

---

## P3.5：Hook Skeleton

状态：已完成 consolidation 第一轮

目标：

- 在不引入 supervisor/subagent 的前提下，把现有 hook 变成清晰、可观察、可扩展的 runtime extension points。
- hook 只增强单 agent 主循环。
- 每个 hook 默认 no-op，可安全降级。
- Memory 作为 hook provider 接入，不能强耦合 Runtime。

现状约束：

- 当前 AgentPool 按 `sessionKey` 的 channel 前缀和 `config.agents.bindings[]` 选择 agent；没有 binding 时使用默认 agent。
- AgentPool 初始化时为每个 profile 预制 agent 模板，每次请求 clone，避免 session 共享消息状态。
- 当前 runtime system context 在 AgentPool/model 初始化阶段追加到模型 `SystemPrompt`。
- 当前静态注入由 `buildRuntimeSystemContext` 完成，包括 runtime 环境、freshness policy、connector gap policy、workspace profile context、discovered skills。
- 当前 workspace profile context 读取 `agent.md`、`user.md`、`tools.md`、`workspace/memory/user/index.md`；尚未正式读取 prompt-facing `memory.md`，也没有按需 memory search。
- Hook Skeleton 要先包住现状，再逐步迁移，不要一开始改变 agent 选择和 session binding 语义。

已完成：

- 定义 hook provider 与 `context_hook` 输入输出。
- Runtime 进入 AgentCore 前执行 context hook。
- hook provider 具备 timeout、panic/error recovery、trace warning。
- 现有 `buildRuntimeSystemContext` 已由 static context provider 复用。
- `workspace/agents/<agent>/memory.md` 已纳入 prompt-facing 静态上下文。
- AgentCore/SystemPrompt 传递链路已修正。
- `followup_hook` 已包装现有规则型 resolver。
- `tool_policy_hook` 已承接危险命令、risky tool confirmation。
- `observe_hook` 已承接 tool step 和 task completed self-learning。
- `response_hook` 已承接 sanitizer、fallback、memory review block。

验收：

- 已通过 `go test ./...`。
- provider panic/timeout 不影响 Runtime。
- trace 可见 `hook_event` / `hook_warning`。
- 各 hook provider 失败会降级到后续 provider 或安全 fallback。

---

## P4：Memory

状态：Hook Skeleton 后开始

原则：

- Memory 是 enhancement / side-effect system。
- Markdown 是 source-of-truth。
- index/sqlite/vector 都是可重建增强层。
- 自我沉淀属于 memory，但默认可审计、可确认、可回滚。

### P4.1：Markdown Schema / Source / Index / Lint

状态：已完成 M2 第一轮

已完成：

- `internal/memory` 支持 Markdown frontmatter 解析。
- `mateway memory lint` 检查必填字段、enum、active source evidence、疑似 secret。
- `mateway memory index rebuild` 从 Markdown 重建 `indexes/memory_index.json`。
- init 模板已更新为 PRD schema。

验收：

- 已通过 `go test ./...`。
- 默认 init 后 lint 可通过。
- index 是派生缓存，可删除重建。

### P4.2：memory.search Safe Read

状态：已完成 M2 第一轮

已完成：

- `mateway memory search` 支持 keyword、scope、type、limit。
- `context_hook` 已按需执行 memory safe-read。
- 注入内容只有短 snippet 和 source refs。
- memory root 缺失/搜索失败只产生 hook warning，不阻断 Runtime。

验收：

- 已通过 `go test ./...`。
- trace 可区分 `static_context` 与 `memory_safe_read`。
- F 组复测：清理旧 memory 后，真实 trace 已出现 `memory_safe_read` 且 `memory_refs=["agents/main/memory.md"]`。

### P4.3：Proposal Review / Commit / Reject

状态：已完成 M3 手动基础版

已完成：

- `mateway memory proposal create/list/reject/commit`。
- proposal 写入 `observe/proposals/*.md`。
- audit 写入 `observe/audit/memory.jsonl`。
- rejected proposal 不可 commit。
- active memory 只有显式 commit 才写入 `workspace/memory`。

验收：

- 已通过 `go test ./...`。
- commit 后可被 index/search 纳入。
- 未接自动学习，不会自动写 active memory。

### P4.4：Self-learning Worker

状态：已完成第一轮

已完成：

- task completed 后轻量沉淀。
- 成功任务写 `observe/diary/*.md`。
- failed/suspect step 写 `observe/reflections/*.md`。
- 高价值 gate 生成 proposal，默认 status=proposed。
- worker 失败只写 trace warning，不影响最终回复。
- 最终回复展示具体候选内容和操作命令。

保留限制：

- 当前是轻量同步执行，后续可改异步队列。
- 当前不调用模型做复杂总结。
- 当前不做 skill patch proposal。

验收：

- 已通过 `go test ./...`。
- 低价值内容只进 diary，不打扰用户。
- 高价值 proposal 包含类型、建议正文、source evidence、操作建议。

### P4.5：Distillation Boundaries

状态：M5 第一轮已完成

已完成：

- task_completed：已由 self-learning worker 写 diary/reflection/proposal。
- session：已支持显式 `mateway memory distill session <session-key>`。
- session distill 只生成 `observe/reflections/*.md`，不修改 task 状态。
- project：已支持显式 `mateway memory distill project close <project_id>`。
- project distill 只生成 reflection/audit，不自动归档或改写 active memory。

下一步：

- session_ended 自动触发后置，避免误判 task completed。
- 未完成 task 保留 pending/summary，不自动清空。

验收：

- 已通过 `go test ./...`。
- 显式 session distill 不会把 running task 改成 completed。
- 显式 project close 才触发 project distill。

### P4.6：Learning Presets / Skill Patch Proposal

目标：

- 支持 `conservative / balanced / autonomous` 预设。
- 支持 experience 自动写入阈值配置。
- 支持 experience 升 skill candidate 阈值配置。
- 支持已有 skill patch proposal。

验收：

- 默认 `conservative`：只生成 proposal，skill 修改必须确认。
- `balanced` 可自动写低风险 agent/project experience。
- skill patch 展示新旧对比、触发原因、source evidence。
- SOP 步骤正文、风险边界、适用范围修改默认必须确认。

---

## P5：Heartbeat / Schedule

状态：Memory proposal/lint/index 稳定后置

原则：

- 当前不做定时任务、heartbeat 或 cron runner。
- M1 可预留接口，但定时 full distill 默认关闭。
- 先迁 heartbeat 中的 `memory.index_rebuild` 和 `memory.lint`。
- 用户显式 `schedule.*` 放在 heartbeat 之后。

验收：

- heartbeat 默认关闭。
- heartbeat 失败不影响 Runtime。
- heartbeat 只处理可审计、可回滚的 memory maintenance。
- F 组复测：手动 `mateway memory heartbeat lint-index` 已在干净 memory root 通过。

## 当前短 TODO

- 已复测 F004-F008：proposal commit 后新 memory 可 index/search/safe-read，`readme.md` 嵌套记忆索引问题已修复。
- 已补真实任务测试清单：当前工具、邮件脚本、远程脚本、skill catalog/search/install 候选验收。
- 下一步优先讨论并实现 Script Bridge 最小规范，再迁回 skill search/install。

---

## P7：Script Bridge / Connector Gap

状态：第二轮优先候选

目标：

- 不做完整 connector framework。
- 允许 agent 在 `connector-gap` skill 指导下发现、验证和调用用户脚本。
- 邮件收发、远程服务器只读检查、自媒体发布前置脚本都先走这个 bridge。

建议能力：

- 约定脚本目录：`~/.mateway/scripts/` 和项目内 `scripts/`。
- 支持只读脚本探测：`command -v`、`--help`、版本命令、脚本存在性。
- 定义脚本输出 evidence：优先 JSON，其次结构化文本。
- 发送邮件、远程执行、发布内容等动作必须走 guarded mutation/确认边界。
- trace 记录脚本路径、命令、exit code、stdout/stderr 摘要。

验收：

- 邮件收件脚本能被发现/调用，并基于真实输出总结。
- 发邮件脚本在缺收件人/主题/正文/账号时追问。
- 远程检查无 host 时追问，有脚本时只读执行并总结 evidence。
- 不能假装邮件发送、服务器检查或平台发布完成。

---

## P8：Skill Search / Install / Promote

状态：第二轮候选，需先讨论协议边界

旧版参考：

- `mateway skill search <query>`
- `mateway skill install <name-or-url>`
- `mateway skill promote ...`
- `mateway skill list`
- 默认 catalog：`skills.sh`、`skillhub.cn`、`clawhub.ai`，DuckDuckGo fallback。

设计取舍：

- 可以把 skill 源写入 config，但 config 只放声明性信息：name、base_url、search_url、enabled、trust_level。
- 不建议把每个源的 HTML/JSON 解析规则全部塞进 config；不同源差异大，解析应由 adapter 代码维护。
- 第一阶段先做 search/report，不自动安装。
- install 是 guarded mutation，只写 `workspace/skills/<name>/SKILL.md`，不执行 skill 内脚本。
- promote 仍走 proposal/diff/audit，不直接改已安装 skill。

验收：

- search 返回来源、URL、名称、是否已安装、风险提示。
- install 幂等，已安装时不覆盖。
- 安装后下一轮 runtime 能 discover skill。
- 公开 skill 必须能人工 review `SKILL.md` 后再启用。

---

## P9：Learning Preset / Skill Patch Proposal

状态：第二轮候选，排在 Script Bridge 和 skill search 之后

目标：

- 基于已验证脚本/重复经验，生成 skill candidate。
- 支持 conservative/balanced/autonomous 配置，但默认 conservative。
- 任何 skill 正文修改先生成 patch proposal。

验收：

- 邮件脚本可以沉淀为 `mail-read` / `mail-send` skill 候选。
- 同类 experience 多次出现时能建议提升为 skill/pattern/wiki。
- skill patch 展示旧内容、新内容、触发原因和 source evidence。
- 用户确认前不改 active skill。

---

## P10：Visualization

状态：后置但可先做只读报告

方向：

- 不先做完整 Web 平台。
- 先做 trace timeline、task tree、memory ledger、skill shelf、workspace health 的只读报告。
- 第一版可以是 CLI/Markdown/静态 HTML，从 trace/testdata/memory 生成。
