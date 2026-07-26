package store

import (
	"time"
)

// ZoxRow is a cached zoxide picker row (no UI types).
// ZoxRow is served over IPC as well as persisted; see PresetMeta on the tags.
type ZoxRow struct {
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"`
	Title   string `json:"title,omitempty"`
	Desc    string `json:"desc,omitempty"`
	Recency int64  `json:"recency,omitempty"`
}

// LoadZox returns cached zoxide rows and cache age (unix updated).
func (s *Store) LoadZox() (rows []ZoxRow, updated int64, ok bool) {
	var upd int64
	err := s.db.QueryRow(`SELECT updated FROM zox_meta WHERE id = 1`).Scan(&upd)
	if err != nil {
		return nil, 0, false
	}
	q, err := s.db.Query(`SELECT name, path, title, desc, recency FROM zox_item ORDER BY ord`)
	if err != nil {
		return nil, 0, false
	}
	defer q.Close()
	for q.Next() {
		var r ZoxRow
		if err := q.Scan(&r.Name, &r.Path, &r.Title, &r.Desc, &r.Recency); err != nil {
			return nil, 0, false
		}
		if r.Name == "" {
			continue
		}
		if r.Title == "" {
			r.Title = "[Zoxide] " + r.Name
		}
		rows = append(rows, r)
	}
	if err := q.Err(); err != nil || len(rows) == 0 {
		return nil, 0, false
	}
	return rows, upd, true
}

// SaveZox replaces the zoxide cache in one transaction.
func (s *Store) SaveZox(rows []ZoxRow) error {
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
	for i, r := range rows {
		if r.Name == "" {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO zox_item(ord, name, path, title, desc, recency) VALUES(?,?,?,?,?,?)`,
			i, r.Name, r.Path, r.Title, r.Desc, r.Recency,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}
