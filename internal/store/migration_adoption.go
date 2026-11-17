package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/pressly/goose/v3"
)

// prepareMigrationHistory is the bounded compatibility boundary for databases
// created before Goose adoption. Schema evidence is verified before any ledger
// repair. Forward schema evolution is owned exclusively by the Goose provider.
func prepareMigrationHistory(ctx context.Context, db *sql.DB) error {
	version, hasGoose, err := gooseVersion(ctx, db)
	if err != nil {
		return err
	}
	legacyVersion, err := pragmaUserVersion(ctx, db)
	if err != nil {
		return err
	}
	tables, err := applicationTables(ctx, db)
	if err != nil {
		return err
	}

	if hasGoose {
		if version > currentSchemaVersion {
			return fmt.Errorf("database schema version %d is newer than this gotomux build supports (%d)", version, currentSchemaVersion)
		}
		if version == 0 && len(tables) != 0 {
			return errors.New("Goose ledger is at version 0 but application tables already exist; refusing to guess whether migration was interrupted")
		}
		if version > 0 {
			matched, _, matchErr := schemaMatchesVersion(ctx, db, tables, int(version))
			if matchErr != nil {
				return matchErr
			}
			if !matched {
				return fmt.Errorf("database schema does not match Goose migration version %d; refusing to change it", version)
			}
		}
		if legacyVersion > version {
			return fmt.Errorf("database user_version %d is newer than Goose migration version %d", legacyVersion, version)
		}
		if legacyVersion != 0 && legacyVersion < version {
			// Goose commits each migration independently. A process may stop after
			// Goose records the new version but before the compatibility marker is
			// synced below. Repair that safe, schema-verified lag.
			if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
				return fmt.Errorf("repair SQLite user_version: %w", err)
			}
		}
	} else if len(tables) > 0 {
		stampVersion := legacyVersion
		if stampVersion == 0 {
			var exact bool
			stampVersion, exact, err = detectSchemaVersion(ctx, db, tables)
			if err != nil {
				return err
			}
			if !exact {
				return errors.New("populated database has no reliable schema version; refusing automatic migration")
			}
		}
		if stampVersion < 1 || stampVersion > currentSchemaVersion {
			return fmt.Errorf("unsupported legacy database schema version %d", stampVersion)
		}
		matched, _, matchErr := schemaMatchesVersion(ctx, db, tables, int(stampVersion))
		if matchErr != nil {
			return matchErr
		}
		if !matched {
			return fmt.Errorf("database structure does not match legacy schema version %d; refusing automatic migration", stampVersion)
		}
		if err := stampGooseVersion(ctx, db, stampVersion); err != nil {
			return fmt.Errorf("adopt legacy schema version %d: %w", stampVersion, err)
		}
		version = stampVersion
	} else if legacyVersion != 0 {
		return fmt.Errorf("empty database has legacy user_version %d; refusing to infer its schema", legacyVersion)
	}
	// Repair databases from the first Goose adoption attempt: their version
	// table may exist but lack Goose's SQLite version-0 existence sentinel.
	version, hasGoose, err = gooseVersion(ctx, db)
	if err != nil {
		return err
	}
	if hasGoose {
		if err := ensureGooseHistory(ctx, db, version); err != nil {
			return fmt.Errorf("repair Goose version table: %w", err)
		}
	}

	return nil
}

func gooseVersion(ctx context.Context, db *sql.DB) (int64, bool, error) {
	var exists int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM sqlite_master WHERE type='table' AND name='goose_db_version'`).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("inspect Goose version table: %w", err)
	}
	var version sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&version); err != nil {
		return 0, true, fmt.Errorf("read Goose version table: %w", err)
	}
	if !version.Valid {
		return 0, true, errors.New("Goose version table is empty; refusing database adoption")
	}
	return version.Int64, true, nil
}

func pragmaUserVersion(ctx context.Context, db *sql.DB) (int64, error) {
	var version int64
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read SQLite user_version: %w", err)
	}
	return version, nil
}

func applicationTables(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, fmt.Errorf("inspect database tables: %w", err)
	}
	defer rows.Close()
	tables := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if name != goose.DefaultTablename {
			tables[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

func stampGooseVersion(ctx context.Context, db *sql.DB, version int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE goose_db_version (
id INTEGER PRIMARY KEY AUTOINCREMENT,
version_id INTEGER NOT NULL,
is_applied INTEGER NOT NULL,
tstamp TIMESTAMP DEFAULT (datetime('now'))
)`); err != nil {
		return err
	}
	for applied := int64(0); applied <= version; applied++ {
		if _, err := tx.ExecContext(ctx, `INSERT INTO goose_db_version(version_id, is_applied) VALUES (?, 1)`, applied); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		return err
	}
	return tx.Commit()
}

