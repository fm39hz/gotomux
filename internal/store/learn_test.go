package store

import (
	"testing"
	"time"

	"github.com/fm39hz/gotomux/internal/config"
)

// isolatedStore opens a store in a temp dir. Unlike the older tests in this
// package, it never touches the developer's real state.db.
func isolatedStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenWithConfig(&config.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("OpenWithConfig: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// agePlacements pushes every placement row's last timestamp well past the
// default prune cutoff (720h).
func agePlacements(t *testing.T, s *Store) {
	t.Helper()
	old := time.Now().Add(-800 * time.Hour).Unix()
	if _, err := s.db.Exec(`UPDATE placement SET last = ?`, old); err != nil {
		t.Fatalf("age placements: %v", err)
	}
}

func TestPruneKeepsWhatBestPlacementReads(t *testing.T) {
	s := isolatedStore(t)

	// A single deliberate freeze: one observation, nothing competing with it.
	if err := s.RecordPlacement("shape-solo", "R,C0"); err != nil {
		t.Fatalf("RecordPlacement: %v", err)
	}
	if got, ok := s.BestPlacement("shape-solo"); !ok || got != "R,C0" {
		t.Fatalf("BestPlacement before prune = %q,%v; want R,C0,true", got, ok)
	}

	agePlacements(t, s)
	s.Prune()

	// Retention must not expire behaviour that BestPlacement still applies.
	// The old rule deleted every n<2 row past the cutoff, so a bake silently
	// changed 30 days after the freeze with nothing to explain it.
	got, ok := s.BestPlacement("shape-solo")
	if !ok || got != "R,C0" {
		t.Errorf("BestPlacement after prune = %q,%v; want R,C0,true (row was still the best observation)", got, ok)
	}
}

func TestPruneDropsSupersededPlacements(t *testing.T) {
	s := isolatedStore(t)

	// One weak pattern, then a stronger competing one for the same shape.
	if err := s.RecordPlacement("shape-multi", "R,R"); err != nil {
		t.Fatalf("RecordPlacement: %v", err)
	}
	for range 2 {
		if err := s.RecordPlacement("shape-multi", "R,C0"); err != nil {
			t.Fatalf("RecordPlacement: %v", err)
		}
	}
	if got, ok := s.BestPlacement("shape-multi"); !ok || got != "R,C0" {
		t.Fatalf("BestPlacement = %q,%v; want R,C0,true", got, ok)
	}

	agePlacements(t, s)
	s.Prune()

	// The superseded row is garbage and should be collected.
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM placement WHERE shape_id = ? AND pattern = ?`,
		"shape-multi", "R,R",
	).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("superseded placement survived prune (%d rows)", n)
	}
	if got, ok := s.BestPlacement("shape-multi"); !ok || got != "R,C0" {
		t.Errorf("BestPlacement after prune = %q,%v; want R,C0,true", got, ok)
	}
}

func TestRecordOpenAccumulates(t *testing.T) {
	s := isolatedStore(t)

	// Guards the telemetry path that had no production caller at all: opens were
	// never recorded, so the recency tier of the ranking never changed.
	for range 3 {
		if err := s.RecordOpen("zz-usage"); err != nil {
			t.Fatalf("RecordOpen: %v", err)
		}
	}
	us, err := s.AllUsage()
	if err != nil {
		t.Fatalf("AllUsage: %v", err)
	}
	u, ok := us["zz-usage"]
	if !ok {
		t.Fatal("no usage row after RecordOpen")
	}
	if u.Opens != 3 {
		t.Errorf("Opens = %d, want 3", u.Opens)
	}
	if u.LastOpen == 0 {
		t.Error("LastOpen = 0; frecency decays by age, so it needs a timestamp")
	}
}

func TestRecordPairsWithLive(t *testing.T) {
	s := isolatedStore(t)

	s.RecordPairsWithLive("zz-a", []string{"zz-b", "zz-c"})
	scores, err := s.PairScores("zz-a", time.Now().Unix())
	if err != nil {
		t.Fatalf("PairScores: %v", err)
	}
	for _, want := range []string{"zz-b", "zz-c"} {
		if scores[want] <= 0 {
			t.Errorf("pair score for %q = %d, want > 0", want, scores[want])
		}
	}
	if _, self := scores["zz-a"]; self {
		t.Error("session paired with itself")
	}
}

func TestRecordTransitionDirected(t *testing.T) {
	s := isolatedStore(t)

	if err := s.RecordTransition("zz-a", "zz-b"); err != nil {
		t.Fatalf("RecordTransition: %v", err)
	}
	if err := s.RecordTransition("zz-a", "zz-b"); err != nil {
		t.Fatalf("RecordTransition: %v", err)
	}
	if err := s.RecordTransition("zz-b", "zz-a"); err != nil {
		t.Fatalf("RecordTransition: %v", err)
	}
	// Empty and self transitions are no-ops, not errors.
	if err := s.RecordTransition("", "zz-b"); err != nil {
		t.Fatalf("RecordTransition empty: %v", err)
	}
	if err := s.RecordTransition("zz-a", "zz-a"); err != nil {
		t.Fatalf("RecordTransition self: %v", err)
	}

	now := time.Now().Unix()
	fromA, err := s.TransitionScores("zz-a", now)
	if err != nil {
		t.Fatalf("TransitionScores: %v", err)
	}
	if got := fromA["zz-b"]; got != 2000 { // n=2, age 0 -> 2*1000/(1+0)
		t.Errorf("zz-a->zz-b score = %d, want 2000", got)
	}
	if _, back := fromA["zz-a"]; back {
		t.Error("transition must not pair a session with itself")
	}
	// Direction matters: zz-b has no outgoing row to itself; only zz-b->zz-a.
	fromB, err := s.TransitionScores("zz-b", now)
	if err != nil {
		t.Fatalf("TransitionScores: %v", err)
	}
	if got := fromB["zz-a"]; got != 1000 {
		t.Errorf("zz-b->zz-a score = %d, want 1000", got)
	}
	if _, fwd := fromB["zz-b"]; fwd {
		t.Error("zz-b->zz-b must not exist")
	}
	// Unknown context gets nothing.
	none, err := s.TransitionScores("zz-nope", now)
	if err != nil {
		t.Fatalf("TransitionScores: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("unknown ctx scores = %+v, want empty", none)
	}
}

func TestTransitionScoresDecay(t *testing.T) {
	s := isolatedStore(t)

	if err := s.RecordTransition("zz-a", "zz-b"); err != nil {
		t.Fatalf("RecordTransition: %v", err)
	}
	old := time.Now().Add(-7 * 24 * time.Hour).Unix()
	if _, err := s.db.Exec(`UPDATE transition SET last = ?`, old); err != nil {
		t.Fatalf("age transition: %v", err)
	}
	stale, err := s.TransitionScores("zz-a", time.Now().Unix())
	if err != nil {
		t.Fatalf("TransitionScores: %v", err)
	}
	fresh, err := s.TransitionScores("zz-a", old)
	if err != nil {
		t.Fatalf("TransitionScores: %v", err)
	}
	if fresh["zz-b"] <= stale["zz-b"] {
		t.Errorf("decay failed: fresh=%d stale=%d", fresh["zz-b"], stale["zz-b"])
	}
}

func TestRebindNameRewritesTransitions(t *testing.T) {
	s := isolatedStore(t)

	if err := s.RecordTransition("old-sess", "other"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTransition("other", "old-sess"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTransition("old-sess", "new-sess"); err != nil {
		t.Fatal(err)
	}

	if err := s.RebindName("old-sess", "new-sess"); err != nil {
		t.Fatal(err)
	}

	fromNew, err := s.TransitionScores("new-sess", 0)
	if err != nil {
		t.Fatal(err)
	}
	if fromNew["other"] <= 0 {
		t.Errorf("old->other should follow rename as new->other: %+v", fromNew)
	}
	if _, self := fromNew["new-sess"]; self {
		t.Errorf("self-transition after rename should be dropped: %+v", fromNew)
	}
	fromOther, err := s.TransitionScores("other", 0)
	if err != nil {
		t.Fatal(err)
	}
	if fromOther["new-sess"] <= 0 {
		t.Errorf("other->old should follow rename as other->new: %+v", fromOther)
	}
	// old endpoint gone
	fromOld, _ := s.TransitionScores("old-sess", 0)
	if len(fromOld) != 0 {
		t.Errorf("old transition scores should be empty: %+v", fromOld)
	}
}
