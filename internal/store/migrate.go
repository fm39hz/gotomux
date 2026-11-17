package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"runtime"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const currentSchemaVersion = 10

func migrateDB(ctx context.Context, db *sql.DB) error {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" && runtime.GOOS != "freebsd" && runtime.GOOS != "openbsd" && runtime.GOOS != "netbsd" {
		return fmt.Errorf("database migration locking is unsupported on %s", runtime.GOOS)
	}

	if err := prepareMigrationHistory(ctx, db); err != nil {
		return err
	}

	migrationFS, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrationFS,
		goose.WithDisableGlobalRegistry(true),
		goose.WithLogger(goose.NopLogger()),
	)
	if err != nil {
		return fmt.Errorf("configure Goose: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply Goose migrations: %w", err)
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("read Goose schema version: %w", err)
	}
	if version != currentSchemaVersion {
		return fmt.Errorf("Goose schema version is %d, want %d", version, currentSchemaVersion)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return fmt.Errorf("sync SQLite user_version: %w", err)
	}
	return nil
}
