# Mateway Skills Collaboration

Last updated: 2026-04-24

## Why This Document Exists

This note is the handoff and coordination document for the current `SKILL.md` migration and progressive disclosure work.

It is written for a development mode where multiple large models, or a mix of human and model contributors, may work on the same area in parallel.

The goal is to keep everyone aligned on:

- what is already true in the codebase
- what invariants should not be broken
- which files own which concerns
- which workstreams can be split safely

## Current State

The current implementation has already crossed the first important migration boundary:

- `SKILL.md` is now the formal skill entry point.
- `_meta.json` is now the optional executable binding layer.
- `skill.yaml` is no longer the primary discovery source.
- doc skills and runnable skills are now distinct runtime concepts.

Current concrete behavior:

1. Skill discovery

- `internal/skills/catalog.go` scans configured skill roots for `SKILL.md`.
- frontmatter is parsed from `SKILL.md`.
- `_meta.json` is optional.
- the catalog retains the `SKILL.md` body, not just summary metadata.

2. Executable binding

- runnable bindings still come from `_meta.json` under the `mateway` field.
- supported binding fields currently include:
  - `type`
  - `entry`
  - `method`
  - `url`
  - `env`
  - `read_only`
  - `risk_level`
  - `tags`

3. Tool exposure

- only runnable skills are exposed as tools by `internal/tools/skills_provider.go`.
- doc-only skills remain visible to the agent as skill context, but are not callable tools.

4. Prompt-side progressive disclosure

- the runtime now injects an `AVAILABLE_SKILLS` block into agent instruction.
- selected skills are summarized under `SELECTED_SKILLS`, but full `SKILL.md` bodies are now loaded through the Eino `skill` activation tool on demand.
- this works for both Eino `chatmodel` and `plan_execute` routes.

5. Skill resources

- the catalog now scans standard skill resource directories:
  - `scripts/`
  - `references/`
  - `assets/`
- extra directories can be declared through frontmatter `resource_dirs`.
- selected skills expose these resource paths in prompt context.
- the runtime now includes a constrained `read_skill_resource` path for lazy loading selected skill files at execution time.

6. Skill picker

- a first-version dedicated `skill-picker` now exists in `internal/harness/skill_picker.go`.
- it uses visible skill discovery metadata only, not full `SKILL.md` bodies, to decide which skills to activate.
- it asks the configured model to return strict JSON with 0-3 selected skills.
- if model selection fails, the runtime falls back to the existing heuristic selector.
- the selected skills are persisted on `Run.SelectedSkills`.
- the selection source is persisted on `Run.SkillPickerSource`.
- a `skill_picker` run step is written into trace for observability.

7. Eino skill activation

- selected skills are now adapted into an Eino skill backend/tool bundle.
- `chatmodel` runs use the official Eino skill middleware handler.
- `plan_execute` runs receive the same Eino skill activation tool in executor tools.
- this means skill activation is no longer only a prompt convention; it is a first-class runtime object.

8. Eino fork bridge

- skill `context: fork` and `context: fork_with_context` now have a first-version bridge to Mateway agent profiles.
- named skill `model` values now resolve through a first-version `ModelHub` bridge.
- forked skill agents inherit the parent run's session scope and are still narrowed by parent capabilities.

This means the runtime is no longer doing only “catalog can read `SKILL.md`”.
It now has an explicit “discover -> pick -> disclose” path.

## Code Map

If multiple contributors work in parallel, use this file map to avoid overlapping edits.

### Discovery and manifest parsing

- `internal/skills/types.go`
- `internal/skills/catalog.go`
- `internal/skills/disclosure.go`

### Runtime selection and agent glue

- `internal/harness/skill_picker.go`
- `internal/harness/harness.go`
- `internal/harness/eino_runtime.go`

### Prompt rendering

- `internal/prompt/assembler.go`

### Tool exposure and execution

- `internal/tools/skills_provider.go`
- `internal/runtime/invoke.go`

### User-facing channel behavior

- `internal/channels/feishu/handler.go`

### Documentation and coordination

- `docs/SKILLS_COLLABORATION.md`
- `docs/CAPABILITIES.md`
- `TODO.md`

