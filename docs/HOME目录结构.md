# Mateway HOME 目录结构

更新时间：2026-05-29

默认 HOME 是 `~/.mateway`。本文记录目标目录结构，并标注当前 M1、Memory 阶段和 Future/rebuildable 项。详细设计见 `docs/记忆prd.md`。

配置文件和配置项的完整中文解释见 `docs/配置说明.md`。

---

## 目标结构

```text
~/.mateway/
  config/                         # M1
    README.md
    config.yaml
    config.sample.yaml
    mateway.env.sample
    memory.yaml                   # M1 target: learning presets / thresholds
    models/
      <model>.yaml
      <model>.sample.yaml
    channels/
      _README.md
      <channel>.yaml
      <channel>.sample.yaml

  runtime/                        # M1 target, current sessions/trace/run may migrate here
    sessions/
      sess_xxx/
        meta.json
        transcript.jsonl
        task_tree.json
        wip.md
    trace/
      <trace-id>.jsonl
    run/
      mateway.lock

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
      software-install/SKILL.md
      fresh-search/SKILL.md
      source-evaluation/SKILL.md
      connector-gap/SKILL.md
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

---

## 当前 M1 已有结构

当前 init/runtime 已建立或使用：

```text
~/.mateway/
  config/                         # 配置目录：主配置、模型配置、channel 配置、环境变量模板
  workspace/
    agents/main/                  # 默认 agent profile 的 prompt-facing 文件
    skills/                       # shared skills，可被所有 agent 发现
    memory/                       # Markdown 长期记忆库
  sessions/                       # runtime session state、task tree、pending 状态
  trace/                          # 每次任务的 JSONL trace
  observe/                        # diary、reflection、proposal、audit
  indexes/                        # 可重建索引，如 memory_index.json
  schedules/                      # 定时任务和运行记录
  run/                            # runtime lock 等进程状态文件
