# Reconcile v2: identity-safe convergence

## Status

Implemented design for replacing the previous reconciliation model. This specification covers observation, identity, matching, planning, execution, verification, persistence, and CLI reporting.

## Problem

Gotomux stores a frozen session baseline and later attempts to bring a live tmux session toward it. The current implementation mixes runtime IDs, semantic similarity, window names, indices, execution, verification, and baseline binding refresh. A failed post-apply verification can leave created windows live while retaining stale bindings, so a retry can create further copies. A window name can also differ while its semantic role remains the same.

The failure is primarily a **wrong association between a frozen baseline and live tmux objects**, followed by non-idempotent repair. Window names were allowed to stand in for identity, while the live window set can change independently: names can be edited, windows can be added, and tmux runtime IDs can change when an object is recreated. A repair that guesses wrong can preserve the wrong window, recreate a window that is already present under another name, or repeat that creation on the next run.

Reconciliation must converge from observable state. It must identify the target session, associate live objects with baseline slots using durable identity and multiple independent live-state signals when a tag or runtime binding is missing, preserve running processes by default, and make retries converge without asking the user to operate a second matching interface. It must never turn an uncertain guess into durable identity metadata before that association has been checked.

## Goals

- Model session identity, baseline slot identity, runtime object identity, and session membership explicitly; these are different facts.
- Use tmux runtime IDs and tmux user options as complementary identity evidence.
- Require a complete, identified baseline. If session or slot IDs are missing, ask the user to freeze the intended session again with the existing `gotomux -f` action.
- Match baseline slots to live objects one-to-one using multiple independent signals, explain the evidence, and detect competing assignments.
- Separate desired state, matching, planning, execution, verification, persistence, and CLI policy.
- Make retries idempotent and recoverable after a partial operation or process interruption.
- Preserve live pane processes in the default mode; make destructive pruning explicit.
- Commit new runtime bindings only after identity-aware verification succeeds.
- Keep SQLite schema changes additive and idempotent. Do not build a data migration path for unidentified old baselines.

## Non-goals

- Reconstruct terminal contents or process memory after a pane process has exited.
- Infer a unique identity for two indistinguishable objects without durable metadata.
- Make multiple tmux commands transactional; tmux does not provide a transaction spanning arbitrary window and pane operations.
- Reconcile remote tmux servers in this change.

## Domain model

### Identity layers

Identity has three layers; do not use one layer as a substitute for another:

- `LogicalID`: gotomux-generated identity for the baseline session. It survives session rename.
- `SlotID`: gotomux-generated identity for a required baseline window or pane. It describes the desired slot across reconciliation runs.
- `RuntimeID`: tmux `session_id`, `window_id`, or `pane_id`, valid only within a server generation and while that tmux object exists.
- `ServerGeneration`: current server identity, derived from the connected socket/server evidence. It scopes all runtime IDs.
- `Name`, `Index`, `Cwd`, commands, executable paths, tool roles, layout, and pane order are attributes or match evidence, never primary identity.

The CLI session name locates the target. It is not proof that a live session is the same logical session as a stored baseline. The session `LogicalID` establishes that association; if the live tag is missing, same-server runtime session identity can restore it. Session membership is represented separately from window identity because tmux can link one window into more than one session.

Gotomux stores the session `LogicalID` and assigned window/pane `SlotID`s in versioned, namespaced tmux user options. Runtime IDs identify live objects; slot IDs identify desired baseline entries. A live extra has no slot ID merely because it is in the target session. Operation markers are temporary recovery evidence for objects created during an interrupted mutation, not another user-managed identity system. IDs are unique across the observed server. Duplicate or malformed tags invalidate those tags; names and indices cannot silently repair the conflict. Instantiating a preset creates a new session identity and new slot IDs. Recreating a missing object retains its baseline slot ID.

### Desired state and live snapshot

The baseline is a desired-state document. It must contain the session logical ID and a slot ID for every required window and pane, along with versioned semantic roles (`Window.Role` plus `Session.ClassifierVersion`), required topology, replay data for missing objects, and schema version. Runtime IDs and server generation are cached observations, not the definition of desired state. Session baseline rows remain addressable by logical ID across session rename; the stored name is a locator/display value.

