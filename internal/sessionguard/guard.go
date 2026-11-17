// Package sessionguard serializes gotomux mutations of one tmux incarnation.
package sessionguard

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

// Acquire returns a process-scoped file lock. The OS releases it on process
// exit; the file is deliberately not unlinked, avoiding split-inode locks.
// The key scopes runtime IDs to their server generation.
func Acquire(serverKey, sessionID string) (func() error, error) {
	if serverKey == "" || sessionID == "" {
		return nil, fmt.Errorf("session operation requires a server generation and runtime ID")
	}
	key := sha256.Sum256([]byte(serverKey + "\x00" + sessionID))
	path := filepath.Join(os.TempDir(), fmt.Sprintf("gotomux-session-%d-%x.lock", os.Getuid(), key[:16]))
	lock := flock.New(path, flock.SetPermissions(0o600))
	acquired, err := lock.TryLock()
	if err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("lock session operation: %w", err)
	}
	if !acquired {
		_ = lock.Close()
		return nil, fmt.Errorf("another gotomux operation is active for session %s; retry when it finishes", sessionID)
	}
	return lock.Close, nil
}
