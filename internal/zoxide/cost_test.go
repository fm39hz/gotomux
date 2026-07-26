package zoxide

import (
	"os"
	"testing"
	"time"
)

// TestCostBreakdown attributes the cold zoxide rebuild: the `zoxide query -l`
// fork versus deriving rows (project.Session -> FindProjectRoot stat-walk per
// path). Run with -v; skipped in normal runs.
func TestCostBreakdown(t *testing.T) {
	// Env-gated like the other tests that depend on the developer's real state:
	// this reads the actual zoxide DB and walks the real filesystem.
	if os.Getenv("ZOX_COST") != "1" {
		t.Skip("ZOX_COST=1")
	}
	t0 := time.Now()
	paths := Query()
	dQuery := time.Since(t0)
	if len(paths) == 0 {
		t.Skip("zoxide not available or empty")
	}

	// Cold-ish: FindProjectRoot memoizes per process, so this first pass pays the
	// real derivation cost.
	t1 := time.Now()
	rows := Rows(paths)
	dDerive := time.Since(t1)

	t2 := time.Now()
	_ = Rows(paths)
	dDeriveMemo := time.Since(t2)

	t.Logf("paths=%d rows=%d", len(paths), len(rows))
	t.Logf("  zoxide query -l   %v", dQuery)
	t.Logf("  Rows (cold memo)  %v", dDerive)
	t.Logf("  Rows (warm memo)  %v", dDeriveMemo)
}
