package store

import (
	"log"
	"strings"
	"time"
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
	_, err := s.db.Exec(`
INSERT INTO usage(name, opens, kills, last_open, last_kill) VALUES(?, 1, 0, ?, 0)
ON CONFLICT(name) DO UPDATE SET
  opens = opens + 1,
  last_open = excluded.last_open
`, name, now)
	return err
}

// RecordKill increments kill count (ctrl-x / explicit kill).
func (s *Store) RecordKill(name string) error {
	if name == "" {
		return nil
	}
	now := time.Now().Unix()
	_, err := s.db.Exec(`
INSERT INTO usage(name, opens, kills, last_open, last_kill) VALUES(?, 0, 1, 0, ?)
ON CONFLICT(name) DO UPDATE SET
  kills = kills + 1,
  last_kill = excluded.last_kill
`, name, now)
	return err
}

// AllUsage returns name -> usage for ranking merge.
func (s *Store) AllUsage() (map[string]Usage, error) {
	rows, err := s.db.Query(`SELECT name, opens, kills, last_open, last_kill FROM usage`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Usage{}
	for rows.Next() {
		var u Usage
		if err := rows.Scan(&u.Name, &u.Opens, &u.Kills, &u.LastOpen, &u.LastKill); err != nil {
			return nil, err
		}
		out[u.Name] = u
	}
	return out, rows.Err()
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
	_, err := s.db.Exec(`
INSERT INTO pair(a, b, n, last) VALUES(?, ?, 1, ?)
ON CONFLICT(a, b) DO UPDATE SET
  n = n + 1,
  last = excluded.last
`, a, b, now)
	return err
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
	rows, err := s.db.Query(`
SELECT a, b, n, last FROM pair WHERE a = ? OR b = ?
`, ctx, ctx)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var a, b string
		var n, last int64
		if err := rows.Scan(&a, &b, &n, &last); err != nil {
			return nil, err
		}
		other := b
		if a != ctx {
			other = a
		}
		age := int64(0)
		if last > 0 && now >= last {
			age = (now - last) / 86400
		}
		if n > 10_000 {
			n = 10_000
		}
		out[other] = n * 1000 / (1 + age)
	}
	return out, rows.Err()
}
