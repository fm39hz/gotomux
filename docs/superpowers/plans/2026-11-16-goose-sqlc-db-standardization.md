# Goose and sqlc Database Standardization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace gotomux's ad hoc schema gate and handwritten CRUD with versioned Goose migrations and sqlc-generated typed persistence while preserving existing data and store behavior.

**Architecture:** Goose SQL migrations are embedded and run through the existing `database/sql` connection before `Store` is exposed. Existing databases are identified and stamped under a cross-process lock, then upgraded only from their supported schema version. sqlc generates internal query methods; `internal/store` continues to own domain mapping, transactions, and the `Storer` API.

**Tech Stack:** Go 1.26.5, `modernc.org/sqlite`, Goose v3.30.0, sqlc v1.31.1, `database/sql`, SQLite.

**Spec:** `docs/specs/database-access-v1.md`

## Global Constraints

- Preserve SQLite, `modernc.org/sqlite`, the existing `store.Storer` boundary, and standalone/daemon compatibility.
- Support in-place adoption of every released schema generation 1–8; missing/zero version on a populated DB is accepted only when an exact structural fingerprint identifies one generation.
- Never replay historical data cleanups or convert baseline identities during adoption.
- Keep SQL and generated types private to `internal/store`.
- Hold a cross-process lock over legacy DB discovery/copy, adoption, and migration; unsupported platforms must not use a no-op lock.
- Keep all existing multi-step write atomicity.
- Do not read or write the developer's real database in implementation tooling.

## Review Focus

- A partially created or inconsistent Goose ledger must stop startup without changing feature data — adoption validation task.
- Two CLI/daemon processes opening a fresh or old DB concurrently must serialize and complete once — lock task.
- A populated DB with absent/zero `user_version` must be matched exactly or fail closed — adoption task.
- SQL NULL values must map identically through generated nullable types — CRUD conversion task.
- A transaction failure halfway through freeze/reconcile/name-rebind must roll back every write — transaction conversion task.

---

### Task 1: Goose schema history and safe startup adoption

**Files:**
- Create: `internal/store/migrations/*.sql`
- Create: `internal/store/migrate.go`
- Create: `internal/store/migration_lock_unix.go` and platform-specific lock files as required by supported build targets
- Create: `internal/store/migration_integrity.go` and committed migration manifest/check script
- Modify: `internal/store/store.go`
- Modify: `go.mod`, `go.sum`, `.github/workflows/*`
- Test: `internal/store/migrate_test.go`, fixtures under `internal/store/testdata/migrations/`

**Interfaces:**
- Produces `migrateDB(ctx context.Context, db *sql.DB, dbPath string) error`, invoked by every store open/reopen path before returning `Store`.
- Produces one internal `migrationLock` acquire/release abstraction with bounded acquisition and context cancellation.

- [ ] Reconstruct released schema transitions 1–8 from git history; classify every DDL statement and historical data operation; record exact structural fingerprints for versions 1–8. Keep all SQL in append-only zero-padded Goose migration files.
- [ ] Add fixtures for released schema versions 1–8, current v8, and a genuine empty DB. Fixture assertions preserve preset, shape, usage, baseline body/identity, fork, and reconcile rows.
- [ ] Implement failing migration tests for empty creation, v1–v8 adoption/upgrade, exact fingerprint mapping when `user_version` is zero, unknown schema rejection without data changes, partially initialized ledger rejection, and concurrent open serialization.
- [ ] Implement embedded Goose provider using the existing `*sql.DB`; acquire the DB-path lock before legacy discovery/copy, configure connection pragmas, inspect schema markers, bootstrap a Goose version row for known pre-Goose DBs, apply pending migrations, and release/close resources on every path.
- [ ] Move `migrateLegacyDB` under the startup lock without changing its source/destination selection semantics.
- [ ] Remove `schemaVersion`/`migrateAll`; Goose becomes the only schema migration path. Never synthesize legacy feature data or replay old cleanup statements when stamping an existing DB.
- [ ] Add migration immutability checksum verification and CI checks for migration ordering, checksum drift, and embedded files.
- [ ] Run focused migration tests and the repository's standard Go checks; inspect the resulting DB schema and data with isolated fixtures only.

### Task 2: sqlc generation foundation and query inventory conversion

**Files:**
- Create: `sqlc.yaml`
- Create: `internal/store/queries/*.sql`
- Create: `internal/store/dbgen/*.go`
- Modify: `go.mod`, `go.sum`, `Makefile`
- Modify: `internal/store/store.go`

**Interfaces:**
- Produces `dbgen.New(*sql.DB)` and transaction-bound generated query objects compatible with `*sql.Tx`.
- `Store` owns `*dbgen.Queries`; public interfaces remain unchanged.

- [ ] Add pinned sqlc configuration: SQLite, `database/sql`, named generated package `dbgen`, schema input from Goose migration directory, query input from `internal/store/queries`.
- [ ] Inventory every static SQL operation in `internal/store` and map it to a named query, preserving column lists and ordering; keep only pragmas, migration infrastructure, and documented dynamic/infrastructure SQL in handwritten Go.
- [ ] Add generation command that regenerates code without changing build behavior and CI drift check that fails when generated output is stale.
- [ ] Generate initial methods and refactor Store construction to initialize generated queries.
- [ ] Compile generated package and verify query parsing against all migrations.

### Task 3: Convert store domains to generated queries without behavior changes

**Files:**
- Modify: `internal/store/preset.go`, `rank.go`, `shape.go`, `zox.go`, `learn.go`, `baseline.go`, `reconcile_ops.go`, `root_neg.go`
- Modify: `internal/store/queries/*.sql`, `internal/store/dbgen/*.go`

**Interfaces:**
- Consumes Task 2's `dbgen.Queries` and transaction-bound queries.
- `store.Storer` signatures and public domain types remain unchanged.

- [ ] Convert read-only preset, ranking, zoxide, shape, baseline, reconcile, placement/fork, and root-negative-cache operations to generated methods.
- [ ] Convert mutations and upserts to generated methods; retain operation-specific row-to-model mapping in store code.
- [ ] For every existing explicit `Begin` transaction, bind generated queries to the same transaction and preserve commit/rollback boundaries, including name rebinding and freeze/reconcile commits.
- [ ] Remove handwritten CRUD SQL from domain files; document any remaining exception at the call site.
- [ ] Run focused store checks and static SQL inventory to confirm only approved exceptions remain.

### Task 4: Final integration and release guardrails

**Files:**
- Modify: `internal/store/*`, `Makefile`, `.github/workflows/*`, `docs/specs/database-access-v1.md`

**Interfaces:**
- Both standalone and daemon continue to use `store.OpenWithConfig` and the same migration path.

- [ ] Verify DB reopen after daemon `Ping` failure uses the same lock/adoption/migration path.
- [ ] Verify startup failures close connections and release migration locks; lock timeout is actionable and does not bypass migration.
- [ ] Verify generated SQL and embedded migration checks are wired to CI and documented developer commands.
- [ ] Run repository-wide Go checks and a clean build; do not use the live user DB or live tmux server.
- [ ] Review the final diff for scope: no reconcile/product behavior changes, no CLI migration controls, no baseline identity conversion, and no edits to existing user changes outside DB work.
