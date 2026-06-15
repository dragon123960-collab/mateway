# Graph-Native Skill Registration Plan

Updated: 2026-06-15

## Summary

Mateway v2 treats skill discovery as a local registration problem.

```text
A skill is discoverable only after local registration.
Registration creates .mateway/metadata.yaml.
Raw SKILL.md files are treated as unregistered drafts.
```

The runtime should consume registered skill metadata instead of guessing from
raw `SKILL.md` files at task time. `SKILL.md` remains the human-readable skill
body. `.mateway/metadata.yaml` is Mateway's local adapter and graph planner
index.

This note describes the target development plan. It does not change the current
v1 runtime by itself.

## Registration Model

Supported skill layouts:

```text
workspace/skills/<skill_name>/SKILL.md
workspace/skills/<skill_name>/.mateway/metadata.yaml

workspace/agents/<agent_id>/skills/<skill_name>/SKILL.md
workspace/agents/<agent_id>/skills/<skill_name>/.mateway/metadata.yaml
```

Discovery rules:

- A skill is visible only when both `SKILL.md` and `.mateway/metadata.yaml`
  exist.
- A raw copied `SKILL.md` without metadata is an unregistered draft, not a
  runtime error.
- Agent-scoped registered skills override shared registered skills with the
  same name.
- Runtime discovery is read-only and must not create metadata as a side effect.
- Maintenance commands, agent repair tasks, or heartbeat jobs may create
  metadata proposals or perform explicit registration.

## Metadata Shape

`metadata.yaml` version 2 extends the existing install metadata with graph
registration fields:

```yaml
adapter_version: "2"
source: "builtin | local | https://..."
installed_at: "2026-06-15T00:00:00Z"
tool_runtime: "mateway"

graph:
  mode: native | adapted | legacy
  stage: planning | execution | synthesis
  node_type: skill
  granularity: atomic | workflow
  allowed_tools:
    - web.search
    - web.fetch
  inputs:
    - topic
  outputs:
    - summary
  suggested_nodes: []
  safety_notes: []
```

Field semantics:

- `mode=native`: the skill is written for Mateway graph semantics.
- `mode=adapted`: a public or legacy skill has a local graph adapter.
- `mode=legacy`: the skill is registered for prompt use, but not trusted to
  expand complex graph nodes automatically.
- `granularity=atomic`: the planner may use the skill as one skill node.
- `granularity=workflow`: the planner must split the workflow into atomic
  nodes and must not hide multiple tool actions inside one skill node.
- `allowed_tools` limits which real tools the skill node may request or expand
  into. Skill names remain distinct from tool names.

## Development Slices

### Slice 1: Metadata Types

- Extend `internal/skill.Metadata` with a `Graph` section.
- Add typed graph metadata fields instead of unstructured maps.
- Keep backward compatibility for existing metadata with
  `adapter_version: "1"` by treating it as `mode=legacy`.
- Add validation for graph enum values and empty required fields.

### Slice 2: Install-Time Registration

- Update `mateway skill install <source>` so installation always writes both
  `SKILL.md` and `.mateway/metadata.yaml`.
- Use `adapter_version: "2"` for newly installed skills.
- Preserve the original `SKILL.md`; Mateway-specific adaptation belongs in
  metadata.
- Reject secret-like or unsafe prompt content before writing either file.

### Slice 3: Register Command

- Add `mateway skill register <path|name>` for manually copied skills.
- Resolve either a skill directory, a `SKILL.md` path, or a workspace skill
  name.
- Validate the skill body with the same checks used by install.
- Create `.mateway/metadata.yaml` with `source: local` and conservative
  `graph.mode: legacy` unless explicit adapter data is provided later.
- Do not mutate `SKILL.md`.

### Slice 4: Doctor And Orphan Reporting

- Add `mateway skill doctor` reporting:
  - registered skills
  - orphan `SKILL.md` files without metadata
  - metadata without `SKILL.md`
  - invalid metadata
- `doctor` must not silently register orphan skills.
- The output should explain that orphan skills are not discoverable and can be
  fixed with `mateway skill register <path|name>`.

### Slice 5: Runtime Discovery

- Change runtime skill discovery to scan only registered metadata.
- Load `SKILL.md` only after metadata confirms the skill is registered.
- Keep discovery read-only.
- Pass metadata summaries to the planner instead of raw skill headers.
- Preserve existing agent-scope precedence.

### Slice 6: Heartbeat Repair

- Heartbeat may detect orphan skills as maintenance work.
- Default behavior should report or propose metadata creation, not write
  metadata silently.
- User-approved repair can call the same registration path used by the CLI.

## Runtime Integration

Planner behavior:

- The graph planner consumes metadata summaries only.
- A selected skill node reads its `SKILL.md` as node-local instruction.
- `granularity=workflow` skills must be decomposed into atomic graph nodes.
- `mode=legacy` skills can guide planning or reasoning, but should not be used
  as a black-box multi-tool executor.

Executor behavior:

- Skill nodes are prompt/instruction nodes, not tool nodes.
- Real actions still go through registered tools such as `file.read`,
  `file.write`, `terminal.run`, `web.search`, and `web.fetch`.
- Tool policy, path validation, redaction, trace, and evidence handling remain
  runtime/tool boundaries.

Session and trace behavior:

- Trace selected skills by skill name, metadata path, graph mode, stage, and
  node id when graph execution exists.
- Do not persist raw skill bodies into traces unless already redacted and
  required for debugging.

## Non-Goals

- No runtime side-effect that silently registers skills during ordinary task
  execution.
- No treating skill names as tool names.
- No multi-agent supervisor or subagent spawning.
- No gateway business routing.
- No copying old experimental packages such as `runtimev2`, `agentv2`,
  `toolv2`, or `workflowv2`.

## Test Plan

- Discovery ignores `SKILL.md` without `.mateway/metadata.yaml`.
- Discovery includes registered shared and agent-scoped skills.
- Agent-scoped registered skills override shared registered skills with the
  same name.
- `skill install` writes both `SKILL.md` and metadata.
- `skill register` converts a copied local skill into a discoverable skill.
- `skill doctor` reports orphan skills without mutating runtime state.
- Runtime planner receives only registered skills.
- Secret-like or unsafe skill content is rejected during install/register.
- Existing v1 behavior remains covered while graph-native discovery is behind
  the v2 implementation path.

## Documentation Follow-Up

When this plan moves from dev note to implementation:

- Update `README.md` and `README.zh.md` if public skill behavior changes.
- Update `docs/configuration.md` with registration and metadata rules.
- Update `docs/architecture.md` and `docs/execution-flow.md` if runtime
  discovery changes from header scanning to metadata scanning.
- Update `docs/roadmap.md` to remove stale non-goals only after graph runtime
  is intentionally adopted.