A live snapshot contains server generation; session logical/runtime identity; window and pane logical/runtime identities; session membership; semantic classification; pane command evidence; working directories; and topology. The adapter marks incomplete observations as errors rather than returning a partial snapshot that looks complete.

Window names are presentation values. A versioned semantic classifier maps pane evidence and a small set of canonical names to roles such as `editor`, `files`, `agent`, `git`, or `shell`. Verification compares the role contract and topology. Preserve policy does not rename an otherwise matching window just to copy the frozen display name.

## Matching algorithm

1. Validate that the observation is complete and that the selected session is the session owning the baseline. A session name locates a candidate; a unique session tag/runtime lineage establishes the association. If that association is absent or contradictory, stop before mutation.
2. Match baseline window/pane slots to live objects in this order: valid unique slot tag; same-server runtime binding; then a multi-signal match for an untagged live object. A tag/runtime contradiction is a conflict, never a reason to fall back to the window name.
3. Multi-signal matching compares the whole observable object: pane/tool executable evidence, pane count and topology, normalized project/cwd scope, and semantic role. Window name is a weak hint; index is only a destination coordinate and is not identity evidence. A single name, command, cwd, or topology fact cannot establish a match by itself. A live object already carrying a different slot tag is ineligible.
4. Solve the one-to-one assignment globally, including explicit unmatched candidates. Use lexicographic evidence tiers and hard incompatibilities; do not blend all signals into an opaque scalar score.
5. If several assignments remain, distinguish **interchangeable groups** from material ambiguity. A group is interchangeable only if every assignment has the same required semantic/topology outcome and preserves the same live processes. Assign slots deterministically within that group and persist the association only after verification. This makes no claim about historical ownership. If the alternatives could change which object is repaired, moved, or destroyed, stop preserve-mode mutation for that dependency and report the conflicting evidence.
6. Resolve pane slots within each matched window using the same rules. Pane indices/order are not identity.
7. Return matches, unmatched baseline slots, extra live objects, conflicts, ambiguity, and evidence provenance. Resolution is pure and does not write tags or mutate tmux/store state.

The resolver must be idempotent as a whole: a successful run writes verified slot bindings, so the next observation uses those bindings. If the first run cannot distinguish a candidate safely, it must not create a second copy and then rely on that copy's new tag to hide the original uncertainty.

Session logical-ID and window/pane slot-tag matches are accepted only when unique in the observed server scope. A copied window can copy its tmux user options; duplicate slot tags invalidate the affected bindings and stop matching for those objects.

## Policies

- `preserve` is the default: restore required baseline topology where safe, preserve unclassified/extra live objects, and report them.
- `exact` is the existing `--hard` policy: rebuild the session to match the frozen baseline exactly and terminate the pane processes it replaces. It requires explicit confirmation unless the user supplies `--force`.
- Name normalization and semantic-classifier version are persisted. A classifier-version change triggers re-resolution; it does not silently rewrite identity.

The existing `--hard` flag maps to `exact`. `--force` skips only the interactive confirmation; it never skips identity checks, conflict checks, preconditions, or verification.

An incomplete or unsupported baseline is rejected before mutation and must be frozen again with `gotomux -f`. Interchangeable untagged live candidates can be resolved as a set while preserving their processes, then receive verified slot tags. Material ambiguity is reported before mutation; it never creates a competing copy.

## Planner and operations

The planner consumes a baseline, a complete snapshot, a resolution, and a policy. It emits immutable operations with:

- target session logical ID or object slot ID and observed runtime ID;
- expected server generation and session membership;
- precondition fingerprint;
- operation kind and desired result;
- recovery/idempotency token where the operation creates an object.

The planner never emits an operation for an ambiguous or conflicting identity. Window indices are destination coordinates only. Operations target verified runtime IDs whenever possible.

