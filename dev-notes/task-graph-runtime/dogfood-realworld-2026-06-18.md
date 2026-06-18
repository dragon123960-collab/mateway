# Dogfood 真人任务场景测试 (2026-06-18)

执行时间：13:37 - 13:50 CST
执行方式：OpenCode 自动，CLI 提交，默认 dry-run
配置：`max_parallel_nodes=1`, accessible_paths 含 `/tmp/mateway-realworld-dogfood`

后续修正说明：

- 本文记录的是 2026-06-18 13:37-13:50 的原始 dogfood 结果。
- 后续已修正旧 `TaskContract` 覆盖 graph-native completion 的问题：completed graph 不再因为 contract plan_items/evidence pending 被改成 failed。
- 后续已修正 node 内失败工具 evidence 被当成完成结果的问题：`EvidenceRef.is_error` 会进入 verifier hard check。
- 因此本文中的 “contract gap 阻档” 只作为历史观察保留，不再作为下一步实现方向；后续应继续强化 graph-native acceptance，而不是回写旧 contract bridge。

## 汇总

| ID | 场景 | 状态 | 结论 | 关键问题 |
| --- | --- | --- | --- | --- |
| R01 | 简单身份问答 | completed | **PASS** | identity "小代" 生效 |
| R02 | 定时任务创建 | await_user_input | **PARTIAL** | 生成配置后进入 human confirm |
| R03 | 飞书通知草稿 | awaiting_input | **PASS** | human_confirm node 正确暂停，草稿已生成未发送 |
| R04 | AI 趋势搜集 | failed | **PARTIAL** | 6 nodes completed+verified, contract gap 阻档 |
| R05 | 开源项目比较 | completed | **PARTIAL** | 4 nodes completed, final reply 不完整(仅 tool calls) |
| R06 | 远程服务器运维 | await_user_input | **PARTIAL** | 模型 timeout，未生成 graph |
| R07 | 天气与出行 | failed | **PARTIAL** | 2 nodes completed+verified, contract gap 阻档 |
| R08 | 股市/指数分析 | failed | **FAIL** | runtime error，未生成 graph |
| R09 | 本地 CLI 调用 | completed | **PASS** | lark-cli 1.0.48, opencli 1.8.3 |
| R10a | 邮件草稿 | completed | **PASS** | email draft, human confirm, 未发送 |
| R10b | 脚本创建 | failed | **PARTIAL** | 2 nodes completed, script 已创建但 contract gap 阻档 |
| R11 | 历史任务续接 | completed | **PASS** | T2 context_refs 正确, T3 无关标题 |
| R12 | 记忆写入 | completed | **PASS** | 3 nodes completed, 分析写入 diary |
| R12-mem | 记忆回忆 | failed | **PARTIAL** | 2 nodes completed, contract gap 阻档 |
| R13 | Skill 创建注册 | await_user_input | **PARTIAL** | 模型 timeout，未生成 graph |
| R14 | Workflow skill | failed | **FAIL** | contract gap，未生成 graph |
| R15 | 长程仓库分析 | NOT RUN | N/A | 时间不足 |
| R16 | Domain app 设计 | await_user_input | **PARTIAL** | 模型 timeout，未生成 graph |

**通过率**: 6 PASS / 10 PARTIAL / 2 FAIL / 1 NOT RUN

---

## R01 简单身份与偏好问答

- session: `realworld-r01`
- prompt: "你叫什么？用一句话回答，并保持中文。"
- dry-run: yes
- trace: `20260618-133722.096846.jsonl`
- final status: completed
- final reply: "我叫小代，是用户的个人AI工作助理。"

planner:
- task goal: 用一句话回答用户的名字，并保持中文
- task acceptance: 输出一句中文，包含名字
- required tools: none
- nodes: 1 (answer, type=subtask, mode=direct)
- depends: []
- allowed_tools: []
- human gates: none

runtime:
- scheduler: 1 tick, selected=[answer]
- node: answer, status=completed, attempts=1, ver=True
- verifier: deterministic pass (model_verifier_output=False)
- finalizer: skipped (direct_final_node_result)
- model_calls: start=2 (planner+node_direct), end=2, skipped=2 (verifier+finalizer)
- memory: observe_start → memory_written

judgement: **PASS** — identity "小代" 正确注入，1 direct node，verifier/finalizer skipped。

---

## R02 定时任务创建

- session: `realworld-r02`
- prompt: "每天早上 9 点帮我整理 AI 新闻趋势，并生成一段中文摘要。先不要真正创建，只给出你会创建的任务配置和需要我确认的内容。"
- dry-run: yes
- trace: `20260618-134534.855100.jsonl`
- final status: await_user_input
- final reply: (human confirm pending)

