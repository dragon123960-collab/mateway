# TaskGraph Verification Closure + Dead Code Cleanup

Status: approved (2026-06-23)
Branch: TBD (`fix/taskgraph-closure` recommended)

## Goal

Make the TaskGraph runtime's "verifiable subtasks; finishes only when graph evidence satisfies the task or a concrete blocker is known" promise true. Today the task-level verifier is a stub (`internal/session/node_verifier.go:428` ignores `contract`), and node-level local replan is a heuristic single-template replacement capped at depth 1. After this work: one clean execution path, a real task-level acceptance loop, and model-driven node replan.

## Scope

### B — Dead code cleanup (prerequisite, independently verifiable)

#### B.1 Delete legacy TaskContract second-plan path
The unified planner path is the only live runtime path (`runtime.Handle` → `planTaskGraphUnified` → `graph_bootstrap.go:50`). The legacy "TaskContract as a second plan" path is unreachable in production and only kept alive by tests. Remove:

- `internal/runtime/task_contract.go`: `ensureTaskContract`, `generateTaskContract`, `taskContractSystemPrompt`, `renderTaskPlanForReview`, `renderTaskPlanForReviewEN` and their helpers.
- `internal/runtime/task_plan.go`: `handleTaskPlanConfirm`, `shouldPauseForTaskPlan`, and any helpers only referenced from this dead branch.
- `internal/runtime/graph_planner.go`: the zero-caller `planTaskGraph` function.
- `internal/runtime/memory_proposal.go`: the branch dispatching on `PendingKindTaskPlanConfirm` (`memory_proposal.go:106-107`).
- `internal/session/store.go`: the `PendingKindTaskPlanConfirm` constant.
- `internal/gateway/gateway.go`: the `case session.PendingKindTaskPlanConfirm` arm (`gateway.go:558`).
- Tests that only exercise the dead path: `gateway_test.go:312`, `continuation_test.go:42`, `runtime_test.go` cases around `shouldPauseForTaskPlan`/`ensureTaskContract`/`renderTaskPlanForReview*`, and `contract_strategy_test.go` cases for `shouldPauseForTaskPlan`. Delete these; keep tests that exercise the live unified path.
- `classifyContractStrategy` in `contract_strategy.go` is traced in the unified path (`graph_bootstrap.go:80-88`) but changes no behavior. Audit: if it has no live caller after B.1 removal, remove it too; otherwise keep as trace-only.

**Keep** the `session.TaskContract` *struct* as a derived artifact (still used by `validateContractTools`, skill binding, trace events, and stored on the task). Its unused fields are left untouched — out of scope for this spec.

#### B.2 Fix doc/contract text inaccuracy
- `internal/tool/file_tools.go:36`: the `file.write` ToolContract text claims "secret scanning enforced before writing". The reality is path validation + the skill/profile proposal redirect which performs `RejectIfSecretLike`. Arbitrary-path writes do NOT secret-scan (`TestFileWriteAllowsRedactedContent` in `policy_test.go:479` confirms redacted content passes through). Update the ToolContract description to describe the actual behavior.
- `docs/task-graph-runtime.md:40` and `docs/architecture.md:25`: the `script` node execution mode is documented as supported. Reality: `NodeModeScript` tool nodes work; skill `script` execution is explicitly stubbed (`node_executor.go:697`). Update docs to mark skill `script` execution as "planned / not yet implemented" so docs match code. Writing a script-skill executor is explicitly OUT of scope for this spec (deferred to a later spec alongside the skill proposal auto-close loop).

#### B.3 Verification checkpoint
- `go test ./internal/runtime/... ./internal/gateway/... ./internal/cli/...`
- `gofmt -l .` clean
- No change to persisted trace/session fields (TaskContract struct retained).

### A — Task-level verification closure + model-driven node replan

#### A.1 TaskGraph state: RepairAttempts slice
Add to `internal/session/graph.go`:

```go
type TaskGraph struct {
    ...existing...
    RepairAttempts []RepairAttempt `json:"repair_attempts,omitempty"`
}

type RepairAttempt struct {
    Round            int       `json:"round"`
    RepairNodeID     string    `json:"repair_node_id,omitempty"`
    VerifierFeedback string    `json:"verifier_feedback,omitempty"`
    Status           string    `json:"status"` // passed | failed | blocked
    AttemptedAt      time.Time `json:"attempted_at"`
}
```

Persisted with the graph, replayed into trace, preserved across recovery. Node-level `node.Input["attempt_feedback"]` is unchanged — this slice is the task-level accumulated feedback history.

#### A.2 Task-level verifier implementation
Replace the stub body of `VerifyTaskGraphWithContract` (`session/node_verifier.go:427`):

1. **Deterministic layer (always runs, free)**: all nodes `VerificationPassed`; the graph's outputs map contains the keys declared in `contract.FinalOutput`; no node blocked/failed/awaiting_input. If any of these fail, record a concrete gap string and return `failed`/`blocked` without calling the model.
2. **Model layer (gated)**: if the deterministic layer passes, run `verifyTaskGraphWithModel` (new, modeled on the existing `verifyNodeWithModel` in `internal/runtime/verifier_model.go`) when `config.Runtime.TaskVerifier ∈ {always, on_failure}`. `on_failure` (default) skips the model call when the deterministic layer already passed trivially (all-green, outputs present) — to save cost. `off` disables the model verifier entirely.
3. The model verifier input is: `contract.TaskAcceptance` (the acceptance criteria), the final outputs of all verified nodes (compact — not raw tool/evidence traces), and the accumulated `RepairAttempts[].VerifierFeedback` (so a repeat of a repair attempt is told what the previous round failed on).
4. Return `GraphVerificationResult.Status`: `passed | failed | blocked | needs_repair`. `needs_repair` means the task failed but is not a blocker (missing synthesis, salvageable gap). `blocked` means a concrete blocker (missing critical input, blocked dependency) — no repair attempt.

