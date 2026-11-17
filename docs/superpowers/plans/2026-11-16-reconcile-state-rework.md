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
6. [x] Add explicit `Recipe`, `Baseline`, and `ObservedSession` types; move reconcile plans and store baseline boundaries to typed values while keeping `Session` at compatibility adapters.
7. [x] Remove semantic similarity as ownership evidence. Untagged windows/panes remain unmanaged and survive preserve.
8. [x] Save edited recipe and desired baseline revision atomically; retain bindings only for unchanged slots and invalidate stale plans.
9. [x] Replace tmux subprocess/control framing with pinned gotmux APIs; keep one centralized executor and explicit ExistingOnly/start policy.
10. [x] Keep Goose/sqlc as the sole schema and typed query path; add the launch command column without rewriting legacy data.

`Session` remains as a compatibility DTO for shape, edit rendering, and tmux adapters. Reconcile planning, baseline persistence, and live snapshots now use the distinct domain contracts.

## Constraints

- No name-only identity decisions or destructive rollback by name.
- No synthesized identities written into unidentified legacy baselines.
- No schema adoption that silently changes legacy baseline contents.
- No duplicate planner/runner/orchestration path.
- No user-facing flags or mapping controls beyond the approved `--reconcile`, `--hard`, and existing freeze flow.
- Preserve the user's existing uncommitted preset changes.

## Verification boundary

Verification completed on isolated HOME/XDG and tmux sockets: full unit/integration suite, go vet, build, deterministic sqlc generation, Goose migration order, checksum, and immutability checks.
