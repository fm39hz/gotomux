package store

import (
	"time"

	"github.com/fm39hz/gotomux/internal/store/dbgen"
)

// RootNegTTL bounds how long a recorded "no project root here" answer is trusted.
//
// Ten minutes is deliberately short: a directory can gain a marker at any moment,
// so absence is only ever a recent observation, never a fact. The TTL exists to
// collapse the burst of identical walks within one working session (picker opens
// in quick succession, daemon re-derives on every zoxide list change), not to be
// a long-term truth store — anything longer just widens the window where a fresh
// go.mod is ignored. It lives here, next to the enforcement in LoadRootNeg, so
// no caller can hold a divergent copy of the number.
const RootNegTTL = 10 * time.Minute

// LoadRootNeg returns when path was last proven markerless. ok is false both
// when nothing was recorded and when the record is older than RootNegTTL — the
// caller treats both as "must walk".
func (s *Store) LoadRootNeg(path string) (checked int64, ok bool) {
	ts, err := s.q.RootNegChecked(storeContext, path)
	if err != nil {
		return 0, false
	}
	if time.Since(time.Unix(ts, 0)) > RootNegTTL {
		return 0, false
	}
	return ts, true
}

// SaveRootNeg records (or refreshes) the "walked, no root" verdict for path.
// The upsert makes repeated derives slide the timestamp forward, so an actively
// visited markerless directory keeps its exemption without growing the table.
func (s *Store) SaveRootNeg(path string) error {
	return s.q.UpsertRootNeg(storeContext, dbgen.UpsertRootNegParams{Path: path, Checked: time.Now().Unix()})
}

// ForgetRootNeg drops any recorded verdict for path. Not needed for correctness
// — an expired row is indistinguishable from an absent one — but lets callers
// proactively invalidate when they observe the world changed.
func (s *Store) ForgetRootNeg(path string) error {
	return s.q.DeleteRootNeg(storeContext, path)
}