## Invariants

These rules should hold unless there is an explicit migration decision.

1. `SKILL.md` stays the primary skill entry format.

2. `_meta.json` stays optional.

- no `_meta.json` means doc skill is still a valid skill
- `_meta.json` only adds runtime binding

3. Doc skills must never be exposed as callable tools.

4. Skill discovery metadata and skill activation context are different layers.

- discovery should stay lightweight
- activation can load `SKILL.md`
- execution may later load scripts, references, and assets on demand

5. The skill picker should choose from visible skills only.

6. The skill picker should be observable.

- selected skills should be visible in run trace
- failures should degrade to heuristic fallback rather than silently disabling skill context

7. `skill.yaml` should not quietly re-enter the main path.

## Current Gaps

The current implementation is still first-version in a few places:

- execution-time lazy loading is now first-version only:
  - standard resource dirs are supported
  - custom dirs are supported when declared in `resource_dirs`
  - arbitrary undeclared directories are still intentionally hidden
- the picker currently uses the general configured model, not a dedicated smaller selector model
- Eino skill activation and fork bridge are first-version only:
  - forked skill agents currently use a simple ChatModelAgent bridge
  - nested skill-in-skill orchestration is still conservative
  - more advanced per-skill agent/model/session policies are not yet exposed
- `_meta.json` schema is still thin for CLI args, API request schema, and output schema
- `/skills` and local TUI are still better at listing than explaining skill categories

## Safe Parallel Workstreams

These workstreams can be split across multiple contributors with minimal overlap.

### Workstream A: Eino skill middleware alignment

Target files:

- `internal/harness/eino_runtime.go`
- new runtime adapter files if needed

Goal:

- move from prompt-only activation toward middleware-like lazy loading semantics
- evolve `read_skill_resource` into richer activation semantics instead of relying only on prompt disclosure
- keep current `skill-picker` as the selector in front of activation

### Workstream B: `_meta.json` schema expansion

Target files:

- `internal/skills/types.go`
- `internal/skills/catalog.go`
- `internal/app/skill_cmd.go`
- `internal/runtime/invoke.go`

Goal:

- add CLI args schema
- add API headers/body schema
- add result/output schema
- align scaffold and runtime around standard `scripts/`, `references/`, `assets/` layout

### Workstream C: UX for `/skills` and TUI

Target files:

- `internal/channels/feishu/handler.go`
- `internal/app/tui.go`

Goal:

- show `doc` vs `cli` vs `api`
- show whether a skill is only informative or runnable
- show selected skills in trace-friendly form

### Workstream D: docs and migration rules

Target files:

- `docs/`
- `README.md`
- `TODO.md`

Goal:

- keep the architecture description aligned with code reality
- keep migration decisions explicit

## Collaboration Rules

When multiple models or contributors work in this area, follow these rules:

1. Prefer disjoint write sets.

- one contributor on `internal/skills/*`
- one contributor on `internal/harness/*`
- one contributor on docs and TODO

2. Do not silently change semantics without updating docs.

- if `SKILL.md` meaning changes, update this doc and `TODO.md`
- if `_meta.json` meaning changes, document the new contract

3. Add tests for every semantic change.

Minimum expected tests:

- skill catalog parsing
- skill picker behavior
- prompt disclosure behavior
- runtime invoke behavior if execution semantics changed

4. Do not revert another contributor's unrelated edits.

5. Keep fallbacks explicit.

- if model picker fails, fallback path should be visible in code and trace

## Recommended Validation

For changes in this area, run at least:

```bash
go test ./internal/skills ./internal/prompt ./internal/harness
```

For cross-runtime changes, run:

```bash
go test ./...
```

## Suggested Next Step

The highest-value next step is still:

- connect the current picker and disclosure path to deeper Eino skill middleware semantics

The current code is now a solid first bridge:

- `SKILL.md` discovery
- optional `_meta.json` execution binding
- explicit skill picking
- prompt disclosure
- constrained resource lazy loading
- trace visibility

The next bridge is:

- true lazy activation and resource loading, not only prompt injection
