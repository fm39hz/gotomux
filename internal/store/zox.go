package store

import (
	"time"

	"github.com/fm39hz/gotomux/internal/store/dbgen"
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
	meta, err := s.q.ZoxMeta(storeContext)
	if err != nil {
		return nil, 0, "", false
	}
	items, err := s.q.ZoxItems(storeContext)
	if err != nil {
		return nil, 0, "", false
	}
	for _, item := range items {
		r := ZoxRow{Name: item.Name, Path: item.Path, Title: item.Title, Desc: item.Desc, Recency: item.Recency}
		if r.Name == "" {
			continue
		}
		if r.Title == "" {
			r.Title = "[Zoxide] " + r.Name
		}
		rows = append(rows, r)
	}
	if len(rows) == 0 {
		return nil, 0, "", false
	}
	return rows, meta.Updated, meta.Sig, true
}

// SaveZox replaces the zoxide cache in one transaction. sig is the signature of
// the path list the rows came from.
func (s *Store) SaveZox(rows []ZoxRow, sig string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteZoxItems(storeContext); err != nil {
		return err
	}
	now := time.Now().Unix()
	if err := qtx.UpsertZoxMeta(storeContext, dbgen.UpsertZoxMetaParams{Updated: now, Sig: sig}); err != nil {
		return err
	}
	for i, r := range rows {
		if r.Name == "" {
			continue
		}
		if err := qtx.InsertZoxItem(storeContext, dbgen.InsertZoxItemParams{
			Ord: int64(i), Name: r.Name, Path: r.Path, Title: r.Title, Desc: r.Desc, Recency: r.Recency,
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}
