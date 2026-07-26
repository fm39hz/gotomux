package store

import (
	"os"
	"testing"
	"time"

	"github.com/fm39hz/gotomux/internal/config"
)

// TestQueryCost times the store reads that sit on the picker's cold path, against
// the developer's real DB. Env-gated: it depends on local state.
func TestQueryCost(t *testing.T) {
	if os.Getenv("QUERY_COST") != "1" {
		t.Skip("QUERY_COST=1")
	}
	t0 := time.Now()
	s, err := OpenWithConfig(config.Load())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t.Logf("  OpenWithConfig    %v", time.Since(t0))

	t1 := time.Now()
	us, _ := s.AllUsage()
	t.Logf("  AllUsage (%d)      %v", len(us), time.Since(t1))

	t2 := time.Now()
	ps, _ := s.PairScores("x", time.Now().Unix())
	t.Logf("  PairScores (%d)     %v", len(ps), time.Since(t2))

	t3 := time.Now()
	pm, _ := s.ListMeta()
	t.Logf("  ListMeta (%d)       %v", len(pm), time.Since(t3))

	t4 := time.Now()
	rows, _, _, _ := s.LoadZox()
	t.Logf("  LoadZox (%d)      %v", len(rows), time.Since(t4))

	t5 := time.Now()
	_, _ = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	t.Logf("  wal_checkpoint    %v", time.Since(t5))

	t6 := time.Now()
	us2, _ := s.AllUsage()
	t.Logf("  AllUsage after ckpt (%d)  %v", len(us2), time.Since(t6))
}
