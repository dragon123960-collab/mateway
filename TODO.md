# Mateway TODO

这份 TODO 只做一件事：

- 让 `Mateway` 从“已经能跑的基础版”进入“可持续开发的个人助理与 agent host”

当前已经具备的基础能力：

- Go 宿主运行时
- `mateway init / gateway / doctor / version`
- `~/.mateway/config/` 分目录配置
- `config/models/*.yaml` 模型分片
- `config/channels/*.yaml` 通道分片
- Feishu websocket 常驻接入
- OpenAI-compatible LLM 调用
- `SOUL.md / AGENT.md / USER.md` 提示词装配
- skill 目录扫描与热更新
- `SKILL.md` progressive disclosure + skill-picker 第一版
- selected skill resource inventory + `read_skill_resource` 第一版
- Eino `skill` activation object 第一版
- Eino skill `fork / fork_with_context` bridge 第一版
- launchd 常驻运行
- session transcript 最小持久化
- Feishu 文本入口 turn harness
- 同 session 并发 turn guard
- LLM history injection
- LLM RPM guard + `429` cooldown
- `gateway_state.json` 运行状态输出
- reflection 基础失败分类

---

## 当前北极星

`Mateway` 要同时做好两件事：

1. 成为日常可用的个人助理入口
2. 成为可嵌入业务系统的 agent host runtime

当前阶段优先级：

- 先把 harness 主链与 builtin tools 做稳
- 再把 memory / agent profile / capability filtering 做成稳定协议
- 然后扩多 agent 编排、更多 channel adapter、更多 MCP/tool provider

Eino 接入后的补充原则：

- 优先复用 Eino 已有能力，不重复造轮子
- `Memory / Session / Store` 的持久化仍由 Mateway 平台层负责
- `Agent runtime / interrupt / supervisor / plan-execute / callback / summarization / skill progressive disclosure` 优先采用 Eino
- 任务分解、skills、MCP、tools、CLI、API 的统一编排是主线
- 开发期必须保留“学习摘要 / 执行过程可见”

---

## 已完成基线

这些项不再属于“从 0 开始做”，而是“已有第一版，后续继续增强”：

- [x] Feishu websocket 常驻接入
- [x] prompt layer 第一版
  - [x] `models.system_prompt`
  - [x] `workspace/SOUL.md`
  - [x] `workspace/AGENT.md`
  - [x] `workspace/USER.md`
- [x] 会话最小闭环
  - [x] Feishu session key 第一版
  - [x] 最近几轮消息持久化
  - [x] LLM 带最近历史调用
- [x] 入口护栏第一版
  - [x] 同 session inflight guard
  - [x] 自身消息忽略
  - [x] `message_id` 去重
- [x] LLM 运行时护栏第一版
  - [x] requests-per-minute
  - [x] `429` cooldown
  - [x] transient retry
- [x] 诊断与运行状态第一版
  - [x] `mateway doctor`
  - [x] `~/.mateway/gateway_state.json`
  - [x] health endpoint
- [x] reflection 第一版
  - [x] skill invocation reflection
  - [x] llm turn reflection
  - [x] failure taxonomy: `llm_throttled / session_busy / timeout / llm_error`

---

## P0 Harness 基线

### 1. 统一 harness 主链

- [x] 新增 `internal/harness` 统一 chat / explicit tool run 入口
- [x] 新增 `internal/tools` registry / provider / MCP provider interface
- [x] 新增 builtin tools 第一版
  - [x] `exec`
  - [x] `read_file`
  - [x] `write_file`
  - [x] `list_files`
  - [x] `search_text`
  - [x] `search_history`
  - [x] `read_memory`
  - [x] `write_memory_note`
  - [x] `create_workspace`
  - [x] `create_agent`
  - [x] `spawn`
  - [x] `wait_agent`
  - [x] `web_search`
  - [x] `sandbox_exec`
- [x] skills 通过 shared tool registry 暴露
- [x] harness run state 持久化到正式 `memory/runs`
- [x] spawn / wait_agent 接通第一版 subagent runtime
- [x] Eino runtime bridge 第一版
  - [x] `ChatModelAgent` 接入
  - [x] `Runner` 接入
  - [x] `Interrupt / Resume` 接入
  - [x] `Supervisor` 第一版接入
  - [x] `Plan-Execute Agent` 路由接入第一版
  - [x] Eino callback -> Mateway trace bridge 做厚第一版（agent/model/tool + summary/offload/tool_search）

### 2. 单 agent 对话主链

- [ ] 基础消息路由
  - [x] direct 默认回复
  - [x] group 默认 `mention_only`
  - [x] allowlist 第一版
  - [ ] direct / group / thread session key 规则做稳
  - [ ] shared thread session 与 per-user session 策略明确
  - [ ] `/new` 或等价 session reset 命令
  - [x] `/agent <name>` session agent 切换第一版