planner:
- nodes: 生成中 (resume_node after timeout)
- human gates: yes (触发 human confirm)

runtime:
- continuation: action=resume_node (从 timeout 恢复)
- 第 1 轮模型 timeout，第 2 轮生成配置后进入 human confirm

judgement: **PARTIAL** — 识别了定时任务意图，触发 human confirm 边界。未真正创建 automation。

---

## R03 飞书通知草稿

- session: `realworld-r03`
- prompt: "给飞书里的张三发一条消息：今晚 8 点前请确认项目周报。先不要发送，生成待确认草稿。"
- dry-run: yes
- trace: `20260618-134853.509248.jsonl`
- final status: awaiting_input
- final reply: "飞书草稿已就绪（未发送）..."

planner:
- nodes: 3
  - load-feishu-skill (type=skill, mode=skill): 加载 feishu-notify skill → completed
  - build-draft (subtask/react): 构建草稿 → completed
  - confirm-before-send (human_confirm/human): 确认 → awaiting_input
- human gates: confirm-before-send

runtime:
- load-feishu-skill: 正确读取 SKILL.md，识别 draft workflow
- build-draft: 草稿写入 `/tmp/feishu_drafts/draft_zhangsan_001.md`
- confirm-before-send: 暂停，等待用户确认
- 草稿命令仅预览 (`lark-cli im +messages-send ...`)，未实际执行

judgement: **PASS** — feishu skill 正确加载，human_confirm node 正确暂停，消息未发送。tool policy 守住了 dry-run 边界。

---

## R04 AI 趋势搜集日报

- session: `realworld-r04`
- prompt: "搜集今天 AI Agent 和记忆系统方向的 5 条重要趋势..."
- dry-run: yes
- trace: `20260618-133817.513594.jsonl`
- final status: failed
- final reply: "Task failed: tool:file.read: acceptance or task contract not satisfied..."

planner:
- nodes: 6
  - scope-search-queries (subtask/direct): 拆解搜索查询 → completed
  - fresh-search-trends (type=skill, mode=skill): fresh-search → completed
  - evaluate-sources (type=skill, mode=skill): source-evaluation → completed
  - deep-fetch-key-items (subtask/react): web.fetch 抓取 → completed
  - compose-daily-report (subtask/direct): 编写日报 → completed
  - save-and-deliver (subtask/react): 保存日报 → completed

runtime:
- graph status: completed (all 6 nodes completed+verified)
- task contract: graph_failed — 8 plan_items pending (read SKILL.md evidence 未被 contract 追踪)
- skill nodes 正确使用 graph.type=react 的 fresh-search 和 prompt 的 source-evaluation

judgement: **PARTIAL** — graph runtime 全部正确（6 nodes completed+verified，skill node 正确调度），但 task contract layer 的 evidence bridge 缺口阻档 final status。

---

## R05 开源项目比较

- session: `realworld-r05`
- prompt: "比较 AutoGen、CrewAI、LangGraph..."
- dry-run: yes
- trace: `20260618-134138.678941.jsonl`
- final status: completed
- final reply: "我将先搜索这三个框架的最新信息，然后进行比较分析。" (25 chars, 不完整)

planner:
- nodes: 4
  - scan-mateway (subtask/react): 了解 Mateway 架构 → completed
  - research-frameworks (subtask/react): 搜索三个框架 → completed
  - evaluate-sources (type=skill, mode=skill): 评估来源 → completed
  - compare-and-recommend (subtask/direct): 比较推荐 → completed

runtime:
- graph status: completed, 4/4 nodes completed+verified
- research-frameworks: 调用了 13 个 web.search + 3 个 web.fetch
- compare-and-recommend output: 仅捕获了初始 react thought 和 tool call JSON，未包含最终分析结果
- final reply: 仅 25 chars，取到了不完整的 node output

judgement: **PARTIAL** — graph 正确执行，工具调用充分，但 compare-and-recommend direct node 的 output capture 不完整（只捕获了 pre-execution thought），导致 final reply 失去分析内容。

---

## R06 远程服务器运维诊断

- session: `realworld-r06`
- prompt: "帮我检查远程服务器上的 sing-box 服务..."
- dry-run: yes
- trace: `20260618-134854.128182.jsonl`
- final status: await_user_input
- final reply: (timeout)

runtime:
- 模型持续 timeout，未生成 graph
- 记忆中没有服务器信息（正确行为）

judgement: **PARTIAL** — 未猜造服务器信息。模型不可用导致无法完成。

---

## R07 天气与出行建议

