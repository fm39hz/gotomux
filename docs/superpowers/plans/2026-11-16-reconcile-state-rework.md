# Reconcile State Rework Implementation Plan

**Goal:** Make preset creation, freeze, and reconcile obey one identity-safe state lifecycle and make stored reconcile plans safely versioned and recoverable.

**Architecture:** Keep the existing Goose/sqlc store, tmux adapter, pure reconcile core, runner, and template orchestration. Close inconsistent contracts at the data boundaries; do not add a parallel service or user-facing workflow.

**Spec:** [Reconcile State and Identity Design](../specs/2026-11-16-reconcile-state-design.md)

## Work sequence

1. [x] Correct preset identity lifecycle and keep unidentified legacy baselines untouched.
2. [x] Version reconcile plan JSON. Accept only the current envelope; abort unstarted old plans and quarantine any plan whose mutation state may be uncertain.
3. [x] Prevent terminal verification loops. Preserve can replan only after completion and absence of target operation markers; exact stays in `needs_attention` when safe ownership proof is unavailable.
4. [x] Keep layout normalization slot-based and operation-time mapping runtime-based (existing implementation retained).
5. [x] Carry runtime session ID and server generation through picker attach; separate create, attach, and preset intents; scope load rollback to the exact created session ID.
6. [ ] Split `model.Session` into explicit preset recipe, baseline, and live observation types and migrate remaining call sites.
7. [x] Confirm store persistence remains on the existing Goose/sqlc path; no parallel schema/SQL path added.

The unchecked domain-type split remains a required follow-up stage; the current model is still overloaded at non-reconcile call sites.

## Constraints

- No name-only identity decisions or destructive rollback by name.
- No synthesized identities written into unidentified legacy baselines.
- No schema adoption that silently changes legacy baseline contents.
- No duplicate planner/runner/orchestration path.
- No user-facing flags or mapping controls beyond the approved `--reconcile`, `--hard`, and existing freeze flow.
- Preserve the user's existing uncommitted preset changes.

## Verification boundary

The implementation environment instructions prohibit adding or running tests unless explicitly requested. Keep changes limited and inspect diffs, migrations, interfaces, and error paths statically; report verification limits clearly.
