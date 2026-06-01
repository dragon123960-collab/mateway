# Mateway 开发 TODO

更新时间：2026-05-29

## 当前主线

当前通用 agent runtime 的基础闭环已经成型，第二阶段第一轮产品化已完成：

```text
AgentCore / Tool / Trace / Memory / Skill / Script / Schedule / Multi-Agent Profile
-> release hardening / dogfooding / internationalization / deeper adapters
```

第二阶段后续目标不是推翻现有 runtime，而是在保持小核心的前提下做增强和优化：

- Release hardening：真实任务 dogfooding、错误体验、安装/升级、文档和回归测试。
- Internationalization：避免非中文开发者被中文 runtime prompt、确认词、错误提示和规则判断卡住。
- Skill source adapters：当前已有 catalog report / adapter status，后续做外部源深度 resolve/install。
- Script Bridge：当前已有基础发现、执行和 secret 注入，后续补 dry-run/help/version 协议。
- Sandbox Runner：当前已有 report 和 evidence，后续补 wrapper 示例与跨平台说明。
- Read-only Workspace UI：当前已有 CLI report，后续补 trace/task/memory/schedule/skill 静态 HTML。

详细设计以 `docs/记忆prd.md` 为准。

## 下一步增强和优化

### N1 Internationalization / English-first Productization

状态：第一轮已实现，后续继续补全更多文案

问题：

- 当前代码、README、测试中有不少中文用户可见文本，例如“确认/取消/保存/忽略/执行”、memory proposal review block、schedule review prompt、友好错误和 followup cue。
- 对中文用户很自然，但非中文开发者调试时会遇到两个问题：
  - 不知道聊天确认应该回复什么。
  - trace/session/pending 里混入中文提示，难以自动化测试和集成。
- 内部标识、config key、trace key、machine output 已大多是英文，但 user-facing prompt 还没有语言策略。

目标：

- 默认仍允许中文用户体验自然，但产品化时要让英文开发者可以完整使用和调试。
- 内部标识、trace key、config key、CLI machine output 保持英文。
- 用户可见自然语言进入可切换 message catalog，不散落在 runtime 逻辑里。

已完成：

- 增加配置：`app.locale: auto | zh-CN | en-US`、`app.message_catalog_dir`。
- 新增内置 `en-US` / `zh-CN` message catalog 和 alias catalog，并支持外部 YAML catalog 覆盖或新增 locale / aliases。
- 首批迁移 approval pending、memory proposal review、agent profile proposal review、schedule review 和 Feishu card 文案。
- 确认词做 catalog 化兼容：内置中英文 aliases，也支持外部 `aliases.confirm`、`aliases.memory_commit` 等扩展。
- README 英文版改为英文操作词优先，并说明中文 aliases。
- 测试覆盖 `en-US` locale 下的 memory proposal、schedule prompt、Feishu card 和 catalog fallback。

后续：

- 继续抽出 friendly runtime errors、`/new` reply、proposal nudge 等剩余用户可见文案。
- 按需补 `de-DE.yaml`、`fr-FR.yaml` 示例语言包。

验收：

- 非中文开发者只读英文 README 即可完成 ask、tool confirmation、memory proposal review、schedule review。
- `app.locale: en-US` 时，runtime 生成英文用户可见提示。
- trace/session/pending 仍保留英文 kind/status/key，不依赖中文字符串做机器判断。

### N2 Release Hardening / Dogfooding

状态：下一阶段优先

待开发：

- 增加真实任务回归套件：代码阅读、文件编辑、web fresh search、script.run、schedule test、memory proposal、agent binding。
- `mateway doctor` 扩展：模型、search provider、scripts、skills、sandbox、agents、memory lint 汇总。
- 文档收口：README 英文版作为默认入口，中文 README 保持完整但不作为唯一说明。
- 打 tag 前检查：`go test ./...`、真实 `mateway test`、`memory lint`、`workspace report`。

### N3 Learning Quality / Reports

状态：候选增强

待开发：

- skill usage/failure rate 按 agent、skill、tool 统计。
- trace quality report：任务成功/失败原因、tool retry、confirmation boundary 命中情况。
- skill patch proposal 去重增强：source hash + target skill + diff key。
- 多 agent memory safe-read scope 验收：确认 agent A 不召回 agent B 私有 memory。

### N4 Connector / Script Bridge Depth

状态：候选增强

待开发：

- script header 扩展 dry-run/help/version。
- `mateway script report <name>` 展示 manifest、required secrets、last run evidence。
- 邮件/SSH/GitHub/publishing 示例 skill + script 模板。
- 缺少脚本时自动生成 integration proposal，而不是直接失败。

### N5 Read-only Workspace Visualization

状态：候选增强

待开发：

- `mateway workspace export-html` 生成静态 HTML。
- trace timeline、task tree、memory ledger、schedule runs、skill shelf、agent profile report。
- 不引入后台服务，不写业务状态。

## 第二阶段看板

### S2.1 Multi-Agent Profile 产品化

状态：已完成第一轮产品化

目标：

- 保留小 runtime，不做 supervisor/spawn/DAG。
- 把已存在的 profile/binding/目录结构变成用户可理解、可检查、可维护的能力。
- 支持多个 agent profile 在不同 channel/account/peer/session namespace 下工作。

已具备：

- `config.agents.default`
- `config.agents.profiles[]`
- `config.agents.bindings[]`
- `workspace/agents/<agent_id>/{agent,soul,user,tools,memory}.md`
- `workspace/agents/<agent_id>/skills/`
- `workspace/memory/agents/<agent_id>/`
- `AgentPool` 按 session/channel binding 选择 profile，并 clone agent 避免 session 状态串线。

