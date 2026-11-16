# Database Access and Migration Design

## Status

Draft for review. This document defines the target architecture for SQLite access in gotomux. It does not change application behavior or the user-facing CLI.

## Problem

`internal/store` currently owns the database connection and most SQL, but schema evolution is a manually maintained `schemaVersion` gate around a large idempotent `migrateAll` function. A new column can be added to a query without the version being bumped, leaving existing installations with an old schema. The resulting runtime SQL error looks like a feature failure even though the cause is an upgrade failure. CRUD operations are also handwritten across many files, so query typing and consistent transaction boundaries depend on each call site.

The solution must make schema changes ordered and reviewable, ensure old databases are upgraded before any feature query runs, and make database operations typed and centralized. It must retain SQLite, `modernc.org/sqlite`, the existing `store.Storer` boundary, and compatibility with both standalone CLI and daemon opening the same DB.

## Decision

Use **Goose for versioned schema migrations** and **sqlc for generated typed query methods**.

- Goose is the runtime migration runner. Migration source files live under `internal/store/migrations` and are embedded in the binary. The application does not shell out to the Goose CLI.
- sqlc generates Go methods into an internal package from the same SQL migration schema and query files under `internal/store/queries`.
- Generated types and methods remain private to `internal/store`. Public callers continue to depend on `store.Storer` and domain models.
- Keep `database/sql` and the pure-Go `modernc.org/sqlite` driver. No ORM and no new database engine.
- Keep the existing CLI commands and behavior. No `--migrate`, `--repair`, or user-facing migration controls are introduced.

Goose handles schema history, not CRUD. sqlc addresses the separate problem of handwritten query execution. Using only Goose would leave the query layer inconsistent; using only sqlc would not reliably upgrade existing databases.

## Scope and non-goals

In scope:

- Replace `schemaVersion` / `migrateAll` with ordered, immutable Goose migrations.
- Move application CRUD SQL into sqlc query files and generated typed methods.
- Keep transaction ownership explicit in store/service methods; generated query methods must support a transaction-bound query handle.
- Ensure concurrent CLI and daemon startup cannot race migration bootstrap or execution.
- Adopt existing supported databases without replaying old data operations or transforming old baseline identities.
- Add repository checks so migration edits, generated-code drift, or stale generated output are detected before release.

Out of scope:

- Changing session, shape, baseline, reconcile, ranking, or picker semantics.
- Converting baseline records with empty/old logical identities. Such rows remain as-is; users can create a fresh freeze when needed.
- Removing `migrateLegacyDB`, which copies the database from historical product directories. That is separate from schema migration and must be evaluated independently.
- Replacing SQLite, `database/sql`, or the `store.Storer` interface.
- Moving SQL into other packages. Application database access remains inside `internal/store`.

## Target structure

```text
internal/store/
  migrations/       # numbered Goose SQL migrations, embedded by store
  queries/          # authored sqlc query declarations, grouped by domain
  dbgen/            # generated sqlc code; no product logic
  store.go          # open/configure DB, migrate, construct Store
  *.go              # domain mapping, transactions, Storer implementation
```

`Store` remains the adapter between `store.Storer` and generated methods. It maps `model.Session` and other public domain types to/from generated row types. Query files should be grouped around existing store responsibilities (presets, usage/ranking, shapes, baselines/reconcile, placement/forks, zoxide/root cache), rather than creating a second public repository abstraction.

Static SQL belongs in sqlc query files. The only acceptable direct SQL in handwritten Go is database setup/diagnostics and genuinely dynamic SQL that sqlc cannot represent; each exception must be small, documented, and covered by an explicit test. `PRAGMA` configuration, checkpointing, migration bootstrap/locking, and table introspection are infrastructure exceptions, not CRUD paths.

## Migration history and existing databases

Migration files are append-only after release. Each schema change gets a new monotonically increasing, zero-padded migration version; released files are never edited or renumbered. Schema DDL and any required data transformation must be explicit in the migration that introduces it. Migrations should be transactional by default. A non-transactional migration requires a documented SQLite-specific reason and restart/recovery behavior.

