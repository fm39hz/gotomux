package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/project"
)

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

// PresetMeta is name+cwd+recency for list/dedup/rank (no full tree load).
// PresetMeta is served over IPC as well as read from SQLite, so it carries json
// tags: without them the payload mixed snake_case keys from daemon.Response with
// Go field names from the embedded types.
type PresetMeta struct {
	Name     string `json:"name"`
	Cwd      string `json:"cwd,omitempty"`
	LastUsed int64  `json:"last_used,omitempty"`
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

func (s *Store) Get(name string) (*model.Session, error) {
	var sessName, cwd string
	err := s.db.QueryRow(`SELECT name, cwd FROM session WHERE name = ?`, name).Scan(&sessName, &cwd)
	if err != nil {
		return nil, err
	}
	sess := &model.Session{Name: sessName, Cwd: cwd}

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
		w := model.Window{Idx: wr.idx, Name: wr.name, Cwd: wr.cwd, Layout: wr.layout}
		prows, err := s.db.Query(
			`SELECT idx, COALESCE(cwd,''), COALESCE(cmd,'') FROM pane WHERE window_id = ? ORDER BY idx`,
			wr.id,
		)
		if err != nil {
			return nil, err
		}
		for prows.Next() {
			var pn model.Pane
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
		sess.Windows = append(sess.Windows, w)
	}
	return sess, nil
}

func (s *Store) Save(sess *model.Session) error {
	if sess == nil {
		return fmt.Errorf("nil preset")
	}
	if !project.ValidSessionName(sess.Name) {
		return fmt.Errorf("invalid session name %q", sess.Name)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := savePresetTx(tx, sess); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteSessionsForSave removes presets that this Save should replace.
func deleteSessionsForSave(tx *sql.Tx, name, cwd string) error {
	// exact name
	if _, err := tx.Exec(`DELETE FROM session WHERE name = ?`, name); err != nil {
		return err
	}
	// same project root -> one preset per project path
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

// RebindName moves ranking telemetry old->new after preset rename.
// usage rows merge (opens/kills sum, last_* max); pair endpoints rewrite.
// Best-effort for ranking only - never blocks Save.
func (s *Store) RebindName(old, newName string) error {
	old, newName = strings.TrimSpace(old), strings.TrimSpace(newName)
	if old == "" || newName == "" || old == newName {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// usage: merge into new, drop old
	var oOpens, oKills, oLastOpen, oLastKill int64
	err = tx.QueryRow(
		`SELECT opens, kills, last_open, last_kill FROM usage WHERE name = ?`, old,
	).Scan(&oOpens, &oKills, &oLastOpen, &oLastKill)
	if err == nil {
		_, err = tx.Exec(`
INSERT INTO usage(name, opens, kills, last_open, last_kill) VALUES(?, ?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
  opens = opens + excluded.opens,
  kills = kills + excluded.kills,
  last_open = MAX(last_open, excluded.last_open),
  last_kill = MAX(last_kill, excluded.last_kill)
`, newName, oOpens, oKills, oLastOpen, oLastKill)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`DELETE FROM usage WHERE name = ?`, old); err != nil {
			return err
		}
	}

	// pair: rewrite endpoints; undirected canonical a < b
	rows, err := tx.Query(`SELECT a, b, n, last FROM pair WHERE a = ? OR b = ?`, old, old)
	if err != nil {
		return err
	}
	type pr struct {
		a, b    string
		n, last int64
	}
	var found []pr
	for rows.Next() {
		var r pr
		if err := rows.Scan(&r.a, &r.b, &r.n, &r.last); err != nil {
			rows.Close()
			return err
		}
		found = append(found, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range found {
		if _, err := tx.Exec(`DELETE FROM pair WHERE a = ? AND b = ?`, r.a, r.b); err != nil {
			return err
		}
		a, b := r.a, r.b
		if a == old {
			a = newName
		}
		if b == old {
			b = newName
		}
		if a == b {
			continue // self-pair after rename - drop
		}
		if a > b {
			a, b = b, a
		}
		_, err = tx.Exec(`
INSERT INTO pair(a, b, n, last) VALUES(?, ?, ?, ?)
ON CONFLICT(a, b) DO UPDATE SET
  n = n + excluded.n,
  last = MAX(last, excluded.last)
`, a, b, r.n, r.last)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
