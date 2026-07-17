package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
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
	db.SetMaxOpenConns(4)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func dataDir() (string, error) {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "tmux_project"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "tmux_project"), nil
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
	return nil
}

func (s *Store) ListNames() ([]string, error) {
	rows, err := s.db.Query(`SELECT name FROM session ORDER BY last_used DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
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

func (s *Store) String() string {
	return fmt.Sprintf("Store{%v}", s.db)
}
