# 09：Memory 集成

更新：2026-06-15

## 目标

以 Task + Node 粒度接入 memory：任务完成时统一写 diary/learning，同时记录 node timeline、失败点、attempts 和 evidence refs。

Memory 不直接驱动工具动作，不绕过用户确认和 tool policy。

## 当前机制参考

- `internal/memory`
  - diary markdown
  - learning JSONL
  - skill usage JSONL
  - proposal 机制
- `internal/runtime/hooks.go`
- `internal/runtime/memory_proposal.go`
- 当前 task completion observe

## Memory 输入

任务完成时写入：

- task goal/status/final text。
- graph summary。
- node timeline。
- failed/retried/blocked nodes。
- selected skills。
- trace graph_id/node_id refs。

Node 级摘要包括：

- `id`
- `type`
- `goal`
- `status`
- `attempts`
- `result_summary`
- `evidence_refs`
- `failure_reason`
- `verified_at`

## 集成点

- Node observe：节点完成/失败时更新 graph state，不直接写长期 memory。
- Task completion observe：任务完成时调用 memory，写 diary/learning/skill usage/proposal。
- Heartbeat distill：离线从 task + node evidence 中蒸馏长期经验。

第一版不做每个节点独立长期 memory proposal。

## 必须接入的位置

Memory 集成不能只新增结构体或导出函数，必须接入现有 observe 流：

```text
graph finalizer completed/failed/blocked
  -> task completion observe
  -> memory learning event
  -> diary / learning jsonl / skill usage jsonl / proposal
```

Node 执行期间只更新 session graph state 和 evidence refs，不直接写长期 memory。长期 memory 只在 task-level observe 或 heartbeat distill 中写入。

建议新增 graph memory 输入结构：

```go
type GraphMemorySummary struct {
    GraphID string
    Nodes []NodeMemorySummary
}

type NodeMemorySummary struct {
    ID string
    Type string
    Goal string
    Status string
    Attempts int
    ResultSummary string
    FailureReason string
    EvidenceRefs []EvidenceRef
    VerifiedAt time.Time
}
```

`LearningEvent` 或 memory hook 输入必须能接收该 summary。不能让 memory 继续只从 final text/transcript 猜 node 状态。

## 与 Task Graph 的关系

Task Graph 会让 memory 从 transcript memory 升级为 experience memory。

旧机制主要从 final text、task step 和 transcript 里推断“发生了什么”。Graph 之后，memory 可以直接读取结构化事实：

- task 是否完成。
- 哪些 node 完成、失败、阻塞或重试。
- 每个 node 的 attempts、result summary、failure reason。
- 每个 node 关联的 evidence refs。
- 哪个 skill node 真正产生了结果，而不是只记录读过 `SKILL.md`。

这能减少从长 transcript 猜测的成本，也让失败学习更准确。例如“部署失败”可以落到具体 `node:deploy`，并关联权限、工具、trace 和重试次数。

## Memory 分层

第一版保持 local-first，不引入图数据库或重型 memory service。

建议三层：

```text
Raw evidence layer
  trace / tool result / session graph

Task and node diary layer
  diary markdown / learning jsonl / skill usage jsonl

Distilled memory layer
  proposal / active memory / generated Obsidian views
```

原则：

- 摘要、标签、关系和 embedding 都是索引，不是事实本身。
- 原始 evidence refs 必须保留，用来校验和补细节。
- LLM 可以参与语义归纳，但底层写入、状态、确认和失效规则要 deterministic。

## 关系层设计

当前阶段不做 runtime graph memory，也不让关系检索参与任务执行。

但可以预留轻量关系层，用于后续 heartbeat distill 和 Obsidian 展示。

关系形态：

```text
subject --relation--> object
```

示例：

```json
{"subject":"task:123","relation":"has_node","object":"node:collect","evidence_refs":["trace:abc"]}
{"subject":"node:collect","relation":"used_tool","object":"tool:file.read","evidence_refs":["trace:abc"]}
{"subject":"node:deploy","relation":"blocked_by","object":"permission:missing","evidence_refs":["trace:def"]}
{"subject":"skill:publish","relation":"produced","object":"artifact:cloud_doc_url","evidence_refs":["trace:ghi"]}
```

关系抽取时机：

- 普通任务运行时不抽取关系，不写关系存储。
- 任务完成时只写 task/node diary、learning event 和 evidence refs。
- Heartbeat distill 扫描最近完成的 task graph，生成 relation proposal。
- 用户确认后，才写入 active relation store。

## JSONL 与 Obsidian Markdown

关系类记忆使用 JSONL 作为 source of truth，Markdown 只作为可重建展示视图。

建议目录：

```text
memory/
  relations/
    relations.jsonl
    proposals/
      rel_20260615.md
  entities/
    project/mateway.md
    skill/publish.md
    tool/terminal.run.md
  manual/
    project-mateway-notes.md
```