已完成：

- `mateway agent list`
- `mateway agent report <agent_id>`
- `mateway agent create <agent_id>`，生成 `agent.md/soul.md/user.md/tools.md/memory.md` 和目录，不引入复杂路由
- `mateway agent bind` / `unbind`，编辑 `config.agents.bindings[]`
- agent profile lint：目录、prompt 文件、memory root、model fallback 基础检查
- 多 agent 绑定测试：channel/peer binding 选择正确 profile

待开发：

- 多 agent 真实任务测试：memory safe-read 不串 agent 的更完整验收
- skill override / tool allow-deny 的更细 lint

验收：

- 两个 agent profile 可以使用不同 model/default prompt/tools guidance。
- Feishu/CLI session 可以按 binding 进入指定 agent。
- agent-specific skill 优先级高于 shared skill。
- agent memory 搜索只召回对应 agent 或共享 scope，不误召回其他 agent 私有经验。

### S2.2 Skill Source Adapter

状态：基础 report / adapter status 已完成，外部源深度 adapter 后续增强

目标：

- 明确 config 里的 catalog 是“搜索入口声明”，不是万能安装协议。
- 为不同 skill 源提供 adapter，而不是把 HTML/JSON 解析逻辑塞进 YAML。

已完成：

- catalog report：显示 enabled、trust、search_url、install_url、adapter 支持状态
- raw `SKILL.md` 安装继续保留为基础能力
- skill patch promote 走 proposal/diff/audit，不直接改 active skill

待开发：

- 外部源深度 adapter：search / resolve / install

验收：

- 搜索结果明确来源、风险和是否可自动安装。
- 不支持自动安装的源只给 review URL，不伪装成已安装。
- 安装后下一轮 runtime 能 discover skill。

### S2.3 Script Bridge

状态：基础发现 / 执行 / secret 注入已完成，复杂 manifest 后续增强

目标：

- 邮件、远程服务器、自媒体发布等先通过用户脚本/skill 接入。
- 不做重型 connector framework。

已完成：

- 脚本目录约定：`~/.mateway/scripts/`、`workspace/scripts/` 和 `scripts.dirs`
- 轻量 header 协议：`mateway.name`、`mateway.description`、`mateway.risk`、`mateway.required_secret`
- `script.run` tool 走 guarded mutation 确认边界
- tool evidence 记录脚本路径、参数、exit code、duration

待开发：

- 脚本 dry-run/help/version 规范

验收：

- Agent 能发现邮件收发脚本并基于真实输出总结。
- 缺少脚本或参数时追问，不编造完成。
- 可复用脚本能沉淀为 skill candidate。

Secret 边界：

- Skill 和脚本 manifest 只能声明 `required_secrets`。
- 明文用户名、密码、token、API key 不写入 `SKILL.md`。
- 执行时由 Script Bridge 从 `mateway secret` 读取并注入子进程环境变量。
- trace、memory、proposal 只记录 secret id 和 redacted marker。

### S2.4 Sandbox Runner

状态：runner report 已完成，wrapper 示例后续增强

目标：

- 把现有 `security.terminal_sandbox` 从配置能力升级为可验证 runner。

已完成：

- runner report：当前 mode/workdir/timeout/prefix
- 命令运行 evidence 中明确 sandbox 状态

待开发：

- wrapper/prefix 示例
- macOS/Linux 不同 runner 的文档边界

验收：

- sandbox 开启后 terminal.run 的 workdir/timeout/prefix 可验证。
- 失败时安全降级，不影响 file/search/memory 基础能力。

### S2.5 Read-only Workspace UI

状态：CLI workspace report 已完成，静态 HTML 后续增强

目标：

- 不先做完整 Web 平台。
- 先做只读报告或静态 HTML。

已完成：

- `mateway workspace report` 汇总 trace、memory、learning、skill、script、schedule、sandbox 状态

待开发：

- trace timeline
- task tree
- memory ledger
- schedule runs
- skill shelf
- agent profile report

验收：

- 本地一条命令生成可读报告。
- 不写入业务状态，不需要后台服务。

---

## 第一阶段归档

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

状态：基础闭环已完成

原则：

- Heartbeat 只做可审计、可回滚的 memory maintenance。
- Schedule 是 channel-neutral 的任务登记和执行系统，不内置飞书/邮件/Slack 投递。
- 用户显式创建定时任务时，先记录 pending，再询问是否现在试运行。
- 用户回复“执行/试运行”后运行一次；成功才激活定时任务。
- 如需通知用户，通知必须是任务内容的一部分，由已有 tool、脚本、connector 或 skill 完成。

验收：

- `mateway memory heartbeat lint-index` / `serve` 可用。
- `mateway schedule create/list/test/activate/pause/run-due/serve` 可用。
- 飞书里创建定时任务后会进入 `schedule_review` pending。
- 用户回复“执行”会试运行，成功后回复“已添加定时任务”。
- 到点执行只写 `~/.mateway/schedules/runs/` run record。

## 当前短 TODO

- v0.1.4：合并 main、打 tag、验证 GitHub release workflow 产物。
- README / README.zh 已更新到当前能力集，后续只做发布措辞微调。
- 下一阶段优先级：Multi-Agent Profile 产品化 -> Skill Source Adapter -> Script Bridge -> Sandbox Runner -> Read-only Workspace UI。

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