#### A.3 Repair/synthesis node closed loop
In `runGraphTask` (`runtime.go:188`), after the schedule→execute→verify loop and before `VerifyTaskGraphWithContract`:

- If the task-level verifier returns `needs_repair` AND `len(g.RepairAttempts) < config.Runtime.MaxRepairRounds` (default 2, 0-3), append a repair node:
  - `Type`: `NodeTypeModel`, `Mode`: `NodeModeDirect` (deliberately NOT a new enum; narrower surface).
  - `Goal`: synthesise the missing acceptance from the verifier feedback.
  - `Input`: `task_acceptance`, all verified nodes' `Output`, accumulated `RepairAttempts` feedback, and the repair round index.
  - `Depends`: all currently-verified node IDs.
  - `Acceptance.Criteria`: "Produces a complete result that closes the verifier's listed gaps."
- Run the appended node through the existing schedule→execute→VerifyNode flow.
- After it completes, push a `RepairAttempt{Round, RepairNodeID, VerifierFeedback, Status, AttemptedAt}` to `g.RepairAttempts`, emit a `task_repair_round` trace event, persist graph state, and re-run `VerifyTaskGraphWithContract`.
- **Termination**: `needs_repair` persists after `MaxRepairRounds` is reached → status escalates to `blocked`; finalizer reports the concrete blocker and the accumulated feedback. `MaxRepairRounds == 0` disables repair append entirely (degrades to "model verdict only, no auto-repair" — the A档1 behavior).
- Recovery: a graph recovered mid-repair picks up from the graph state; completed verified nodes are skipped, the in-flight/queued repair node runs, and the `RepairAttempts` history is preserved.

#### A.4 Model-driven node-level replan
Replace `localReplanReplacementNode` (`runtime.go:385`) and raise the depth cap:

- **Generator**: a new `replan` segment in `graph_planner_prompt.go` calls the unified planner model to produce a single replacement node (goal, allowed tools, inputs, acceptance). Inputs to the replan call: the failed node's goal/output/failure reason, the verifier feedback, sibling verified nodes' outputs (for context), and the allowed tools whitelist.
- **Validation**: reuse `validatePlanTools` (stage∈{execution,synthesis}, granularity≠workflow, allowed-tools⊆skill metadata).
- **Apply**: still via `session.ApplyLocalReplan` with `ReplacementNodes=[modelGeneratedNode]`; downstream pending nodes are cleared and will wait on the new node's completion.
- **Depth cap**: `localReplanDepth(node) >= config.Runtime.MaxNodeReplanDepth` (default 2, 1-3) → node escalates to `Failed`; the existing trace event `local_replan_limit_reached` continues to fire.
- **Cost guard**: model replan only fires after a node exhausts its retry attempts (`VerificationReplan`); the heuristic shortcut is removed, so retries still precede the model call.

#### A.5 Configuration knobs
Extend `config.Runtime` (location: `internal/config/`) with:
- `TaskVerifier string` — `always | on_failure | off` (default `on_failure`)
- `MaxRepairRounds int` — default 2, range 0-3
- `MaxNodeReplanDepth int` — default 2, range 1-3
- The existing `ModelVerifier` knob (`node_executor.go:1377`) keeps its node-level semantics; the task-level model verifier reuses the same model client plumbing, not the same config value.

Defaults must keep the existing test suite green — current behavior is "no task verifier, depth-1 heuristic replan", so existing tests that depend on the heuristic must be updated to the new model-stub (tests use a fake model, so model-driven replan becomes deterministic in tests).

#### A.6 Verification checkpoint
- New tests:
  - task-level deterministic verdicts (all-pass / missing-output / blocked / awaiting)
  - task-level model verifier: passed / needs_repair / blocked; accumulated `RepairAttempts` ingested on round 2
  - repair node append loop: 2 rounds → passed; 2 rounds → still failing → blocked
  - `MaxRepairRounds=0` degrades cleanly
  - recovery preserves `RepairAttempts`
  - node-level model-driven replan: single replacement node generated and applied; depth cap escalates to `Failed`
- Run `go test ./...` (touches runtime, session, gateway via shared model plumbing).
- `gofmt`.

## Execution order (post plan-exit)

1. Write this spec + commit.
2. Branch `fix/taskgraph-closure` (recommended) off current `codex/local-api-server`.
3. B.1 → B.2 → B.3 (commit). Each sub-item is a checkpoint.
4. A.1 → A.2 → A.3 → A.4 → A.5 → A.6 (commit per stage sub-item).

## Out of scope (explicitly deferred)

- `internal/api/` embedding server (separate spec, C).
- skill `script` execution + skill proposal auto-close loop (separate spec, D).
- multi-agent supervisor / spawn / subagent (non-goal per AGENTS.md).
- gateway business routing (non-goal per AGENTS.md).
- Replacing the `TaskContract` struct itself (kept as derived artifact; scope for a future cleanup).

## Risks / tradeoffs

- **Repair runaway**: hard `MaxRepairRounds` cap + `blocked` fallback; never infinite.
- **Model replan cost**: only after retry exhaustion; single node; bounded depth.
- **Model verifier cost**: default `on_failure` + deterministic-first.
- **B.1 wrong-delete risk**: confirmed unreachable in production via grep; running the runtime/gateway/cli test set will surface any regression.