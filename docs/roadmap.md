# Roadmap

Mateway's current direction is loop engineering for a small local-first agent runtime.

## Current Mainline

```text
planning contract
  -> selected skill preflight
  -> executable checklist
  -> transcript-driven ReAct execution
  -> evidence evaluator
  -> final answer or blocker
```

## Near-Term Work

- Prompt diet: keep always-on runtime context small and inject freshness, connector, and self-knowledge sections only when triggered.
- Skill context gating: keep default skills as editable assets, but inject only selected skills during execution.
- Contract checklist context: give execution a compact checklist instead of a full JSON contract plus duplicate rules.
- Documentation rebuild: keep README short and move stable architecture, configuration, execution flow, and roadmap notes into `docs/`.

## Later Work

- Docker-backed terminal sandbox for `terminal.run`.
- More precise contract repair when planning misses a relevant skill.
- Better transient retry and fetch failure budgets tied to plan items and required evidence.
- Safer context economy around long sessions, large tool outputs, and retained task contracts.
- Skill and memory crystallization from repeated successful workflows.

## Non-Goals

- No PlanExecute framework.
- No DAG runtime.
- No multi-agent supervisor or subagent spawning.
- No gateway business routing layer.
- No Feishu/Lark-specific runtime branch.
- No command execution tool besides `terminal.run`.

These boundaries are deliberate. Mateway should grow by making the loop more observable, evidence-aware, and locally useful, not by turning into a heavy workflow platform.