An exact-rebuild operation records the complete original runtime window set. It creates replacement windows at deterministic staging indices with operation-scoped temporary slot IDs, and markers remain on each new window and pane until the whole exact result verifies. After every replacement verifies, it removes originals, promotes temporary slots to baseline slots, and places windows at their frozen indices. This keeps a valid window in the session throughout and makes every interrupted phase recoverable from slot and operation IDs.

Planning is pure and deterministic for identical inputs. The plan is inspectable and can be rendered as a concise explanation before mutation.

## Executor and recovery

The executor is the only component allowed to issue mutating tmux commands. Before each operation it re-observes the required scope and checks the plan's preconditions. For a semantic identity binding, it also confirms the original baseline-to-runtime association and compares the candidate's planned fingerprint immediately before writing the slot tag. Preserve plans bind verified identities before topology operations, so expected fingerprints describe the observed object rather than a later reshaped state. It applies one operation, observes again, and verifies that the intended object changed. If the server generation, association, or relevant membership/fingerprint changed, execution stops and the caller resolves a fresh plan.

Tmux mutations are not globally atomic. Therefore:

- New objects receive a unique operation marker at creation and their baseline slot ID as early as possible. Exact rebuild retains its markers through final verification and clears them afterward; a slot tag without the matching operation marker is not accepted as proof that a window belongs to an interrupted exact run.
- A durable operation journal records plan ID, completed operations, and observed object IDs. Recovery first observes tmux and advances the journal from facts; it does not blindly replay completed work.
- An operation whose outcome cannot be determined becomes `needs_attention`; it is not retried by creating another object.
- Kill operations require a final exact runtime-ID and membership precondition immediately before execution.
- Exact rebuild records the original runtime window set and stages uniquely tagged replacements before removing originals. Retries identify staged windows and panes through operation markers, never through names or copied baseline slot tags.
- Exact rebuild stops before mutation if a target window is linked into another session; it never kills a shared tmux window as a side effect.
- Temporary options such as `renumber-windows` are restored on every exit path, with restoration failures reported separately.

The executor does not promise rollback of killed processes. It promises safe stopping, explicit partial-state reporting, and idempotent recovery where tmux evidence permits it.

## Verification and commit

Verification consumes the same identity resolution lineage used by planning, then observes a fresh complete snapshot. It checks:

- target session logical identity and server generation;
- every required baseline object is represented by the intended live object;
- required semantic role, pane membership, pane count, and topology;
- preserve/exact policy postconditions;
- no duplicate or contradictory session IDs or slot IDs;
- all created/slot-bound objects have unique runtime IDs and stable slot IDs.

It does not rediscover objects by index or raw name. Only a verified result can atomically update baseline bindings and mark the operation journal complete. A failed verification leaves the desired baseline unchanged and the operation journal available for diagnosis/recovery.

## Persistence

- Extend baseline JSON with schema version, session logical ID, window/pane slot IDs, semantic role/classifier version, and runtime bindings.
- Add a nullable `logical_id` column and partial unique index to `baseline`; new baselines always carry it, and reconcile looks up by logical ID so a session rename does not sever the association. Name lookup is allowed only when the live session tag is missing and same-server runtime identity proves it is the same session; otherwise reject.
- Add an additive SQLite operation-journal table and any needed indexes. Migrations remain idempotent and never drop existing tables.
- Baselines missing required session/slot IDs are unsupported inputs. Reject them and require a fresh freeze; do not infer or migrate their identities.
- Freeze writes the session logical ID and window/pane slot IDs to tmux objects and baseline as one coordinated workflow. A partial metadata write is detected by the next observation and resumed or reported; it must not silently overwrite a conflicting tag.
- Store methods are exposed through `store.Storer`; no reconciliation path reaches the concrete store outside that interface.

## Component boundaries