func ensureGooseHistory(ctx context.Context, db *sql.DB, current int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT version_id, is_applied FROM goose_db_version ORDER BY id`)
	if err != nil {
		return err
	}
	seen := make(map[int64]bool, current+1)
	order := make([]int64, 0, current+1)
	for rows.Next() {
		var version int64
		var applied int
		if err := rows.Scan(&version, &applied); err != nil {
			rows.Close()
			return err
		}
		if version < 0 || version > current || applied != 1 || seen[version] {
			rows.Close()
			return fmt.Errorf("invalid Goose history row version=%d applied=%d", version, applied)
		}
		seen[version] = true
		order = append(order, version)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	complete := int64(len(order)) == current+1
	if complete {
		for index, version := range order {
			if version != int64(index) {
				complete = false
				break
			}
		}
	}
	if !complete {
		if _, err := tx.ExecContext(ctx, `DROP TABLE goose_db_version`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `CREATE TABLE goose_db_version (
id INTEGER PRIMARY KEY AUTOINCREMENT,
version_id INTEGER NOT NULL,
is_applied INTEGER NOT NULL,
tstamp TIMESTAMP DEFAULT (datetime('now'))
)`); err != nil {
			return err
		}
		for version := int64(0); version <= current; version++ {
			if _, err := tx.ExecContext(ctx, `INSERT INTO goose_db_version(version_id, is_applied) VALUES (?, 1)`, version); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func schemaMatchesVersion(ctx context.Context, db *sql.DB, tables map[string]bool, version int) (matches bool, exact bool, err error) {
	var detected int64
	detected, exact, err = detectSchemaVersion(ctx, db, tables)
	if err != nil {
		return false, false, err
	}
	if exact {
		return detected == int64(version), true, nil
	}
	if detected != 3 || (version != 3 && version != 4) {
		return false, exact, nil
	}
	return true, exact, nil
}

func detectSchemaVersion(ctx context.Context, db *sql.DB, tables map[string]bool) (int64, bool, error) {
	base := []string{"session", "window", "pane", "usage", "pair", "zox_meta", "zox_item", "shape", "sticky", "placement", "fork"}
	for _, table := range base {
		if !tables[table] {
			return 0, false, nil
		}
	}
	columns := func(table string) (map[string]bool, error) {
		rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		found := make(map[string]bool)
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return nil, err
			}
			found[name] = true
		}
		return found, rows.Err()
	}
	has := func(table, column string) (bool, error) {
		cols, err := columns(table)
		return cols[column], err
	}
	for table, column := range map[string]string{"window": "cwd", "shape": "updated_at"} {
		ok, err := has(table, column)
		if err != nil || !ok {
			return 0, false, err
		}
	}
	version := int64(1)
	if ok, err := has("zox_meta", "sig"); err != nil {
		return 0, false, err
	} else if ok {
		version = 2
	}
	if ok, err := has("shape", "mirror_path"); err != nil {
		return 0, false, err
	} else if ok {
		if sig, err := has("shape", "mirror_sig"); err != nil || !sig {
			return 0, false, err
		}
		version = 3
	}
	if tables["baseline"] {
		version = 5
	}
	if tables["transition"] && tables["root_neg"] {
		version = 6
	}
	if ok, err := has("pane", "cmd_path"); err != nil {
		return 0, false, err
	} else if ok {
		version = 7
	}
	if tables["reconcile_journal"] {
		if ok, err := has("baseline", "logical_id"); err != nil || !ok {
			return 0, false, err
		}
		version = 8
	}
	if version == 8 {
		for table, column := range map[string]string{"session": "logical_id", "window": "slot_id", "pane": "slot_id"} {
			ok, err := has(table, column)
			if err != nil {
				return 0, false, err
			}
			if !ok {
				return version, true, nil
			}
		}
		version = 9
	}
	if version == 9 {
		if ok, err := has("pane", "start_cmd"); err != nil {
			return 0, false, err
		} else if ok {
			version = 10
		}
	}
	expectedTables := map[string]bool{
		"session": true, "window": true, "pane": true, "usage": true,
		"pair": true, "zox_meta": true, "zox_item": true, "shape": true,
		"sticky": true, "placement": true, "fork": true,
	}
	if version >= 5 {
		expectedTables["baseline"] = true
	}
	if version >= 6 {
		expectedTables["transition"] = true
		expectedTables["root_neg"] = true
	}
	if version >= 8 {
		expectedTables["reconcile_journal"] = true
	}
	for table := range tables {
		if !expectedTables[table] {
			return 0, false, nil
		}
	}
	if len(tables) != len(expectedTables) {
		return 0, false, nil
	}
	expectedColumns := map[string][]string{
		"session":   {"name", "cwd", "created_at", "last_used"},
		"window":    {"id", "session", "idx", "name", "cwd", "layout"},
		"pane":      {"id", "window_id", "idx", "cwd", "cmd"},
		"usage":     {"name", "opens", "kills", "last_open", "last_kill"},
		"pair":      {"a", "b", "n", "last"},
		"zox_meta":  {"id", "updated"},
		"zox_item":  {"ord", "name", "path", "title", "desc", "recency"},
		"shape":     {"id", "key", "body", "created_at", "updated_at"},
		"sticky":    {"id", "shape_id"},
		"placement": {"shape_id", "pattern", "n", "last"},
		"fork":      {"key", "body", "n", "last"},
	}
	if version >= 2 {
		expectedColumns["zox_meta"] = append(expectedColumns["zox_meta"], "sig")
	}
	if version >= 3 {
		expectedColumns["shape"] = append(expectedColumns["shape"], "mirror_path", "mirror_sig")
	}
	if version >= 5 {
		expectedColumns["baseline"] = []string{"name", "body", "updated_at"}
	}
	if version >= 6 {
		expectedColumns["transition"] = []string{"prev", "next", "n", "last"}
		expectedColumns["root_neg"] = []string{"path", "checked"}
	}
	if version >= 7 {
		expectedColumns["pane"] = append(expectedColumns["pane"], "cmd_path")
	}
	if version >= 8 {
		expectedColumns["baseline"] = append(expectedColumns["baseline"], "logical_id")
		expectedColumns["reconcile_journal"] = []string{"id", "session_key", "session_name", "session_logical_id", "policy", "status", "plan_json", "completed_step", "last_error", "created_at", "updated_at"}
	}
	if version >= 9 {
		expectedColumns["session"] = append(expectedColumns["session"], "logical_id")
		expectedColumns["window"] = append(expectedColumns["window"], "slot_id")
		expectedColumns["pane"] = append(expectedColumns["pane"], "slot_id")
	}
	if version >= 10 {
		expectedColumns["pane"] = append(expectedColumns["pane"], "start_cmd")
	}
	for table, expected := range expectedColumns {
		found, err := columns(table)
		if err != nil {
			return 0, false, err
		}
		if len(found) != len(expected) {
			return 0, false, nil
		}
		for _, column := range expected {
			if !found[column] {
				return 0, false, nil
			}
		}
	}
	expectedIndexes := []string{"idx_session_last_used"}
	if version >= 8 {
		expectedIndexes = append(expectedIndexes, "idx_baseline_logical_id", "idx_reconcile_active_session")
	}
	if version >= 9 {
		expectedIndexes = append(expectedIndexes, "idx_preset_session_logical_id", "idx_preset_window_slot_id", "idx_preset_pane_slot_id")
	}
	for _, index := range expectedIndexes {
		var found int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&found); err != nil {
			return 0, false, err
		}
		if found != 1 {
			return 0, false, nil
		}
	}
	// Schema v4 changed only disposable learned/mirror contents; v3 and v4
	// have the same relational fingerprint and cannot be distinguished safely.
	exact := version != 3
	return version, exact, nil
}
