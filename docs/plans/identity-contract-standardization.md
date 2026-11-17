# Identity and action contract standardization

Scope: picker projection, connect target resolution, recipe/baseline persistence,
freeze, reconciliation, daemon/standalone parity and their documentation.

Required invariants:
- Session equality uses logical identity or server-scoped runtime identity.
  Missing evidence is unknown, not different. Project membership is separate.
- Picker action identity includes intent and authoritative target identity.
  Distinct sessions sharing a directory must survive projection.
- Filtering and action availability consume complete observations, including
  the current live session; ranking cannot destroy source observations.
- Connect uses the same action resolution rules as picker projection and
  revalidates runtime receipts before effects.
- Freeze commits a consistent recipe/baseline identity tree atomically.
- Legacy normalization belongs at persistence boundaries, with one recipe
  equivalence contract; it cannot infer live ownership from attributes.
- Desired state and runtime bindings remain separate through typed ports.
- Reconcile retains journal recovery, per-operation preconditions and verified
  binding commits. Its specification must describe the implemented resolver.

Execution order:
1. Separate session comparison from project membership; represent unknown.
2. Replace picker path/name dedup with intent/target keys and a complete-input
   projection; cover refresh, same-path sessions, collisions and current target.
3. Centralize connect decisions and recipe equivalence; remove boolean-driven
   compatibility branches from application orchestration.
4. Enforce freeze and persistence invariants; complete typed repository ports.
5. Align reconcile specification and validate end-to-end parity, recovery and
   migration scenarios in isolated HOME/XDG/tmux environments.

Completion requires evidence for all five stages; passing focused tests alone
is insufficient. No new user-facing flags or workflows are required.

Progress and evidence:
- Session comparison exposes unknown/same/different; project comparison is
  explicit. `target_test.go` covers missing linkage and renamed owners.
- Picker snapshots survive projection and source refresh. `projection_test.go`
  covers current-session collisions, disappearance, same-project sessions and
  stable action IDs. Project navigation is shadowed separately from session
  identity deduplication.
- Saved target ownership resolves through `ResolveSavedTarget` in both picker
  and connect. Connect intent and baseline write policy are explicit enums.
- `Recipe.SameReplay` is shared by template checks and transactional identity
  binding. Replay equivalence deliberately excludes ownership metadata.
- Freeze validates replay and identity tree consistency before transaction,
  and reobserves under the session lock before inspecting operation markers.
- RecipeRepository and BaselineRepository are mandatory Storer contracts;
  application call sites no longer select DTO fallback paths dynamically.
- The reconcile matching section describes durable/runtime anchors; similarity
  is not ownership evidence. Existing journal/recovery implementation remains.
- Full isolated Go suite passed after typed repository conversion. Focused
  projection/replay tests, vet, build and diff checks passed subsequently.

Completion audit:
- Ownership and collision decisions: `model/target_test.go` covers renamed
  owners, name collisions, duplicate owners and contradictory runtime tags.
  Picker and connect call the same resolver. Live effects still validate the
  observed server/runtime receipt through the centralized tmux adapter.
- Projection: `picker/projection_test.go` proves source snapshots survive
  current-session filtering and refresh, preset availability changes when the
  live object disappears, and same-project live sessions keep distinct action
  IDs across presentation changes. Existing parity tests exercise both modes.
- Persistence: typed recipe/baseline contracts are mandatory. Legacy baseline
  envelope decoding is shared by read and revision paths. Recipe normalization
  verifies canonical persisted baseline and recipe replay in one transaction;
  store tests cover rejection without ownership changes. Freeze checks replay
  and identity trees before writes and reobserves under its session lock.
- Desired/runtime separation: Recipe excludes runtime handles; Baseline keeps
  slot-keyed bindings separately. Existing model and reconcile tests cover
  independent copies, binding refresh, journal recovery and exact rebuild.
- Documentation: reconcile matching describes slot/runtime anchors and treats
  semantic roles as desired attributes. Saved-session restoration preserves
  lineage; new-project shape baking creates fresh identities.
- Final verification: full isolated `go test ./... -count=1`, `go vet ./...`,
  `go build ./...`, migration version/immutability checks, sqlc regeneration
  and `git diff --check` passed. Integration tests used isolated tmux servers.

Compatibility DTOs remain only at existing shape/tmux/serialization boundaries;
application repository selection no longer uses dynamic DTO fallback branches.
No schema migration or new user-facing workflow was introduced in this change.