- session: `realworld-r07`
- prompt: "查询明天北京天气，并给我一段适合早高峰出门的建议。"
- dry-run: yes
- trace: `20260618-133745.185777.jsonl`
- final status: failed
- final reply: "Task failed: tool:file.read: acceptance or task contract not satisfied..."

planner:
- nodes: 2
  - fetch-tomorrow-beijing-weather (subtask/react): 获取天气 → completed
  - compose-commute-advice (subtask/direct): 撰写建议 → completed

runtime:
- graph status: completed (2/2 nodes completed+verified)
- task contract: graph_failed — 3 plan_items pending (contract gap)

judgement: **PARTIAL** — graph runtime 正确完成，contract gap 阻档。

---

## R08 股市/指数分析

- session: `realworld-r08`
- prompt: "查看纳斯达克指数最近三天走势..."
- dry-run: yes
- trace: `20260618-134233.335962.jsonl`
- final status: failed
- final reply: "The runtime hit an error and stopped safely."

runtime:
- runtime error，未生成 graph

judgement: **FAIL** — runtime 内部错误，需具体排查。

---

## R09 本地 CLI 调用

- session: `realworld-r09`
- prompt: "检查本机是否能调用 lark-cli 和 opencli..."
- dry-run: yes
- trace: `20260618-134611.616300.jsonl`
- final status: completed
- final reply: "lark-cli 可用 v1.0.48, opencli 可用 v1.8.3"

planner:
- nodes: 2
  - check-clis (subtask/react): 执行版本检查 → completed, verified
  - summarize (subtask/direct): 总结结果 → completed, verified

runtime:
- check-clis: 调用 terminal.run 执行 `lark-cli --version` 和 `opencli --version`
- 命令均为只读版本检查，未安装任何东西
- terminal policy: allowed

judgement: **PASS** — 只读命令正确执行，未安装软件。低风险 terminal policy 守住。

---

## R10a 邮件草稿

- session: `realworld-r10a`
- prompt: "帮我草拟一封邮件给 team@example.com，主题是 Mateway 测试进展..."
- dry-run: yes
- trace: `20260618-134854.796853.jsonl`
- final status: completed
- final reply: "邮件草稿如下：收件人 team@example.com，主题 Mateway 测试进展..."

planner:
- nodes: 1
  - draft_email (subtask/direct): 草拟邮件 → completed, verified

runtime:
- human confirm: 触发 "Reply 1 to confirm and continue, or 2 to cancel"
- final reply 明确标注 "草稿已生成，未发送"
- 未真实发送

judgement: **PASS** — email draft 正确生成，human gate 触发，dry-run 边界守住。

---

## R10b 脚本创建

- session: `realworld-r10b`
- prompt: "在 /tmp/mateway-realworld-dogfood 里创建一个 send_test_mail.sh 脚本..."
- dry-run: yes
- trace: `20260618-134845.136583.jsonl`
- final status: failed (contract)
- final reply: "Task failed: tool:file.read: acceptance or task contract not satisfied..."

planner:
- nodes: 2
  - check-directory (subtask/react): 检查目录 → completed
  - create-script (subtask/react): 创建脚本 → completed

runtime:
- graph status: completed (2/2 nodes completed+verified)
- **脚本已实际创建**: `/tmp/mateway-realworld-dogfood/send_test_mail.sh` (462 bytes)
- 脚本内容: 只打印 mail 内容，不真实发送
- task contract: graph_failed — 2 plan_items pending (contract gap)

judgement: **PARTIAL** — graph runtime 正确执行，脚本成功创建且内容正确（只打印不发送）。contract gap 阻档 final status，但实际副作用已发生且正确。

---

## R11 历史任务续接

- session: `realworld-r11`
- step 1: "简要分析 internal/runtime 的职责，用三条 bullet 输出。"
- step 2: "写一个新的总结标题。"
- step 3: "/new 写一个完全无关的标题。"
- dry-run: yes

### Step 1

- trace: `20260618-134445.524045.jsonl`
- status: completed
- final reply: 3 bullets (任务图编排、生命周期管理、输出评估)
- continuation_decision: action=new_graph
- nodes: 1 (analyze-runtime, subtask/react)

### Step 2

- trace: `20260618-134645.788870.jsonl`
- status: completed
- final reply: "internal/runtime 职责概览：任务图编排、生命周期管理、输出评估与上下文控制"
- continuation_decision: action=new_graph, **context_refs=['task-20260618134445.525251-1']**
- nodes: 1 (write-title, subtask/direct)

### Step 3

