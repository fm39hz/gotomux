# Reconcile v2 Implementation Plan

> **For agentic workers:** Implement task-by-task in this order. Keep each task independently reviewable. Unidentified old baselines are out of scope; do not add a migration or compatibility branch for them.

**Goal:** Replace reconciliation with identity-safe, explainable, idempotent convergence toward a frozen baseline.

**Architecture:** Keep four facts separate: session logical identity, baseline window/pane slot IDs, current tmux runtime IDs, and evidence used to associate untagged live objects with slots. Require a complete identified baseline; resolve from a complete read-only snapshot and persist slot bindings only after a safe association is verified. Keep matching and planning pure, tmux effects behind one guarded runner, and orchestration in one path. Do not add user-facing selectors or old-baseline migration branches.

**Tech Stack:** Go, tmux CLI, SQLite (`modernc.org/sqlite`), existing `model`, `store.Storer`, and `tmuxtest.Isolate`.

**Spec:** `docs/specs/reconcile-v2.md`

## Global Constraints

- Additive, idempotent SQLite migrations only; never drop or rewrite an existing table.
- Every store capability is added to `store.Storer` and implemented by `*store.Store`.
- Every tmux integration test uses `tmuxtest.Isolate`; never set `TMUX_TMPDIR` manually or call `kill-server` in a test.
- Any real-binary test redirects both `HOME` and XDG data/config paths.
- No reconcile work is added to daemon startup, polling, or picker hot paths.
- `--force` may skip confirmation only; it cannot skip identity, precondition, or verification checks.
- Window indices are destinations; verified runtime IDs are mutation selectors.

## Review Focus

- Every accepted baseline has a session logical ID and a slot ID for each window and pane. Reject incomplete baselines and require a fresh freeze.
- The session name locates the target; it does not establish baseline identity. Window names are weak evidence only.
- A live object must never receive a baseline slot tag before the association has been checked and verified.
- Repeated or copied tmux user options create duplicate session IDs or slot IDs; fail closed and explain the conflict.
- A window can be linked to more than one session; distinguish object identity from membership.
- A crash between `new-window`, metadata tagging, and journal update must not cause a duplicate on retry.
- External tmux changes between observation and mutation must invalidate stale operations before they apply.

---

### Task 1: Add explicit domain identity and versioned desired-state types

**Files:**
- Modify: `internal/model/session.go`
- Create: `internal/model/reconcile.go`
- Create: `internal/reconcile/core/types.go`
- Test: `internal/model/reconcile_test.go`
- Test: `internal/reconcile/core/types_test.go`

**Interfaces:**
- Add `Session.LogicalID`, `Session.TmuxID` (tmux `session_id`), `Window.SlotID`, and `Pane.SlotID`; existing window/pane `TmuxID` fields remain runtime IDs.
- Define `PolicyPreserve` and `PolicyExact`, evidence class, resolution status, operation ID, and snapshot completeness values in `internal/reconcile/core` types.
- Pin new baseline writes to schema version 3. Reconcile rejects baselines missing required identities.
- Do not migrate baseline rows that omit required identities.

- [x] Add round-trip tests for session, slot, and runtime identity fields.
- [x] Define policy, evidence, resolution, operation, and server-scoped runtime reference types.
- [x] Update model comments to distinguish logical identity from runtime binding.

### Task 2: Observe tmux session, window, pane metadata, and membership

**Files:**
- Modify: `internal/tmux/ctl.go`
- Create: `internal/tmux/identity.go`
- Create: `internal/tmux/snapshot.go`
- Test: `internal/tmux/identity_test.go`
- Test: `internal/tmux/snapshot_test.go`

**Interfaces:**
- Add `Ctl.Observe(ctx, targetName) (*model.Snapshot, error)` returning server generation, session runtime/logical identity, per-window and per-pane identities, and all memberships of windows in the server.
- Add typed methods to read and set namespaced session/window/pane user options by runtime ID.
- Keep `Freeze` as baseline capture while reconcile callers move to the richer snapshot.

- [x] Parse fixture output containing linked windows and duplicate IDs; reject incomplete or inconsistent snapshots.
- [x] Add isolated tmux integration coverage for user-option round trips and linked-window membership.
- [x] Ensure observation brackets metadata collection with server-generation checks and reports a stale/incomplete snapshot as an error.

### Task 3: Add logical identity assignment and safe baseline adoption

**Files:**
- Create: `internal/identity/id.go`
- Test: `internal/identity/id_test.go`
- Create: `internal/tmux/identity.go`
- Modify: `internal/tmux/connector.go` (keep preset orchestration out of the low-level connector)
- Modify: `internal/template/connect.go`
- Create: `internal/template/identity.go`
- Test: `internal/template/identity_test.go`
- Modify: `internal/template/connect_test.go`

