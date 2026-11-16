package store

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gofrs/flock"
)

const migrationLockTimeout = 10 * time.Second

func acquireMigrationLock(dir string) (func() error, error) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" && runtime.GOOS != "freebsd" && runtime.GOOS != "openbsd" && runtime.GOOS != "netbsd" {
		return nil, fmt.Errorf("database migration locking is unsupported on %s", runtime.GOOS)
	}
	lock := flock.New(filepath.Join(dir, ".migration.lock"), flock.SetPermissions(0o600))
	ctx, cancel := context.WithTimeout(context.Background(), migrationLockTimeout)
	defer cancel()
	locked, err := lock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("acquire database migration lock: %w", err)
	}
	if !locked {
		return nil, fmt.Errorf("timed out waiting %s for database migration lock in %s", migrationLockTimeout, dir)
	}
	return lock.Unlock, nil
}