规则：

- `relations.jsonl` 是真实数据，只由 Mateway 写。
- `entities/*.md` 是 generated view，可随时从 JSONL 重建。
- `manual/*.md` 是用户自由笔记，heartbeat 可读取并生成 proposal。
- 用户不直接编辑 generated entity markdown 来修改事实。
- Obsidian 导出建议由 heartbeat distill 或显式 `memory export` 触发，不放在普通任务执行路径。
- 导出过程只读 active JSONL / proposal 状态并重建 Markdown；它不反向解析 generated Markdown。

Generated markdown 顶部必须标记：

```md
---
generated_by: mateway
source: relations.jsonl
manual_edits: ignored
---

> This file is generated. Manual edits may be overwritten.
```

如果用户要改 active memory，应通过命令或 proposal 流程修改 JSONL，例如后续可提供：

```text
mateway memory edit <id>
mateway memory relation reject <id>
mateway memory relation stale <id>
```

如果用户希望在 Obsidian 中补充自由文本，应写入 `manual/*.md`。Heartbeat 可以读取 manual notes，生成 memory proposal 或 relation proposal；用户确认后再写入 JSONL。这样可以避免“用户编辑 Markdown 之后 JSONL 不同步”的双写问题。

## Skill Usage 规则

Skill usage 必须来自 verified skill node 或 verified node evidence，而不是“发现过 skill”或“读过 `SKILL.md`”。

记录字段至少包括：

- skill name。
- skill node id。
- graph id。
- node result summary。
- verifier status。
- evidence refs。

如果 skill node failed/blocked，也应该记录失败原因供学习使用，但不能当作 successful usage。

## Secret 与 Raw Evidence 规则

- memory 不写 raw trace dump。
- memory 不写 raw tool output，除非已经通过 redaction/compact。
- evidence refs 可以指向 trace/tool result，但 diary 里只写摘要。
- secret-like 内容必须复用现有 redaction。
- generated Obsidian markdown 也必须只包含 redacted summary。

## 版本与失效

长期 memory 必须考虑事实更新。

关系记录后续应支持：

```json
{
  "id": "rel_001",
  "subject": "project:mateway",
  "relation": "uses",
  "object": "go-local-first-runtime",
  "evidence_refs": ["trace:abc"],
  "status": "active",
  "confidence": "medium",
  "valid_from": "2026-06-15T00:00:00Z",
  "valid_to": "",
  "supersedes": [],
  "created_at": "2026-06-15T00:00:00Z",
  "updated_at": "2026-06-15T00:00:00Z"
}
```

第一版可以只在 proposal 中保留这些字段，不要求 runtime 检索使用。

## 实现 TODO

- [ ] 扩展 `LearningEvent` 或新增 graph summary 输入结构。
- [ ] 将 graph memory summary 接入 task completion observe。
- [ ] task completed 时传入 node timeline。
- [ ] failed/blocked task 写入失败 node 和 blocker reason。
- [ ] skill usage 关联到 skill node result，而不是只记录读过 `SKILL.md`。
- [ ] diary 中记录 attempts 和关键 evidence summary。
- [ ] memory proposal 保持用户确认流程。
- [ ] heartbeat distill 默认只读 graph evidence，不写 runtime state。
- [ ] 为后续 relation proposal 预留 task/node evidence refs，不在普通任务路径写关系。
- [ ] 如果生成 Obsidian markdown，必须从 JSONL source of truth 重建，并标记 generated。

## 测试 TODO

- [ ] 简单单节点任务 diary 正常写入。
- [ ] 复杂 graph diary 包含 node timeline。
- [ ] 节点重试后 memory 记录 attempts。
- [ ] 失败任务 memory 能指出 failed node。
- [ ] skill node 完成后 skill usage 关联 node result。
- [ ] discovered-but-unused skill 不写 successful skill usage。
- [ ] failed skill node 不写 successful skill usage，但保留 failure learning。
- [ ] memory 不包含 raw secret-like evidence。
- [ ] memory proposal 仍需用户确认。
- [ ] generated entity markdown 可从 JSONL 重建，手动修改不影响 source of truth。
- [ ] task final text 和 graph node summary 不一致时，以 verified graph state 为准。

## 非目标

- 第一版不做每个节点独立长期记忆 proposal。
- 第一版不做 runtime graph memory。
- 第一版不让关系检索参与普通任务执行。
- 不让 memory 自动执行工具。
- 不让 memory 绕过 human confirm。
- 不把 raw trace dump 写入 diary。
- 不把 generated markdown 当作事实源。

## Codex Review 重点

- memory 是否是 task completion 后的观察结果。
- 是否记录 node 粒度而不是 transcript 猜测。
- 是否避免 secret/raw trace 泄漏。
- skill usage 是否和 node result 关联。
- relations JSONL 和 generated markdown 是否边界清楚。
