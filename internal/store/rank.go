package store

import (
	"log"
	"strings"
	"time"

	"github.com/fm39hz/gotomux/internal/store/dbgen"
)

// Usage is per-session connect/kill stats for frecency ranking.
type Usage struct {
	Name     string `json:"name"`
	Opens    int64  `json:"opens,omitempty"`
	Kills    int64  `json:"kills,omitempty"`
	LastOpen int64  `json:"last_open,omitempty"`
	LastKill int64  `json:"last_kill,omitempty"`
}

// RecordOpen increments open count (connect / create / attach).
func (s *Store) RecordOpen(name string) error {
	if name == "" {
		return nil
	}
	now := time.Now().Unix()
	return s.q.RecordOpen(storeContext, dbgen.RecordOpenParams{Name: name, LastOpen: now})
}

// RecordKill increments kill count (ctrl-x / explicit kill).
func (s *Store) RecordKill(name string) error {
	if name == "" {
		return nil
	}
	now := time.Now().Unix()
	return s.q.RecordKill(storeContext, dbgen.RecordKillParams{Name: name, LastKill: now})
}

// AllUsage returns name -> usage for ranking merge.
func (s *Store) AllUsage() (map[string]Usage, error) {
	rows, err := s.q.AllUsage(storeContext)
	if err != nil {
		return nil, err
	}
	out := map[string]Usage{}
	for _, row := range rows {
		out[row.Name] = Usage{Name: row.Name, Opens: row.Opens, Kills: row.Kills, LastOpen: row.LastOpen, LastKill: row.LastKill}
	}
	return out, nil
}

// RecordPair bumps undirected co-occurrence (a,b) with day-decay friendly counters.
func (s *Store) RecordPair(a, b string) error {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" || a == b {
		return nil
	}
	// canonical order so (x,y) == (y,x)
	if a > b {
		a, b = b, a
	}
	now := time.Now().Unix()
	return s.q.RecordPair(storeContext, dbgen.RecordPairParams{A: a, B: b, Last: now})
}

// RecordPairsWithLive records pairs between name and every other live session.
func (s *Store) RecordPairsWithLive(name string, live []string) {
	for _, other := range live {
		if err := s.RecordPair(name, other); err != nil {
			log.Printf("record pair: %v", err)
		}
	}
}

// PairScores returns map[otherName]decayedScore given context session.
// Score = n*1000/(1+ageDays); 0 if no rows.
func (s *Store) PairScores(ctx string, now int64) (map[string]int64, error) {
	ctx = strings.TrimSpace(ctx)
	if ctx == "" {
		return nil, nil
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	rows, err := s.q.PairRowsForSession(storeContext, dbgen.PairRowsForSessionParams{A: ctx, B: ctx})
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, row := range rows {
		other := row.B
		if row.A != ctx {
			other = row.A
		}
		age := int64(0)
		if row.Last > 0 && now >= row.Last {
			age = (now - row.Last) / 86400
		}
		n := row.N
		if n > 10_000 {
			n = 10_000
		}
		out[other] = n * 1000 / (1 + age)
	}
	return out, nil
}

// RecordTransition bumps directed prev->next frequency. Unlike pair (undirected
// co-presence), transition learns ordered switches: "from prev I usually go to
// next". Learned from actual attach diffs, never from picker intent.
func (s *Store) RecordTransition(from, to string) error {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" || to == "" || from == to {
		return nil
	}
	now := time.Now().Unix()
	return s.q.RecordTransition(storeContext, dbgen.RecordTransitionParams{Prev: from, Next: to, Last: now})
}

// TransitionScores returns map[nextName]decayedScore for switches INTO next
// from the given prev session. Directed: only rows with prev = ctx count.
// Score = n*1000/(1+ageDays); 0 if no rows.
func (s *Store) TransitionScores(ctx string, now int64) (map[string]int64, error) {
	ctx = strings.TrimSpace(ctx)
	if ctx == "" {
		return nil, nil
	}
	if now <= 0 {
		now = time.Now().Unix()
	}
	rows, err := s.q.TransitionRowsFrom(storeContext, ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, row := range rows {
		age := int64(0)
		if row.Last > 0 && now >= row.Last {
			age = (now - row.Last) / 86400
		}
		next, n := row.Next, row.N
		if n > 10_000 {
			n = 10_000
		}
		out[next] = n * 1000 / (1 + age)
	}
	return out, nil
}