The current database uses `PRAGMA user_version` (currently 9) and has no Goose ledger on installations upgrading from the previous schema system. A naive `goose.Up` would see version zero and replay the initial history against populated data. Therefore migration adoption is a first-class part of the change. The supported in-place range is every released pre-Goose schema generation 1–8; Goose-managed schema 9 is the current version. A populated pre-Goose DB with missing/zero `user_version` is supported only when its complete structural fingerprint identifies exactly one of generations 1–8; ambiguous or unknown state fails closed. A genuinely empty database is a new install.

1. Before any generated query is used, inspect whether the DB is empty, has the old `user_version` format, or already has Goose history.
2. For an empty DB, initialize Goose normally and run the complete migration history.
3. For a known pre-Goose DB, use a one-time adoption path that reads the old version and initializes the Goose version state to the matching point without executing earlier migrations. Then run only migrations newer than that point.
   The SQLite Goose ledger must include the applied version-0 sentinel and a complete, ascending applied history through the adopted version; Goose uses the sentinel to recognize an existing ledger and checks the migration sequence for gaps/order errors.
4. The adoption path must be serialized across processes and transactional where SQLite permits. It must verify that the observed tables, columns, and required indexes are compatible with the claimed old version; a mismatch, unsupported future version, or Goose version zero with pre-existing application tables must stop startup with an actionable error and leave feature data untouched. If Goose is ahead of `user_version` but the full structure exactly matches Goose's recorded version, repair the lagging compatibility marker; this covers a process stopping after a migration commit but before marker synchronization. A `user_version` ahead of Goose, or any structural disagreement, fails closed.
5. Do not replay historical data cleanups (for example old fork resets), regenerate baseline identities, or otherwise reinterpret feature data during adoption. Structural additions required by later migrations are performed only by their corresponding new migrations.
6. After adoption, Goose's migration ledger is authoritative. Keep `PRAGMA user_version` as a compatibility mirror for existing tooling/tests; sync it to Goose after migration and repair only a lagging marker when the Goose version's structural fingerprint matches exactly. Never advance Goose based only on this marker.

The released v1 and v2 databases contain the original core tables; v3 adds shape mirror columns; v4 changed disposable stored content without changing schema; v5 adds `baseline`; v6 adds directed transitions and the markerless-root cache; v7 adds `pane.cmd_path`; v8 adds baseline `logical_id` plus the reconcile journal/indexes; and v9 stores stable session/window/pane identities with instance presets. The v4 cleanup is intentionally not replayed during adoption or fresh migration because it was a data reset rather than a schema requirement. Since v3/v4 have the same structural fingerprint, a populated database with `user_version` zero cannot be assigned to either and must fail closed.

Instance presets persist their stable identities in v9. When opening a v1–v8 preset with no identity columns, the matching baseline may supply those IDs only if the entire persisted instance shape (project path, ordered windows, pane counts, layouts, commands, and paths) matches. A mismatch fails before creating a tmux session. After a successful load, the IDs are written back to the preset atomically with the baseline update. This lets the same saved instance recreate its logical tmux objects without minting a new identity on each attach.

The supported legacy range and structural fingerprints must be derived from the actual released schema history, not guessed from the current schema. Unknown schemas must fail closed rather than be silently stamped as current. Migration tests must cover a fresh DB and on-disk fixtures for every released schema generation 1–8, including the current version 8 and the immediately preceding version.

## Concurrency and startup guarantees

Both daemon and CLI can open the same data directory. Migration startup therefore needs a cross-process lock scoped to the DB path, acquired before legacy-file discovery/copy, adoption, or Goose version checks and held through migration completion. This keeps the existing `migrateLegacyDB` behavior while ensuring it cannot race another opener; it remains a separate file-copy compatibility feature, not a schema/data transformation. The lock is distinct from SQLite's transaction lock: it serializes the decision to copy/adopt/apply and prevents two processes from racing ledger creation. Implement the lock for every OS on which gotomux supports building/running (using OS-specific files behind one internal interface if needed), with bounded waiting. Lock timeout must produce a clear startup error, never continue without migration. Confirm the supported OS matrix from the project's release/build targets during planning; unsupported targets must not silently use a no-op lock.

