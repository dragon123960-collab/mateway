# Mateway 项目开发导览

更新：2026-06-09

这份文档面向后续维护者和接手开发的大模型。它记录当前项目事实、核心机制和下一步方向。文档说明可使用中文；代码标识、tool name、config key、trace key、prompt guidance 和 machine-readable output 保持英文。

## 1. 当前定位

Mateway 是一个 Go 版 local-first small Agent Runtime。当前主线是精简重写，不做 heavy workflow platform，也不做多 agent supervisor / spawn / DAG routing。

当前保留的主干：

```text
channel / gateway / config / ~/.mateway / session namespace
transcript-driven AgentCore
small hard tools
hook-first runtime
trace ledger
white-box Markdown memory
```

主流程：

```text
channel -> gateway -> runtime setup -> AgentCore loop -> tools -> observe/finalize
```

Runtime 不做语义路由，不做 completion review，不做 chat approval pending。模型在 `AgentCore` 循环里根据 transcript、system context、tool contracts 和 task contract 自己推进；工具层负责真实动作和硬边界。

## 2. 代码地图

- `internal/channel`: channel package 只做 I/O 和消息归一化。Feishu、Weixin、CLI 都归一到 `channel.InboundMessage`。
- `internal/gateway`: 负责 session key、dedupe、异步运行和 channel serving。
- `internal/runtime`: 负责 setup、system context、task contract、hooks、context budget、memory proposal review、finalize。
- `internal/agentcore`: transcript-driven model/tool loop。它只知道 model、tools、hooks、messages，不知道具体 channel。
- `internal/tool`: built-in tool registry 和工具实现，包括 `file.*`、`file.edit`、`terminal.run`、`web.*`、`secret.set`、`schedule.manage`、task recall、`toolresult.read`。
- `internal/session`: session state、task tree、pending action、task steps、execution events。
- `internal/model`: provider clients、native tool calling、text tool-call fallback、usage parsing、reasoning cleanup。
- `internal/memory`: Markdown memory、proposal、lint/index/search、diary/reflection、heartbeat distill、skill learning。
- `internal/schedule`: local scheduled task store 和 run records。
- `internal/config`: `~/.mateway/config` loader、model config、agent profile config、security config。
- `cmd/mateway`: CLI command entrypoints。

## 3. 功能特色

- `Trace Ledger`: 每次运行写 JSONL trace，记录 request、model turns、tool calls/results、hook events、token/cache usage、context budget telemetry、final reply。
- `Task Contract`: 每个 task 可生成轻量完成契约，要求最终回答前满足必要 tool evidence，避免模型只说计划不执行。
- `Tool Evidence`: tool result 经过 `observe_hook` 变成 task step，记录 status、risk、mutation、accepted evidence 和 summary。
- `Context Budget`: 每轮模型调用前估算 token，超 soft budget 时压缩旧 transcript/tool result，超 hard budget 时停止。
- `Dynamic Visible Tools`: 每轮只向模型暴露相关 tool schemas/contracts，执行层仍保留完整 registry。
- `Raw Ref Retrieval`: 大型 tool result 压缩后保存 `raw_ref`，模型可用 `toolresult.read` 按 query 回读命中行。
- `Skill Guidance`: `SKILL.md` 是 prompt guidance，不是可执行能力；需要动作仍必须走真实 tool。
- `Multi-Agent Profile Foundation`: `config.agents.profiles[]`、channel bindings、agent-specific skills、agent-scoped memory directories 已存在，当前不做 supervisor。

## 4. 记忆系统

Mateway 的 memory 是 white-box Markdown 工作区，不是黑箱向量库。

主要路径：

```text
~/.mateway/
  workspace/
    memory/
      agents/<agent_id>/
      user/
      projects/
  observe/
    diary/
    reflections/
    proposals/
    learning/
    audit/
  indexes/
    memory_index.json
    memory_distill_state.json
    learning_distill_state.json
    skill_learning_state.json
```

结构说明：

- `workspace/memory/` 是长期 Markdown 记忆根。
- `workspace/memory/agents/<agent_id>/` 是 agent-scoped long-term memory。
- 每个 agent profile 有 prompt-facing 文件：`agent.md`、`soul.md`、`user.md`、`tools.md`、`memory.md`。
- `observe/diary/` 保存任务日记，记录 session、task、goal、status、steps、final reply。
- `observe/reflections/` 保存失败、用户纠正、suspect step 的反思。
- `observe/proposals/` 保存待审核记忆候选。
- `indexes/memory_index.json` 是可重建索引，不是事实源。

Memory 使用方式：

- `memory_safe_read` 在相关任务中检索 `workspace/memory/`，最多注入 3 条相关 snippets。
- `mateway memory lint` 检查 Markdown memory。
- `mateway memory index rebuild` 重建 `memory_index.json`。
- `mateway memory search <query>` 搜索本地记忆。
- `mateway memory proposal list/show/commit/reject` 审核或提交候选记忆。

## 5. 自我学习与自我沉淀

完成任务后，`observe_hook` 会记录 task evidence，并在适当时触发 `memory.RecordTaskCompletion`。

当前沉淀链路：

```text
completed task
  -> accepted/suspect/failed tool steps
  -> diary
  -> optional reflection
  -> optional memory proposal
  -> user review: 1 save / 2 ignore
  -> Markdown long-term memory
  -> rebuildable index
```

规则边界：

- `RecordTaskCompletion` 总是写 diary。
- 如果有用户纠正、失败步骤或 suspect step，会写 reflection。
- 如果 task goal 或 final text 有强 memory cue，会生成 memory proposal。
- proposal 进入 pending review；聊天入口只接受 `1` 保存、`2` 忽略。
- Runtime 不静默写长期记忆，不静默改 skill。
- skill usage 和 learning events 会进入后续 heartbeat distill。