**Interfaces:**
- Provide an operation that assigns missing session/slot IDs during freeze only after checking uniqueness across the observed server scope.
- During freeze, assign session identity and window/pane slot IDs only after checking uniqueness across the observed server scope.
- New windows/panes created from a baseline inherit the desired slot IDs.
- Instantiating a reusable preset assigns fresh IDs to the new session tree; reconcile recreation retains baseline IDs.

- [x] Test first-time adoption, existing valid tags, copied duplicate tags, malformed tags, and conflicting tags.
- [x] Ensure failed partial tagging is visible on the next observation and never silently overwrites an existing conflicting value.
- [x] Preserve current freeze/load behavior while assigning fresh IDs to new preset instances and retaining IDs during reconcile recreation.

### Task 4: Implement pure evidence resolver for sessions, windows, and panes

**Files:**
- Create: `internal/reconcile/core/evidence.go`
- Create: `internal/reconcile/core/resolve.go`
- Create: `internal/reconcile/core/resolve_test.go`
- Create: `internal/tmuxlayout/layout.go`
- Modify: `internal/tmux/layout.go` (compatibility wrappers around pure parser)

**Interfaces:**
- `core.Resolve(baseline core.DesiredState, live model.Snapshot) core.Resolution`
- Resolution includes one-to-one matches, unmatched objects, evidence provenance, conflicts, and ambiguity candidates.
- Matching uses lexicographic evidence tiers and explicit unmatched assignments; it has no tmux/store dependencies.

- [x] Test slot-tag match, same-generation runtime match, missing-tag recovery, contradictory IDs, and duplicate tags.
- [x] Test global assignment where local greedy matching is wrong, equal-optimum ambiguity, and cases where name/index alone are insufficient.
- [x] Test pane matching independent of pane index and ensure cwd-only changes do not imply pane loss.
- [x] Test semantic aliases such as `files`/`yazi` with classifier-versioned role evidence.

### Task 5: Define desired-state policy and pure operation planner

**Files:**
- Modify: `internal/model/session.go` (persist `Window.Role` and `Session.ClassifierVersion`)
- Modify: `internal/classify/classify.go`
- Test: `internal/classify/window_role_test.go`
- Create: `internal/reconcile/core/desired.go`
- Create: `internal/reconcile/core/operations.go`
- Create: `internal/reconcile/core/planner.go`
- Create: `internal/reconcile/core/planner_test.go`

**Interfaces:**
- `core.Plan(desired core.DesiredState, live model.Snapshot, resolved core.Resolution, policy core.Policy) core.PlanResult`
- Each operation carries logical target, observed runtime target, server generation, membership, precondition fingerprint, and recovery token where needed.
- Policies distinguish preserve and exact. Interchangeable untagged live candidates are adopted as a group without restarting their processes. Materially ambiguous candidates remain untouched; do not create a competing copy that could duplicate an existing baseline object.
- Preserve leaves display names alone when the semantic role and topology match. Exact emits a destructive rebuild only when the exact desired state is not already satisfied.

- [x] Assert pure deterministic planning and zero operations for any unresolved identity dependency; slot bindings carry evidence fingerprints and precede topology changes.
- [x] Cover missing objects, extra windows/panes, linked-window repair, role normalization, index placement, and preserve/exact semantics.
- [x] Assert index is never used as a mutation identity and no destructive operation is emitted by preserve policy.
- [x] Persist classifier-versioned roles in schema-v3 baselines and recalculate roles when the classifier version changes.
- [x] Block exact rebuild when a baseline window is linked outside the target session.

### Task 6: Add additive baseline and operation-journal persistence

**Files:**
- Modify: `internal/store/store.go`
- Modify: `internal/store/baseline.go`
- Create: `internal/store/reconcile_ops.go`
- Test: `internal/store/reconcile_ops_test.go`

**Interfaces:**
- Add `GetBaselineByLogicalID(id)` and transactional `CommitReconcile` to `store.Storer`; commit verified bindings and operation completion together.
- Add create/update/load journal methods keyed by unique operation ID and baseline logical identity.
- Baseline session and slot identity fields are required for reconcile; reject unidentified rows and ask for a fresh freeze.

- [x] Test additive migration from databases with only the existing baseline table; verify new tagged rows can be found after session rename and a version-7 database receives `baseline.logical_id` without converting old row contents.
- [x] Test transaction rollback: neither bindings nor journal completion persist if any write fails.
- [x] Test journal transitions, duplicate operation IDs, and recovery reads using `isolatedStore`.

### Task 7: Implement guarded, recoverable tmux executor

**Files:**
- Create: `internal/reconcile/runner/runner.go`
- Create: `internal/reconcile/runner/runner_test.go`
- Create: `internal/tmux/reconcile_create.go`
- Create: `internal/tmux/renumber.go`
- Modify: `internal/tmux/reconcile_ops.go`
- Remove old `internal/reconcile/hard.go` during the final cutover.

