package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Preset struct {
	Name    string
	Cwd     string // session root
	Windows []PresetWindow
}

type PresetWindow struct {
	Idx    int
	Name   string
	Cwd    string // window working dir (usually first/active pane)
	Layout string
	Panes  []PresetPane
}

type PresetPane struct {
	Idx int
	Cwd string
	Cmd string // detected tool: nvim, lazygit, ... empty = default shell
}

type Store struct {
	db *sql.DB
}

func openStore() (*Store, error) {
	dir, err := dataDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		return nil, err
	}
	// Single writer is enough; keep pool tiny for a short-lived CLI.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	if err := s.pragma(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// pragma: personal-tool hygiene — WAL + reasonable sync + busy wait.
func (s *Store) pragma() error {
	// modernc accepts these; ignore failures on exotic builds
	pragmas := []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA busy_timeout = 1000`,
		`PRAGMA temp_store = MEMORY`,
	}
	for _, p := range pragmas {
		if _, err := s.db.Exec(p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

func dataDir() (string, error) {
	// Prefer gotomux; fall back to legacy dir names if they already exist.
	base := func(root string) string {
		candidates := []string{
			filepath.Join(root, "gotomux"),
			filepath.Join(root, "go-tomux"),
			filepath.Join(root, "tmux_project"),
		}
		for _, c := range candidates {
			if st, err := os.Stat(c); err == nil && st.IsDir() {
				return c
			}
		}
		return candidates[0]
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return base(xdg), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return base(filepath.Join(home, ".local", "share")), nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS session (
  name       TEXT PRIMARY KEY,
  cwd        TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_used  INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS window (
  id      INTEGER PRIMARY KEY,
  session TEXT NOT NULL REFERENCES session(name) ON DELETE CASCADE,
  idx     INTEGER NOT NULL,
  name    TEXT NOT NULL DEFAULT '',
  cwd     TEXT NOT NULL DEFAULT '',
  layout  TEXT,
  UNIQUE(session, idx)
);
CREATE TABLE IF NOT EXISTS pane (
  id        INTEGER PRIMARY KEY,
  window_id INTEGER NOT NULL REFERENCES window(id) ON DELETE CASCADE,
  idx       INTEGER NOT NULL,
  cwd       TEXT,
  cmd       TEXT,
  UNIQUE(window_id, idx)
);
`)
	if err != nil {
		return err
	}
	// soft-migrate older DBs missing window.cwd
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('window') WHERE name='cwd'`).Scan(&n)
	if n == 0 {
		_, _ = s.db.Exec(`ALTER TABLE window ADD COLUMN cwd TEXT NOT NULL DEFAULT ''`)
	}
	if _, err = s.db.Exec(`
CREATE TABLE IF NOT EXISTS usage (
  name      TEXT PRIMARY KEY,
  opens     INTEGER NOT NULL DEFAULT 0,
  kills     INTEGER NOT NULL DEFAULT 0,
  last_open INTEGER NOT NULL DEFAULT 0,
  last_kill INTEGER NOT NULL DEFAULT 0
);`); err != nil {
		return err
	}
	if _, err = s.db.Exec(`
CREATE TABLE IF NOT EXISTS pair (
  a        TEXT NOT NULL,
  b        TEXT NOT NULL,
  n        INTEGER NOT NULL DEFAULT 0,
  last     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (a, b)
);`); err != nil {
		return err
	}
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_session_last_used ON session(last_used DESC)`)
	return err
}

func (s *Store) ListNames() ([]string, error) {
	ms, err := s.ListMeta()
	if err != nil {
		return nil, err
	}
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Name
	}
	return out, nil
}

// PresetMeta is name+cwd+recency for list/dedup/rank (no full layout load).
type PresetMeta struct {
	Name     string
	Cwd      string
	LastUsed int64
}

func (s *Store) ListMeta() ([]PresetMeta, error) {
	rows, err := s.db.Query(`SELECT name, cwd, last_used FROM session ORDER BY last_used DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PresetMeta
	for rows.Next() {
		var m PresetMeta
		if err := rows.Scan(&m.Name, &m.Cwd, &m.LastUsed); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) Get(name string) (*Preset, error) {
	var p Preset
	err := s.db.QueryRow(`SELECT name, cwd FROM session WHERE name = ?`, name).Scan(&p.Name, &p.Cwd)
	if err != nil {
		return nil, err
	}
	type winRow struct {
		id     int64
		idx    int
		name   string
		cwd    string
		layout string
	}
	wrows, err := s.db.Query(
		`SELECT id, idx, name, COALESCE(cwd,''), COALESCE(layout,'') FROM window WHERE session = ? ORDER BY idx`,
		name,
	)
	if err != nil {
		return nil, err
	}
	var wins []winRow
	for wrows.Next() {
		var w winRow
		if err := wrows.Scan(&w.id, &w.idx, &w.name, &w.cwd, &w.layout); err != nil {
			wrows.Close()
			return nil, err
		}
		wins = append(wins, w)
	}
	if err := wrows.Err(); err != nil {
		wrows.Close()
		return nil, err
	}
	wrows.Close()

	for _, wr := range wins {
		w := PresetWindow{Idx: wr.idx, Name: wr.name, Cwd: wr.cwd, Layout: wr.layout}
		prows, err := s.db.Query(
			`SELECT idx, COALESCE(cwd,''), COALESCE(cmd,'') FROM pane WHERE window_id = ? ORDER BY idx`,
			wr.id,
		)
		if err != nil {
			return nil, err
		}
		for prows.Next() {
			var pn PresetPane
			if err := prows.Scan(&pn.Idx, &pn.Cwd, &pn.Cmd); err != nil {
				prows.Close()
				return nil, err
			}
			w.Panes = append(w.Panes, pn)
		}
		err = prows.Err()
		prows.Close()
		if err != nil {
			return nil, err
		}
		p.Windows = append(p.Windows, w)
	}
	return &p, nil
}

func (s *Store) Save(p *Preset) error {
	if !validSessionName(p.Name) {
		return fmt.Errorf("invalid session name %q", p.Name)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().Unix()
	if _, err := tx.Exec(`DELETE FROM session WHERE name = ?`, p.Name); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO session(name, cwd, created_at, last_used) VALUES(?,?,?,?)`,
		p.Name, p.Cwd, now, now,
	); err != nil {
		return err
	}
	for _, w := range p.Windows {
		res, err := tx.Exec(
			`INSERT INTO window(session, idx, name, cwd, layout) VALUES(?,?,?,?,?)`,
			p.Name, w.Idx, w.Name, w.Cwd, w.Layout,
		)
		if err != nil {
			return err
		}
		wid, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for _, pn := range w.Panes {
			if _, err := tx.Exec(
				`INSERT INTO pane(window_id, idx, cwd, cmd) VALUES(?,?,?,?)`,
				wid, pn.Idx, pn.Cwd, pn.Cmd,
			); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) Touch(name string) error {
	_, err := s.db.Exec(`UPDATE session SET last_used = ? WHERE name = ?`, time.Now().Unix(), name)
	return err
}

func (s *Store) Delete(name string) error {
	_, err := s.db.Exec(`DELETE FROM session WHERE name = ?`, name)
	return err
}

// Usage is per-session connect/kill stats for frecency ranking.
type Usage struct {
	Name     string
	Opens    int64
	Kills    int64
	LastOpen int64
	LastKill int64
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

// AllUsage returns name → usage for ranking merge.
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
		_ = s.RecordPair(name, other)
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

func (s *Store) String() string {
	return fmt.Sprintf("Store{%v}", s.db)
}