## 6. Heartbeat 系统

Heartbeat 是离线维护和自我沉淀入口，不是另一个 agent supervisor。

当前源码真正支持的 heartbeat jobs：

- `lint-index`: lint Markdown memory；无 error 时重建 `indexes/memory_index.json`。
- `memory_distill`: 从 `observe/diary/` 和 `observe/reflections/` 中提炼 experience proposal。
- `learning_distill`: 从 diary/reflection 和 `observe/learning/events.jsonl` 中提炼 experience proposal。
- `skill_learning`: 从 skill usage / learning events 生成 skill patch 或 new skill proposal。

命令入口：

```bash
mateway memory heartbeat lint-index
mateway memory heartbeat distill
mateway memory heartbeat learning
mateway memory heartbeat skill
mateway memory heartbeat serve
```

配置注意：

- `NormalizeHeartbeatJobs` 会把 `memory_lint`、`memory_index_rebuild`、`memory_lint_index` 归一为 `lint-index`。
- `memory_distill` / `distill`、`learning_distill` / `learning`、`skill_learning` / `skill` 会归一到对应 job。
- 如果配置里出现 `memory_daily_review`、`memory_recent_compact`，当前源码未实现这些 job，会被 normalize 忽略，不应写成已运行能力。

## 7. Dream 功能

当前没有独立 `dream` runtime、job 或 tool。

现有最接近 dream 的机制是 heartbeat distill：离线扫描 diary、reflection 和 learning events，把重复经验、失败模式和 skill 使用证据提炼成 proposal。

如果后续要做 dream，建议作为显式 heartbeat job，例如 `dream_reflect`：

- 输入仍来自 trace、diary、reflection、learning events 和 memory index。
- 输出仍进入 `observe/proposals/` 或 skill proposal。
- 不直接修改 long-term memory、agent profile 或 skill。
- trace/audit 中必须记录来源和模型输出摘要。

## 8. Scheduler

`schedule` 是本地任务状态和运行记录系统。

当前能力：

- `mateway schedule create`
- `mateway schedule list`
- `mateway schedule test`
- `mateway schedule activate`
- `mateway schedule pause`
- `mateway schedule run-due`
- `mateway schedule serve`

执行方式：

- schedule task 存在本地 state。
- 到期任务通过 runtime 走同一套 `AgentCore`、tools、trace、session。
- run record 保存 status、output、trace path 和 error。
- schedule 是 channel-neutral，目前不自动把结果发回 Feishu、Weixin、email 或其他 channel。

## 9. 安全边界与 Sandbox

当前安全模型：

- `terminal.run` 默认 direct shell。
- destructive command hard block，例如 `rm`、`rmdir`、`shred`、`git reset`、`git clean`。
- 命令执行有 timeout。
- workspace path policy 限制文件工具和部分 read-only command path。
- secret scan 拒绝把已知 secret value 直接放进 command。
- `terminal.run.env_secrets` 通过 secret id 注入环境变量，trace 只记录 secret id 和 env name。
- 不存在 chat approval pending；非 destructive 动作直接执行，destructive 动作直接拒绝。

`security.terminal_sandbox` 现状：

- 配置和 evidence 字段已存在。
- 当前不是完整 OS-level isolation。
- 开启后 `terminal.run` 会记录 sandbox mode/workdir evidence，并按配置 workdir/prefix 执行。

下一步 sandbox 方向：

- Docker-backed execution isolation。
- 明确 mount allowlist、workdir、network policy、env secret injection、CPU/memory/time limit。
- 所有 sandbox 参数和结果写入 trace evidence。
- Docker sandbox 失败时应返回明确 blocker，不降级成不透明 direct shell。

## 10. 精简版边界

当前已删除或应继续清理的旧层：

- follow-up router
- completion review
- chat approval pending
- localized runtime action aliases
- Script Bridge / `script.run`
- 多 agent supervisor / spawn / DAG routing

仍保留的人工确认：

- memory proposal review：用户回复 `1` save，`2` ignore。
- agent profile / skill proposal promotion：通过显式 CLI 命令 promote/reject。

## 11. 下一步开发方向

优先级较高：

- Tool-call parser hardening：尤其是 text protocol fallback、Minimax/local model sentinel、malformed tool call repair。
- Contract blocker handling：required tool 被 policy block 或持续 failed 时，不再反复要求“再用一次工具”，而是给出 blocker 或替代路径。
- FinalText gate：contract unsatisfied 时 response 应标 failed/partial，避免把承诺文本当成最终成功回复。
- Reasoning cleanup：`strip_reasoning` 应覆盖 OpenAI Chat `message.reasoning` 字段，并谨慎处理 `<think>` 类文本。
- Docker sandbox：把 direct shell 逐步替换为可配置隔离执行。
- Heartbeat job/config 对齐：配置里出现的 job 必须要么实现，要么 doctor 明确提示 unsupported。
- Approval residue cleanup：保持 runtime 无 chat approval pending，只保留 hard block 和 explicit proposal review。

接手开发建议：

1. 先跑 `go test ./...`。
2. 读 `docs/runtime.md`、`docs/tools.md`、`docs/configuration.md`。
3. 用 `mateway trace` 或 `/events` 看最近 trace。
4. 看 `internal/runtime/runtime_test.go` 和 `internal/agentcore/loop_test.go` 理解主状态机。
5. 修改 runtime 前先确认能不能通过 prompt/skill/tool contract 解决。
6. 修改 README 时同步 `README.zh.md`。
