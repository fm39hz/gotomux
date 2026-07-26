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

// LoadZox returns the cached rows, when they were written, and the signature of
// the raw zoxide path list they were derived from.
//
// sig lets a caller decide whether re-deriving is necessary without paying for it:
// see zoxide.Signature.
func (s *Store) LoadZox() (rows []ZoxRow, updated int64, sig string, ok bool) {
	var upd int64
	var sg string
	err := s.db.QueryRow(`SELECT updated, COALESCE(sig, '') FROM zox_meta WHERE id = 1`).Scan(&upd, &sg)
	if err != nil {
		return nil, 0, "", false
	}
	q, err := s.db.Query(`SELECT name, path, title, desc, recency FROM zox_item ORDER BY ord`)
	if err != nil {
		return nil, 0, "", false
	}
	defer q.Close()
	for q.Next() {
		var r ZoxRow
		if err := q.Scan(&r.Name, &r.Path, &r.Title, &r.Desc, &r.Recency); err != nil {
			return nil, 0, "", false
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
		return nil, 0, "", false
	}
	return rows, upd, sg, true
}

// SaveZox replaces the zoxide cache in one transaction. sig is the signature of
// the path list the rows came from.
func (s *Store) SaveZox(rows []ZoxRow, sig string) error {
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
INSERT INTO zox_meta(id, updated, sig) VALUES(1, ?, ?)
ON CONFLICT(id) DO UPDATE SET updated = excluded.updated, sig = excluded.sig
`, now, sig); err != nil {
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