```

说明：

- 当前已有 `sessions/trace/run` 可逐步迁移到 `runtime/`，不强行一次性搬。
- 迁移前，代码仍可继续读写现有 `~/.mateway/sessions`、`~/.mateway/trace`、`~/.mateway/run`。
- 目标结构中的 `runtime/` 是后续收敛方向。

---

## 当前 HOME 逐项说明

### 根目录

- `~/.mateway/`：Mateway 的本机 HOME。默认由 `MATEWAY_HOME` 或用户家目录推导。这里保存配置、运行状态、trace、workspace、记忆和定时任务数据，不应整体提交到项目仓库。

### `config/`

- `config/`：所有配置文件的入口目录。详细字段解释见 `docs/配置说明.md`。
- `config/README.md`：init 生成的本地配置说明。
- `config/config.yaml`：主配置文件。定义 app、model、memory、learning、skills、scheduler、agents、security、search。
- `config/config.sample.yaml`：主配置模板，供用户参考或复制。
- `config/mateway.env`：本机密钥环境变量文件。用户从 sample 复制后填写，不应提交。
- `config/mateway.env.sample`：环境变量模板，列出模型、搜索、飞书等密钥变量名。
- `config/models/`：模型端点配置目录。
- `config/models/minimax.yaml`：MiniMax 模型配置，默认模型。
- `config/models/minimax.sample.yaml`：MiniMax 模型配置模板。
- `config/models/openai-gpt54-mini.yaml`：OpenAI 兼容模型配置，默认关闭。
- `config/models/openai-gpt54-mini.sample.yaml`：OpenAI 兼容模型模板。
- `config/models/local-mlx.yaml`：本地 `mlx_lm.server` 模型配置，默认关闭。
- `config/models/local-mlx.sample.yaml`：本地模型配置模板。
- `config/channels/`：channel 配置目录。
- `config/channels/_README.md`：channel 配置说明。
- `config/channels/feishu.yaml`：飞书 channel 配置，默认关闭。
- `config/channels/feishu.sample.yaml`：飞书 channel 配置模板。

### `workspace/`

- `workspace/`：可编辑工作区。包含 agent profile、shared skills 和 Markdown memory。
- `workspace/agents/`：agent profile 根目录。每个子目录对应一个 agent id。
- `workspace/agents/main/`：默认 agent `main` 的 profile 目录。
- `workspace/agents/main/agent.md`：默认 agent 的行为规则和回答风格说明。
- `workspace/agents/main/soul.md`：默认 agent 的角色定位和核心目标。
- `workspace/agents/main/user.md`：用户长期偏好和用户画像摘要。
- `workspace/agents/main/tools.md`：工具使用规则和工具边界提示。
- `workspace/agents/main/memory.md`：prompt-facing memory card。只放稳定、短小、几乎每轮都值得注入的记忆摘要或链接。
- `workspace/agents/main/skills/`：agent-specific skills。这里的同名 skill 会覆盖 shared skill。
- `workspace/agents/main/skills/README.md`：agent-specific skills 的放置说明。
- `workspace/skills/`：shared skills 目录，所有 agent 默认可发现。
- `workspace/skills/software-install/SKILL.md`：软件安装、配置、验证任务的默认行为指导。
- `workspace/skills/fresh-search/SKILL.md`：实时信息、今日、最新、价格、天气、版本等任务的搜索指导。
- `workspace/skills/source-evaluation/SKILL.md`：来源可靠性、时效性、官方性评估指导。
- `workspace/skills/connector-gap/SKILL.md`：缺少邮件、SSH、发布等 connector 时的探测和脚本桥接指导。

### `workspace/memory/`

- `workspace/memory/`：长期记忆 Markdown wiki。Markdown 是 source-of-truth。
- `workspace/memory/README.md`：记忆库说明。
- `workspace/memory/schema.md`：长期记忆 frontmatter schema。
- `workspace/memory/index.md`：记忆库入口索引。
- `workspace/memory/log.md`：记忆操作日志入口。
- `workspace/memory/user/`：用户级共享记忆。
- `workspace/memory/user/index.md`：用户级记忆入口。
- `workspace/memory/user/long/`：稳定用户偏好和跨 agent 用户事实。
- `workspace/memory/user/inbox/`：用户级待确认候选。默认不参与 active memory lint/index。
- `workspace/memory/org/`：组织级共享记忆。
- `workspace/memory/org/index.md`：组织级记忆入口。
- `workspace/memory/org/long/`：组织术语、系统、流程、协作知识。
- `workspace/memory/org/inbox/`：组织级待确认候选。默认不参与 active memory lint/index。
- `workspace/memory/agents/`：agent-scoped memory 根目录。
- `workspace/memory/agents/main/`：默认 agent 的长期记忆空间。
- `workspace/memory/agents/main/memory.md`：默认 agent 长期记忆 wiki 入口，不是每轮完整注入的 prompt 文件。
- `workspace/memory/agents/main/index.md`：默认 agent 长期记忆索引。
- `workspace/memory/agents/main/experiences/`：已确认经验。当前 proposal commit 常写入这里。
- `workspace/memory/agents/main/experience/`：目标结构中的经验目录，旧文档可能使用单数；后续会逐步统一。
- `workspace/memory/agents/main/skills/`：已确认 SOP 或 skill 相关长期记忆。
- `workspace/memory/agents/main/patterns/`：抽象策略、模式、可复用思考框架。
- `workspace/memory/agents/main/wiki/`：稳定知识页。
- `workspace/memory/agents/main/inbox/`：agent 级待确认候选。默认不作为 active memory。
- `workspace/memory/agents/main/archive/`：归档、废弃或历史内容。
- `workspace/memory/projects/`：项目级记忆根目录。
- `workspace/memory/projects/<project_id>/context/`：项目背景和上下文。
- `workspace/memory/projects/<project_id>/decisions/`：项目决策记录。
- `workspace/memory/projects/<project_id>/experiences/`：项目经验。
- `workspace/memory/projects/<project_id>/skills/`：项目 SOP 或 skill 候选。
- `workspace/memory/projects/<project_id>/patterns/`：项目内复用模式。
- `workspace/memory/projects/<project_id>/wiki/`：项目知识页。
- `workspace/memory/projects/<project_id>/inbox/`：项目待确认候选。
- `workspace/memory/projects/<project_id>/archive/`：项目归档内容。

### `sessions/`

- `sessions/`：会话状态目录。
- `sessions/<session_key>.json`：某个 session 的 task tree、active task、pending confirmation 或 pending memory/schedule review 状态。
- `sessions/` 中的数据是运行状态，不是长期事实源。

### `trace/`

- `trace/`：JSONL trace 目录。
- `trace/<trace_id>.jsonl`：一次任务的 request、model turn、tool call、hook event、pending、reply 和耗时记录。
- trace 用于调试和审计。持久化记录会做 secret redaction，但仍应视为本机私有运行数据。

### `observe/`

- `observe/`：self-learning 工作区，不是 active long memory。
- `observe/diary/`：任务或 session 的轻量工作日志。
- `observe/reflections/`：失败、retry、低效工具策略等反思。
- `observe/proposals/`：待 review 的 memory/skill/pattern 候选。
- `observe/audit/`：proposal、commit、reject、heartbeat、distill 等审计日志。

### `indexes/`

- `indexes/`：可重建索引目录。
- `indexes/memory_index.json`：由 Markdown memory rebuild 出来的搜索索引。
- `indexes/entity_graph.json`：未来实体关系图索引预留。
- `indexes/sqlite/`：未来 SQLite 派生索引预留。
- `indexes/embeddings/`：未来 embedding/vector 派生索引预留。

### `schedules/`

- `schedules/`：定时任务状态目录。
- `schedules/tasks/`：定时任务定义。任务默认先 pending，试跑成功后 active。
- `schedules/runs/`：定时任务运行记录，包括 test run 和 due run。
- scheduler core 是 channel-neutral，不负责自动把结果发回飞书、邮件或 Slack。

### `run/`

- `run/`：本机运行态文件目录。
- `run/mateway.lock`：gateway 或 runtime 单实例锁。
- 这里的文件可删除重建，不是长期数据。

---

## Prompt 摘要与 Memory Wiki

当前目标结构里有两个 `memory.md`。它们可以互相引用，但不要互相替代：

- `workspace/agents/main/memory.md`：prompt-facing memory card。只放极少数稳定、已确认、几乎每轮都值得注入的要点，也可以只放到长期 memory wiki 的链接。
- `workspace/memory/agents/main/memory.md`：agent 长期记忆库的导航入口。它负责组织链接和索引，真正的长期内容分布在 `experience/`、`skills/`、`patterns/`、`wiki/`、`projects/` 等目录，未来由 `memory.search` 或 `context_hook` 按需召回。

简单说：

```text
workspace/agents/main/memory.md
= 每轮可注入的短摘要 / prompt-facing memory card