- [x] `/approve` `/deny` risky tool 批准第一版
- [x] 多 pending approval + approval id 第一版
- [ ] busy / cooldown 时的用户提示更自然
- [x] 复杂任务自动路由到 Eino `Plan-Execute` 第一版
- [x] 开发态 trace 增强第一版
  - [x] 显示 `goal`
  - [x] 显示 `route`
  - [x] 显示 `visible_tools`
  - [x] 显示 `selected_skills`
  - [x] 显示 planner / replanner / executor agent steps
- [ ] 开发态计划展示：首轮任务分解 / plan artifact 做厚

### 3. 把提示词装配做成稳定层

- [x] 把 `SOUL.md / AGENT.md / USER.md` 明确成正式 prompt layer
- [ ] 支持 workspace 内更多角色文件按需注入
- [ ] 加最小 prompt debug 输出，便于排查“为什么这么答”
- [ ] 把 channel / session / runtime state 的关键信息有控制地注入 prompt
- [x] Skill progressive disclosure 接入 Eino instruction 第一版
- [x] skill-picker 第一版（模型选择 + 启发式回退 + run trace）
- [x] selected skill 资源按需读取第一版（`scripts/ references/ assets/` + `read_skill_resource`）
- [x] Eino `skill` activation object 第一版（on-demand load）
- [x] Eino skill `fork / fork_with_context` + `ModelHub` bridge 第一版
- [ ] Skill progressive disclosure 与 Eino Skill middleware 更深语义对齐（更细的 agent/session/model 策略）

### 4. 把 LLM 配置做实

- [x] 支持从 `config/models/*.yaml` 选择默认模型
- [x] 支持 headers / provider 特殊参数
- [x] 区分“模型配置错误”和“模型服务返回错误”第一版
- [ ] 给 doctor 增加模型连通性检查
- [x] fallback model / backup provider 第一版（按 `models.fallbacks` 顺序切换）
- [x] 不同 provider 的错误分类统一化第一版（额度耗尽 / 供应侧限流 / 本地冷却）
- [ ] prompt/token/cost 基础统计

### 5. 把飞书运行链做稳

- [ ] 补完整飞书 direct / group 行为测试
- [ ] 补 reconnect / error logging
- [ ] 回复失败时给出结构化日志
- [x] 支持基础 reaction / placeholder 策略第一版
- [ ] 未注册事件的噪声日志降级，不影响主日志判断

---

## P1 Agent / Memory / Capability

### 1. Skill 协议产品化

- [x] `SKILL.md` 作为正式 skill 主入口第一版
- [x] 可执行 skill 绑定改为 `SKILL.md + _meta.json` 第一版
- [x] `skill.yaml` 退出主标准
- [x] CLI / API skill 模板脚手架第一版
- [x] skill-picker 第一版
- [x] 标准资源目录协议第一版（`scripts/ references/ assets/`）
- [x] 非标准资源目录声明第一版（`resource_dirs`）
- [ ] 继续评估 `SKILL.md` 主流生态的兼容细节
- [ ] skill 执行时注入统一环境变量
- [x] skill 失败结构化落 reflection
- [ ] 做一个内置 demo skill 集合
- [ ] skill stdout/stderr/result schema 稳定化
- [ ] skill fork agent 的更细粒度策略（nested skill、session sharing、profile override）
- [ ] 更细粒度资源类型与 schema 化 lazy loading

### 2. 会话与记忆

- [x] `workspace/memory/` 目录协议第一版
- [x] session transcript 持久化
- [x] basic reflection index 第一版
- [x] failure index 第一版
- [x] session summary / rolling summary 第一版
- [x] “上次做到哪了”最小召回第一版
- [x] memory search primitive 第一版
- [x] thread / task / agent 维度检索第一版
- [x] run trace / learning summary 第一版（Feishu `/trace` `/learn`）
- [x] markdown wiki memory 第一版
  - [x] `workspace/memory/wiki/` 目录协议
  - [x] `wiki_ingest`
  - [x] `wiki_query`
  - [x] `wiki_lint`
  - [x] `index.md` / `log.md`
- [x] 接入 Eino `Summarization` middleware，减少长上下文重复造轮子
- [ ] reflection 到长期记忆沉淀
- [ ] self-improvement proposal store

### 3. Agent Profiles 与创建

- [x] `workspace/agents/*.md` profile 第一版
- [x] CLI `workspace create/list`
- [x] CLI `agent create/list`
- [x] CLI `channel create`
- [x] agent inheritance merge 第一版
- [x] agent-level capability compile 第一版
- [ ] 对话驱动创建 workspace / agent / channel

### 4. 能力过滤

