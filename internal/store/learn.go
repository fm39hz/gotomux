package store

import (
	"strings"
	"time"
)

// Prune drops learned rows that can no longer affect a bake.
//
// Retention must not expire what BestPlacement would still read. It used to
// delete every placement row with n < 2 past the cutoff, while BestPlacement
// applies no threshold at all — so a single deliberate freeze governed bakes for
// 30 days and then silently stopped, with nothing in the UI to explain it. The
// EXISTS clause keeps a low-count row when it is still the best observation for
// its shape, and collects only rows some other pattern has already superseded.
//
// Best-effort and silent: callers treat it as housekeeping, not an operation.
func (s *Store) Prune() {
	dur := 30 * 24 * time.Hour
	if s.cfg != nil {
		dur = s.cfg.PruneCutoff
	}
	cutoff := time.Now().Add(-dur).Unix()
	_, _ = s.db.Exec(`
DELETE FROM placement
WHERE n < 2 AND last < ?
  AND EXISTS (
    SELECT 1 FROM placement p2
    WHERE p2.shape_id = placement.shape_id AND p2.n > placement.n
  )`, cutoff)
	// fork rows are currently write-only (nothing reads fork.body, and ForkHits
	// has no production caller), so the plain age rule is harmless here.
	_, _ = s.db.Exec(`DELETE FROM fork WHERE n < 2 AND last < ?`, cutoff)
}

func (s *Store) RecordPlacement(shapeID, pattern string) error {
	shapeID, pattern = strings.TrimSpace(shapeID), strings.TrimSpace(pattern)
	if shapeID == "" || pattern == "" {
		return nil
	}
	now := time.Now().Unix()
	_, err := s.db.Exec(`
INSERT INTO placement(shape_id, pattern, n, last) VALUES(?, ?, 1, ?)
ON CONFLICT(shape_id, pattern) DO UPDATE SET
  n = n + 1,
  last = excluded.last
`, shapeID, pattern, now)
	return err
}

// BestPlacement returns the highest-count pattern for a shape (tie -> newest
// last), or ok=false when the shape has no observation.
//
// There is deliberately no confidence threshold: a freeze is an explicit user
// action, so one observation is a real signal. Prune is written to match — it
// never removes a row that this query would still return.
func (s *Store) BestPlacement(shapeID string) (pattern string, ok bool) {
	shapeID = strings.TrimSpace(shapeID)
	if shapeID == "" {
		return "", false
	}
	err := s.db.QueryRow(`
SELECT pattern FROM placement
WHERE shape_id = ?
ORDER BY n DESC, last DESC
LIMIT 1
`, shapeID).Scan(&pattern)
	if err != nil || pattern == "" {
		return "", false
	}
	return pattern, true
}

// RecordFork bumps a window-level essence unit (silent fork learning).
// key fingerprints topology+tools of one window; body is product JSON for that window.
func (s *Store) RecordFork(key, body string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	now := time.Now().Unix()
	_, err := s.db.Exec(`
INSERT INTO fork(key, body, n, last) VALUES(?, ?, 1, ?)
ON CONFLICT(key) DO UPDATE SET
  n = n + 1,
  last = excluded.last,
  body = CASE WHEN excluded.body != '' THEN excluded.body ELSE body END
`, key, body, now)
	return err
}

// ForkHits returns how often a window key has been observed (0 if unknown).
func (s *Store) ForkHits(key string) int64 {
	key = strings.TrimSpace(key)
	if key == "" {
		return 0
	}
	var n int64
	_ = s.db.QueryRow(`SELECT n FROM fork WHERE key = ?`, key).Scan(&n)
	return n
}