workspace/memory/agents/main/memory.md
= 长期记忆库导航入口 / navigation index
```

如果后续实现里发现维护两个入口会增加负担，可以保留 `workspace/agents/main/memory.md` 作为短摘要，把 `workspace/memory/agents/main/memory.md` 简化为自动生成的索引页。

---

## Memory 目录说明

- `workspace/memory/global/`：跨用户/项目的稳定全局认知。
- `workspace/memory/user/long/`：用户偏好和长期工作方式。
- `workspace/memory/user/inbox/`：用户级 proposal。
- `workspace/memory/org/long/`：组织术语、系统、流程、协作知识。
- `workspace/memory/org/inbox/`：组织级 proposal。
- `workspace/memory/agents/main/experience/`：agent 局部经验。
- `workspace/memory/agents/main/skills/`：agent 已确认 SOP。
- `workspace/memory/agents/main/patterns/`：agent 抽象策略。
- `workspace/memory/agents/main/wiki/`：agent 稳定知识入口。
- `workspace/memory/agents/main/inbox/`：待确认 memory proposal。
- `workspace/memory/agents/main/archive/`：归档或废弃内容。
- `workspace/memory/projects/<project_id>/`：项目隔离记忆空间。

Markdown 是长期认知 source-of-truth。`indexes/`、SQLite、向量库只是可重建增强层。

---

## observe 工作区

`observe/` 是 self-learning 工作区，不是长期事实源。

- `observe/diary/`：任务或 session 的轻量工作日志。
- `observe/reflections/`：失败、retry、低效工具策略等反思。
- `observe/proposals/`：待 review 的 memory/skill/pattern 候选。
- `observe/audit/`：proposal、commit、reject、skill patch、rollback 日志。

进入 active long memory 前，内容必须经过 proposal/review/commit 或满足配置允许的自动写入边界。

---

## indexes 可重建层

`indexes/` 保存派生索引：

- `memory_index.json`
- `entity_graph.json`
- `sqlite/`
- `embeddings/`

这些都不是 source-of-truth。删除后必须能从 Markdown、trace、source evidence 重建。

`indexes/` 不应提交 runtime state 或 secrets。

---

## Skills

`workspace/agents/main/skills/` 与 `workspace/skills/` 都是可编辑行为指导，不是可执行 tool。

- runtime 先发现 agent-specific skills。
- 再发现 shared workspace skills。
- 同名 skill 下 agent-specific 优先。
- discovery 会把 name/description/stage/priority 和短 guidance 注入 system prompt。

Self-learning 未来可以提出 skill patch proposal。默认展示新旧对比、触发原因、source evidence，用户确认后修改。

---


## Docker 目录

Docker 不属于当前项目 init 契约。SearXNG 等本地服务放在用户级 compose：

```text
~/.mateway/docker-compose/docker-compose.yml
```

Docker volume/data 目录由用户本地部署决定，不提交到项目。