- [x] user/channel/session/agent 三层 capability compiler 第一版
- [x] visible vs callable 双层语义第一版
- [x] path policy / safe exec 第一版（默认关闭，可配置开启；危险 exec 仍默认拦截）
- [x] approval policy for risky tools 第一版
- [x] 通用 chat tool loop 第一版（`TOOL_CALL <tool> <json>` + autonomous web search fallback）
- [x] structured tool-call envelope 第一版（兼容 JSON envelope）
- [x] run-level approval status / step trace 第一版
- [x] run query interface 第一版（CLI `run list/get` + Feishu `/runs` `/run_status`）
- [x] prompt-injected tool protocol 第一版
- [x] schema-aware tool prompt 第一版（注入 input schema）
- [x] web search provider switching 第一版（duckduckgo / tavily）
- [x] external CLI provider 第一版（配置驱动 `<provider>_list` / `<provider>_run`）
- [x] Eino ToolReduction / ToolSearch 等价能力评估并接入
- [x] skills / tools / MCP / CLI / API 的按任务渐进式披露第一版（动态打分选择器）
- [ ] wiki query 与普通 memory / web search 的统一检索路由
- [ ] callback failure taxonomy / prompt-token-cost 观测继续做厚

### 5. 部署与运维

- [ ] `build` / `install` / `run` 统一脚本
- [x] launchd 管理脚本第一版（install/restart）
- [ ] Linux systemd 首版
- [ ] 日志文件和健康检查文档补齐
- [x] gateway status 文件
- [x] CLI `gateway health/status/restart` 第一版
- [ ] 不再把 CLI 安装进 PATH 当作核心产品能力
- [x] `mateway tui` 本地交互式终端入口第一版
- [x] Feishu read-event / bot-enter 噪声日志抑制

---

## P2 Multi-Agent / Integrations / Self-Iteration

### 1. 多 agent

- [x] `spawn` / `wait_agent` tool slots 预留
- [x] sync subagent run
- [x] async subagent run + notify parent
- [x] coordinator-worker mode 第一版
- [x] shared conversation mode 第一版
- [x] parent-child run trace / result aggregation 第一版
- [ ] 用 Eino `Plan-Execute` 取代手写复杂任务分解
- [x] Qwen3 / thinking-mode 模型自动避开不兼容的 `plan_execute` forced `tool_choice`
- [ ] shared conversation 平台策略与 Eino 协作事件对齐

### 2. Workflow

- [ ] `workspace/workflows/*.yaml` 协议
- [ ] 单宿主 workflow 调度器

### 3. 更多入口与集成

- [ ] Telegram / Slack / Webhook channel 抽象
- [x] channel adapter interface 第一版
- [ ] MCP concrete provider(s)
- [ ] 外部 API integration manifest
- [x] web search provider 第一版
- [x] browser-oriented providers 第一版（`browser_fetch`）
- [ ] skill market / builtin skills

### 4. 更完整的运行时能力

- [ ] 可观测性和诊断页
- [ ] richer run state store
- [ ] proposal-driven self-improvement flow
- [ ] prompt / agent / memory rewrite proposals with confirmation
- [x] schedule CLI + gateway scheduler 第一版
- [x] schedule interval + cron + timezone + run history 第二版
- [x] builtin `schedule_*` tools 第一版（create/list/get/enable/disable/remove）
- [x] agent 自主创建 / 更新 schedule 的产品策略第一版（默认审批、幂等更新、target session/agent 语义）
- [ ] schedule delivery / timeout / target 语义补齐，对齐 OpenClaw cron 能力
- [ ] 开发态学习摘要增强
  - [x] 显示任务目标
  - [x] 显示首轮任务分解第一版（`dev_plan` + `/learn`）
  - [x] 显示可见 skills / tools
  - [x] 显示每步为何选这个 tool
  - [x] 显示失败后的 fallback 路径第一版（policy deny / failed step）
  - [x] `/learn` 高价值结果自动提案写入 wiki memory

---

## Deferred

- [ ] TTS
- [ ] ASR
- [ ] Computer Use

---

## 参考项目输入

当前这一版 Mateway 已经明确吸收了这些项目的部分思路：

- `picoclaw`
  - `turn harness`
  - session / history / reflection
  - runtime boundary 与 host-first 思路
- `hermes-agent`
  - 稳定 session key
  - gateway runtime status
  - shared-thread session 设计
- `free-code`
  - heartbeat / poll 不允许 tight loop
  - 运行时节流参数要可配置
- `OpenClaw`
  - personal assistant + persistent memory + self-hackable host 的产品方向

后续研究时，优先吸收“runtime guard / session model / observability”，不要把整个大内核直接搬进来。
/Users/dongping/project/hermes-agent，这是hermes，
/Users/dongping/free-code，这是free-code，也是参考对象。
https://openclaw.ai/是openclaw
/Users/dongping/project/picoclaw，这是picoclaw
---

## 判断阶段是否完成

满足下面这些，说明当前基础版真正站稳了：

- [x] 飞书里可以连续多轮对话
- [ ] LLM 会读取 workspace prompt 层
- [ ] skill 安装后无需重启即可生效
- [ ] doctor 能指出配置、模型、通道、skill 的真实问题
- [x] launchd / 本地启动链稳定