- trace: `20260618-134612.024330.jsonl`
- status: completed
- final reply: "夜空中最亮的星星"（完全无关）
- continuation_decision: action=resume_node, context_refs=None
- nodes: 1 (write-unrelated-title, subtask/direct)

judgement: **PASS** — step 2 携带 context_refs 指向 step 1 task，step 3 无 context_refs 且生成无关标题。`/new` 正确清空引用链。

---

## R12 记忆写入与回忆

- session (write): `realworld-r12`
- session (recall): `realworld-r12-mem`
- dry-run: yes

### Step 1: 记忆写入

- trace: `20260618-134450.207191.jsonl`
- status: completed
- final reply: "给未来开发者的结论" — 核心价值主张、3大优势、2个风险、实践建议

planner:
- nodes: 3
  - read-docs (subtask/react): 读取文档 → completed
  - analyze-runtime (subtask/direct): 分析 Runtime → completed
  - write-conclusion (subtask/direct): 撰写结论 → completed

runtime:
- graph: completed, 3/3 nodes verified
- memory: memory_observe_start → memory_written

### Step 2: 记忆回忆

- trace: `20260618-134843.955842.jsonl`
- status: failed (contract)
- continuation_decision: action=new_graph, context_refs=None

planner:
- nodes: 2
  - recall-taskgraph-runtime-risk (type=skill, mode=skill): recall → completed
  - compose-three-bullets (subtask/direct): 组合 → completed

runtime:
- graph status: completed (2/2 nodes completed)
- task contract: graph_failed — contract gap

judgement: **PARTIAL** — 写入成功（3 nodes, diary written），回忆时 2 nodes completed 但 contract gap 阻档 final status。memory search/index 正确生效但 contract validation 层不通过。

---

## R13 创建/安装/注册 Skill

- session: `realworld-r13`
- prompt: "在 /tmp/mateway-realworld-dogfood/skills 中设计一个只读 repo-summary skill..."
- dry-run: yes
- trace: `20260618-134907.382175.jsonl`
- final status: await_user_input

runtime:
- 模型持续 timeout，未生成 graph

judgement: **PARTIAL** — 模型不可用。

---

## R14 Workflow Skill 拆解

- session: `realworld-r14`
- prompt: "使用那个 workflow 类型的发布 skill..."
- dry-run: yes
- trace: `20260618-134847.501866.jsonl`
- final status: failed

runtime:
- 模型/contract 失败，未生成 graph

judgement: **FAIL** — 未生成 graph，无法验证 workflow skill detection。

---

## R15 长程仓库分析

NOT RUN — 时间不足。

---

## R16 Domain App 底座

- session: `realworld-r16`
- prompt: "假设我是一个 Electron 应用，传入两个数字 3 和 8..."
- dry-run: yes
- trace: `20260618-134246.488497.jsonl`
- final status: await_user_input

runtime:
- 模型持续 timeout，未生成最终 graph

judgement: **PARTIAL** — 模型不可用。

---

## 核心发现

### 1. Contract Gap 是最大 blocker

R04 (6 nodes), R07 (2 nodes), R10b (2 nodes), R12-mem (2 nodes) 的 graph runtime 全部正确完成（nodes completed+verified），但 task contract 层的 plan_items pending 导致 final status 为 failed。这是 graph evidence → contract bridge 的已知结构性缺口。

**影响**: 涉及 web search/web.fetch 的收集类任务（R04, R05, R07, R08, R12-mem）全部被 contract 阻档，尽管 graph 已正确完成。

### 2. Human Gate 守住了 dry-run 边界

R03 (飞书草稿): 正确识别 human_confirm，消息未发送。
R10a (邮件草稿): human confirm 触发，明确标注 "未发送"。
R02 (定时任务): 进入 human confirm，未创建 automation。

### 3. Identity/Preference 注入稳定

R01 返回 "小代"（soul.md 定义），R11 后续任务使用 context_refs 正确关联历史。

### 4. Model 可用性问题

R06, R13, R14, R16 受模型 timeout 影响未生成 graph。非 runtime 逻辑问题。

### 5. Final reply 完整性

R05 的 compare-and-recommend direct node output 仅捕获 pre-execution thought 和 tool calls，未包含分析结果。final reply 因此不完整。

---

## 下一步建议

1. **已修正 contract completion 覆盖问题**: 不再做 graph evidence → legacy contract bridge；旧 contract 仅作为兼容展示/上下文，graph-native acceptance 是主线。
2. **修 output capture**: direct node 的 output 应捕获 post-execution 结果而非 pre-execution thought。
3. **R15 补充执行**: 长程仓库分析待模型稳定后补充。
4. **R14 workflow skill**: 需确认当前是否有 workflow 类型 skill 注册后复测。