- `internal/model`: shared persisted identity fields and server snapshot/membership value types (`Session.LogicalID`, `Session.TmuxID` for tmux session ID, `Window.SlotID`, and `Pane.SlotID`; existing window/pane `TmuxID` fields remain runtime IDs).
- `internal/tmux`: snapshot adapter, user-option access, runtime identity, and low-level operation methods. It depends on model values, not on reconcile policy.
- `internal/tmuxlayout`: pure parsing and topology comparison for tmux layout strings; no process or database access.
- `internal/reconcile/core`: pure resolver, policy-aware planner, operation types, and verifier. It depends on model, semantic classification, and pure layout values; it imports neither tmux nor SQLite.
- `internal/reconcile/runner`: effectful operation runner behind narrow tmux and journal ports; it depends on core operations and never owns matching policy.
- `internal/store`: baseline versioning, transactional binding commit, and operation journal.
- `internal/template`: coordinate observe/resolve/plan/run/verify/commit and reject incomplete baselines before mutation.
- `main.go`: parse CLI policy and present confirmation/reporting; no matching logic.

Both standalone and daemon-mediated paths must use the same semantics. Reconcile is an explicit user action and must not add expensive work to daemon startup, polling, or picker hot paths.

## Required invariants

1. No mutation on incomplete observations, unknown server generation, session identity mismatch, duplicate session/slot IDs, or unresolved conflict.
2. Names and indices never establish identity by themselves; matching uses independent evidence and a global assignment.
3. No slot tag is written before its match has been verified. Extras remain untagged. Interchangeable groups may be assigned as a group only when all assignments preserve the same live processes and satisfy the same baseline contract; material ambiguity is not guessed through.
4. Every mutation is guarded by a fresh identity and precondition check.
5. Runtime bindings are committed only after identity-aware verification.
6. A successful reconcile is idempotent on immediate repetition.
7. A retry after partial execution does not create duplicate windows/panes.
8. Preserve mode does not kill user processes.
9. Exact/force never bypass identity or safety invariants.
10. Same-server linked-window membership is modeled rather than mistaken for object deletion.
11. SQLite changes remain additive. Old baselines without required IDs are rejected without mutation and can be replaced by a fresh freeze.
12. Reports distinguish completed, preserved-extra, ambiguous, conflict, partial/recoverable, and failed states.

## Acceptance scenarios

- Close a baseline window and reconcile twice: the missing role is restored once; the second run makes no change.
- Close the baseline agent, create a Codex window, and reconcile twice: preserve Codex; do not duplicate the agent or other roles.
- Keep several semantically interchangeable untagged live editor candidates under a complete baseline: preserve their pane processes, assign the required slots as a group, and make the next reconcile a no-op without creating copies.
- Add a semantically incompatible Codex window while the frozen agent slot is missing: preserve Codex as an extra, restore the agent slot once, and make the next reconcile a no-op.
- Present candidates with materially different evidence but no safe assignment: do not tag them or create a competing copy; report the unresolved dependency and leave live processes untouched.
- Rename and renumber windows: identity remains stable and the plan uses runtime IDs.
- Change `files` to `yazi`: role-equivalent state verifies without a false failure.
- Copy a window carrying gotomux options: duplicate slot IDs are detected and no destructive operation follows.
- Move/link a baseline window to another session on the same server: detect the object and membership state; do not recreate it as if the object vanished.
- Change the session name: find the baseline by logical session ID. Replace a session under a reused name with no surviving identity evidence: stop and ask the user to freeze the intended live session before reconcile.
- Change the live session between planning and execution: fail the stale precondition before the next mutation.
- Interrupt after each mutation boundary and retry: recover or report `needs_attention`, never blindly duplicate completed work.
- Run preserve and exact (`--hard`) policies: preserve process IDs in the former; terminate/rebuild the exact baseline in the latter only after confirmation and complete identity validation.
- Freeze `files` with a running `yazi` pane, rename the live window to `yazi`, then reconcile: the `files` semantic role verifies without renaming the live window.

## Rollout

1. Ship additive schema/model and metadata observation while keeping the existing CLI surface.
2. Add identity resolver and diagnostic plan output; compare resolver results in tests and controlled integration fixtures.
3. Route reconcile through the new planner/executor/verifier and reject baselines missing required IDs.
4. Enable operation journaling and safe retry behavior.
5. Enable exact-policy UX; retain current `--hard` and `--force` semantics.
6. Keep only the core and runner packages; remove old apply/reconcile paths.