Open order is: resolve/create data directory; acquire the data-directory migration lock; run legacy-file discovery/copy; open DB and configure connection pragmas; inspect/adopt and apply migrations; release lock; construct `Store`; only then serve store methods. Any failure closes the DB, releases the lock, and returns an error. The daemon's existing reopen-on-Ping-failure path uses this same open path, so it cannot bypass migrations.

## sqlc and query rules

- Configure sqlc for SQLite and `database/sql`, with generated transaction support.
- Query parameters and results use named/generated types where useful; avoid `SELECT *` so schema changes do not silently alter row decoding.
- Multi-step writes remain explicit store transactions. A transaction starts in the store method, binds generated queries to it, and commits only after all steps succeed.
- Preserve existing atomicity for freeze, baseline, reconcile journal, preset save/delete, and shape/mirror metadata operations. The refactor must not split currently atomic writes.
- Preserve established error semantics at the `store.Storer` boundary; wrap errors with operation context without leaking generated package details.
- Dynamic filters or table/column names must not be assembled from untrusted input. Prefer a small number of explicit generated queries over generic SQL builders.
- Keep row-to-domain mapping and business decisions out of generated code.

## Integrity and developer workflow

- Pin Goose and sqlc versions in the module/tool configuration.
- Embed migrations in the binary; runtime operation does not depend on a separate migration directory or CLI installation.
- CI verifies generated sqlc output is clean by regenerating and checking for a diff.
- CI verifies migration immutability/integrity against the committed migration manifest (or equivalent checksum mechanism). Goose's runtime ledger tracks applied versions but is not treated as a content checksum.
- CI checks migration ordering, duplicate versions, and that schema/query compilation succeeds.
- Add a documented developer command for regeneration and validation; normal `make build` must not mutate generated files.

## Rollout and validation

The refactor should be delivered in stages while preserving a buildable repository:

1. Add Goose/sqlc configuration, embedded migration infrastructure, and migration adoption tests while existing CRUD remains in place.
2. Prove fresh DB creation and upgrades of supported legacy DBs, then switch `OpenWithConfig` to the new migration path.
3. Move domain query groups incrementally from handwritten SQL to generated methods, preserving transactions and public behavior.
4. Remove the old migration implementation and handwritten CRUD only after each domain has parity coverage.
5. Add CI integrity/generation checks and verify standalone and daemon use the same open path.

Required validation includes isolated store tests (never the real user DB), schema upgrade fixtures, transaction rollback tests for multi-step writes, generated-code drift checks, and the repository's normal Go test/vet/build checks. Tests that execute the real binary must redirect both `HOME` and XDG paths as documented in `AGENTS.md`.

## Risks and mitigations

- **Existing DB adoption can mis-stamp schema history.** Build a version-to-structure map from repository history and released fixtures; fail closed on mismatch; test supported versions before rollout.
- **Two processes may migrate simultaneously.** Hold a cross-process lock around adoption and migration, in addition to SQLite's own locking.
- **Generated code may obscure domain behavior.** Keep generation at the persistence boundary; retain explicit mapping and transaction orchestration in `internal/store`.
- **sqlc's schema parser must understand the chosen migration SQL.** Keep schema DDL in SQL migrations and run sqlc against that directory. Avoid Go migrations for schema changes unless a concrete SQLite constraint requires them.
- **Migration files can drift after release.** Enforce committed checksums in CI; do not rely on Goose's applied-version table as a checksum.
- **Refactor can accidentally alter write atomicity.** Inventory existing transaction boundaries and preserve them with rollback tests before replacing each path.

## Approval boundary

Approval of this design authorizes creation of a detailed implementation plan. It does not itself authorize data conversion or user-facing CLI changes. The implementation will keep baseline identity conversion out of scope and will fail closed for unsupported database states.
