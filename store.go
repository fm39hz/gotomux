package main

import (
	"database/sql"
	"fmt"
	"io"
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
	// One-shot: empty gotomux DB + legacy state → copy presets over.
	_ = migrateLegacyDB(dir)
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
	setZoxStore(s)
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
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "gotomux"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "gotomux"), nil
}

// migrateLegacyDB copies state.db from old product dirs once when gotomux has no sessions.
// Does not keep reading legacy paths afterward.
func migrateLegacyDB(dir string) error {
	dst := filepath.Join(dir, "state.db")
	// already has content?
	if hasSessionRows(dst) {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	roots := []string{filepath.Join(home, ".local", "share")}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		roots = append([]string{xdg}, roots...)
	}
	var src string
	for _, root := range roots {
		for _, leg := range []string{"tmux_project", "go-tomux"} {
			p := filepath.Join(root, leg, "state.db")
			if hasSessionRows(p) {
				src = p
				break
			}
		}
		if src != "" {
			break
		}
	}
	if src == "" {
		return nil
	}
	// close any WAL on source by best-effort copy of main file only is wrong if WAL pending;
	// use sqlite backup via file copy of db+wal+shm when present.
	for _, suf := range []string{"", "-wal", "-shm"} {
		s := src + suf
		d := dst + suf
		if _, err := os.Stat(s); err != nil {
			continue
		}
		if err := copyFile(s, d); err != nil {
			return err
		}
	}
	return nil
}

func hasSessionRows(dbPath string) bool {
	st, err := os.Stat(dbPath)
	if err != nil || st.Size() == 0 {
		return false
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return false
	}
	defer db.Close()
	var n int
	err = db.QueryRow(`SELECT COUNT(*) FROM session`).Scan(&n)
	return err == nil && n > 0
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
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
	if _, err = s.db.Exec(`
CREATE TABLE IF NOT EXISTS zox_meta (
  id      INTEGER PRIMARY KEY CHECK (id = 1),
  updated INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS zox_item (
  ord     INTEGER PRIMARY KEY,
  name    TEXT NOT NULL,
  path    TEXT NOT NULL,
  title   TEXT NOT NULL DEFAULT '',
  desc    TEXT NOT NULL DEFAULT '',
  recency INTEGER NOT NULL DEFAULT 0
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
	// Replace any prior row for this freeze: exact name, same project cwd, or
	// legacy alias (tmuxproject vs tmux-project after sanitize change).
	if err := deleteSessionsForSave(tx, p.Name, p.Cwd); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO session(name, cwd, created_at, last_used) VALUES(?,?,?,?)`,
		p.Name, p.Cwd, now, now,
	); err != nil {
		return err
	}
	for i, w := range p.Windows {
		widx := w.Idx
		if widx == 0 {
			widx = i
		}
		res, err := tx.Exec(
			`INSERT INTO window(session, idx, name, cwd, layout) VALUES(?,?,?,?,?)`,
			p.Name, widx, w.Name, w.Cwd, w.Layout,
		)
		if err != nil {
			return err
		}
		wid, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for j, pn := range w.Panes {
			pidx := pn.Idx
			if pidx == 0 {
				pidx = j
			}
			if _, err := tx.Exec(
				`INSERT INTO pane(window_id, idx, cwd, cmd) VALUES(?,?,?,?)`,
				wid, pidx, pn.Cwd, pn.Cmd,
			); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// deleteSessionsForSave removes presets that this Save should replace.
func deleteSessionsForSave(tx *sql.Tx, name, cwd string) error {
	// exact name
	if _, err := tx.Exec(`DELETE FROM session WHERE name = ?`, name); err != nil {
		return err
	}
	// same project root → one preset per project path
	if cwd != "" {
		if _, err := tx.Exec(`DELETE FROM session WHERE cwd = ? AND name != ?`, cwd, name); err != nil {
			return err
		}
	}
	// legacy aliases: names that collapse to the same key without -/_
	key := sessionAliasKey(name)
	if key == "" {
		return nil
	}
	rows, err := tx.Query(`SELECT name FROM session`)
	if err != nil {
		return err
	}
	var victims []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		if n != name && sessionAliasKey(n) == key {
			victims = append(victims, n)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, n := range victims {
		if _, err := tx.Exec(`DELETE FROM session WHERE name = ?`, n); err != nil {
			return err
		}
	}
	return nil
}

// sessionAliasKey: compare names ignoring - and _ (legacy sanitize dropped _).
func sessionAliasKey(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if r == '-' || r == '_' {
			continue
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
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

// LoadZoxItems returns cached zoxide picker rows and cache age.
func (s *Store) LoadZoxItems() (items []item, updated int64, ok bool) {
	var upd int64
	err := s.db.QueryRow(`SELECT updated FROM zox_meta WHERE id = 1`).Scan(&upd)
	if err != nil {
		return nil, 0, false
	}
	rows, err := s.db.Query(`SELECT name, path, title, desc, recency FROM zox_item ORDER BY ord`)
	if err != nil {
		return nil, 0, false
	}
	defer rows.Close()
	for rows.Next() {
		var it item
		var title, desc string
		if err := rows.Scan(&it.name, &it.path, &title, &desc, &it.recency); err != nil {
			return nil, 0, false
		}
		if it.name == "" {
			continue
		}
		it.src = srcZoxide
		it.kind = kindZoxide
		it.title = title
		if it.title == "" {
			it.title = "[Zoxide] " + it.name
		}
		it.desc = desc
		items = append(items, it)
	}
	if err := rows.Err(); err != nil || len(items) == 0 {
		return nil, 0, false
	}
	return items, upd, true
}

// SaveZoxItems replaces the zoxide item cache in one transaction.
func (s *Store) SaveZoxItems(items []item) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM zox_item`); err != nil {
		return err
	}
	now := time.Now().Unix()
	if _, err := tx.Exec(`
INSERT INTO zox_meta(id, updated) VALUES(1, ?)
ON CONFLICT(id) DO UPDATE SET updated = excluded.updated
`, now); err != nil {
		return err
	}
	for i, it := range items {
		if it.name == "" {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO zox_item(ord, name, path, title, desc, recency) VALUES(?,?,?,?,?,?)`,
			i, it.name, it.path, it.title, it.desc, it.recency,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) String() string {
	return fmt.Sprintf("Store{%v}", s.db)
}