**Interfaces:**
- Runner consumes one planned operation at a time through a narrow tmux port: `Observe`, `MoveWindowTo`, `LinkWindowTo`, `CreateWindowWithOperation`, `CreateWindowWithOperationKeepingMarkers`, `AddPaneWithOperation`, `ApplyLayoutAt`, `SelectWindowByID`, `KillWindowAt`, `ClearOperationID`, and `StabilizeWindowIndices`.
- `CreateWindowWithOperation(ctx, sessionID string, index int, window model.Window, sessionCwd, operationID string) (windowID string, err error)` returns the actual window runtime ID and resumes via its operation marker or slot ID.
- `AddPaneWithOperation(ctx, sessionID, windowID string, expectedCount int, expectedPaneIDs, expectedPaneSlotIDs []string, pane model.Pane, operationID string) (paneID string, err error)` resumes a pane split that happened before the journal advanced and tags by returned runtime ID.
- Before and after each mutation, it observes the required scope and verifies generation, identity, membership, and fingerprints.
- Exact rebuild retains per-window/pane operation markers until the full restored tree verifies. An unmarked matching slot ID is treated as untrusted output, not recovered work.
- Window-index changes are bracketed by a session-scoped renumber marker containing the prior local/global setting; a resumed run restores that setting.

- [x] Unit-test precondition rejection, successful operation, tmux error, postcondition failure, generation change, and resume after already-completed mutation.
- [x] Integration-test create/tag/retry recovery, move by runtime ID under renumbering, linked membership, and exact-rebuild recovery from a partial tree using `tmuxtest.Isolate`.
- [x] Verify temporary tmux options are restored on success, cancellation, command failure, verification failure, and resumed execution.

### Task 8: Verify through identity lineage and orchestrate commit

**Files:**
- Create: `internal/reconcile/core/verify.go`
- Create: `internal/reconcile/core/verify_test.go`
- Modify: `internal/template/reconcile.go`
- Modify: `internal/template/reconcile_integration_test.go`

**Interfaces:**
- `core.Verify(desired core.DesiredState, freshSnapshot model.Snapshot, resolution core.Resolution, policy core.Policy, plan core.PlanResult) error`
- Orchestrator performs observe → resolve → plan → execute/recover → fresh observe → verify → transactional commit.
- Baseline is not changed on ambiguity, conflict, partial execution, or failed verification.

- [x] Test verify does not bind by index or raw name, checks pane-slot-to-layout-leaf placement, accepts semantic aliases, and preserves a durable live slot whose process role changed.
- [x] Test successful repeated reconcile is a no-op, partial execution does not create duplicates on retry, and a stale no-op journal replans after a window disappears.
- [x] Add the `kho-cong` regression shape: mismatched agent tool intent, preserved Codex extra, files/yazi role alias, and two repeated runs.
- [x] Test materially ambiguous candidates execute no operation; interchangeable untagged groups preserve processes and bind as a set.

### Task 9: Wire CLI policy, confirmation, and explainable reports

**Files:**
- Modify: `main.go`
- Modify: `main_test.go`
- Modify: `README.md`
- Modify: `man/gotomux.1` if reconcile CLI is documented there

**Interfaces:**
- CLI selects a target session and preserve/exact policy; it does not perform matching.
- Preserve remains the default. Exact (`--hard`) has explicit confirmation. `--force` bypasses confirmation only.
- A baseline missing session or slot IDs is rejected and must be recreated with the existing `gotomux -f` action. No window/pane binding flags are added.
- Reports expose status, resolved identity evidence, preserved extras, ambiguity/conflict candidates, and recoverable partial operations.

- [x] Test argument parsing and confirmation paths for preserve/exact and invalid flag combinations.
- [x] Test report states for aligned, changed, preserved extras, ambiguous, conflict, partial/recoverable, and failure.
- [x] Test that a stale `needs_attention` no-op journal is aborted and current state is replanned.
- [x] Verify help/docs use “reconcile” consistently and never describe force as bypassing safety checks.

### Task 10: Remove obsolete reconciliation paths and finish verification

**Files:**
- Modify: `internal/template/reconcile_test.go`
- Remove: old top-level reconcile/apply implementation files after all callers use `internal/reconcile/core` and `internal/reconcile/runner`.
- Modify: `README.md`
- Modify: `docs/todo.md` if reconcile work is tracked there

- [x] Verify any baseline missing required IDs is rejected without tagging or topology mutation; verify a fresh freeze creates a complete baseline.
- [x] Verify exact rebuild resumes after staged replacement creation, original-window removal, and partial slot promotion; reject slot-tag-only copies.
- [x] Compare preserve/exact behavior against the documented acceptance cases, including `kho-cong`, repeated reconciliation, and restoring pane slots to their frozen layout leaves.
- [x] Remove obsolete score/match/refresh code after callers moved to resolver → planner → executor → verifier.
- [x] Run `go test ./... -count=1`, `go vet ./...`, and build both binaries; record exact results.
- [x] Inspect final diff and confirm tests used isolated tmux servers and redirected XDG state.

