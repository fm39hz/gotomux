package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/project"
)

// ShapeRow is a pure shape blob (JSON body) keyed for dedupe.
type ShapeRow struct {
	ID   string
	Key  string
	Body string
}

// SaveFreeze: ONE transaction - instance preset tree + pure shape (dedupe by key).
// setSticky true -> sticky points at resulting shape id in same tx.
// Call writeConfigMirror only AFTER this returns nil (post-commit).
func (s *Store) SaveFreeze(sess *model.Session, shapeID, shapeKey, shapeBody string, setSticky bool) (outShapeID string, shapeCreated bool, err error) {
	if sess == nil {
		return "", false, fmt.Errorf("nil preset")
	}
	if !project.ValidSessionName(sess.Name) {
		return "", false, fmt.Errorf("invalid session name %q", sess.Name)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := savePresetTx(tx, sess); err != nil {
		return "", false, err
	}
	outShapeID, shapeCreated, err = putShapeTx(tx, shapeID, shapeKey, shapeBody)
	if err != nil {
		return "", false, err
	}
	if setSticky {
		if err := setStickyTx(tx, outShapeID); err != nil {
			return "", false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return outShapeID, shapeCreated, nil
}

// StickShape: pure shape + sticky pointer in ONE transaction.
func (s *Store) StickShape(shapeID, shapeKey, shapeBody string) (outID string, created bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback() }()
	outID, created, err = putShapeTx(tx, shapeID, shapeKey, shapeBody)
	if err != nil {
		return "", false, err
	}
	if err := setStickyTx(tx, outID); err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return outID, created, nil
}

// RememberShapeOnly: shape upsert only, one tx.
func (s *Store) RememberShapeOnly(shapeID, shapeKey, shapeBody string) (outID string, created bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback() }()
	outID, created, err = putShapeTx(tx, shapeID, shapeKey, shapeBody)
	if err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return outID, created, nil
}

func setStickyTx(tx *sql.Tx, shapeID string) error {
	if shapeID == "" {
		shapeID = "default"
	}
	_, err := tx.Exec(`
INSERT INTO sticky(id, shape_id) VALUES(1, ?)
ON CONFLICT(id) DO UPDATE SET shape_id = excluded.shape_id
`, shapeID)
	return err
}

func putShapeTx(tx *sql.Tx, id, key, body string) (outID string, created bool, err error) {
	if key == "" || body == "" {
		return "", false, fmt.Errorf("shape needs key and body")
	}
	var exist string
	err = tx.QueryRow(`SELECT id FROM shape WHERE key = ?`, key).Scan(&exist)
	if err == nil && exist != "" {
		now := time.Now().Unix()
		_, _ = tx.Exec(`UPDATE shape SET body = ?, updated_at = ? WHERE id = ?`, body, now, exist)
		return exist, false, nil
	}
	if id == "" {
		id = key
		if len(id) > 32 {
			id = id[:32]
		}
	}
	now := time.Now().Unix()
	_, err = tx.Exec(`INSERT INTO shape(id, key, body, created_at, updated_at) VALUES(?,?,?,?,?)`, id, key, body, now, now)
	if err != nil {
		if e2 := tx.QueryRow(`SELECT id FROM shape WHERE key = ?`, key).Scan(&exist); e2 == nil && exist != "" {
			return exist, false, nil
		}
		id2 := id + "-" + key[:min6(key)]
		_, err = tx.Exec(`INSERT INTO shape(id, key, body, created_at, updated_at) VALUES(?,?,?,?,?)`, id2, key, body, now, now)
		if err != nil {
			if e2 := tx.QueryRow(`SELECT id FROM shape WHERE key = ?`, key).Scan(&exist); e2 == nil && exist != "" {
				return exist, false, nil
			}
			return "", false, err
		}
		return id2, true, nil
	}
	return id, true, nil
}

func min6(key string) int {
	if len(key) < 6 {
		return len(key)
	}
	return 6
}

func savePresetTx(tx *sql.Tx, sess *model.Session) error {
	now := time.Now().Unix()
	if err := deleteSessionsForSave(tx, sess.Name, sess.Cwd); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO session(name, cwd, created_at, last_used) VALUES(?,?,?,?)`,
		sess.Name, sess.Cwd, now, now,
	); err != nil {
		return err
	}
	for i, w := range sess.Windows {
		widx := w.Idx
		if widx == 0 {
			widx = i
		}
		res, err := tx.Exec(
			`INSERT INTO window(session, idx, name, cwd, layout) VALUES(?,?,?,?,?)`,
			sess.Name, widx, w.Name, w.Cwd, w.Layout,
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
	return nil
}

// GetShapeByKey returns shape id+body if key exists.
func (s *Store) GetShapeByKey(key string) (id, body string, ok bool) {
	if key == "" {
		return "", "", false
	}
	err := s.db.QueryRow(`SELECT id, body FROM shape WHERE key = ?`, key).Scan(&id, &body)
	return id, body, err == nil && id != ""
}

// GetShapeMeta returns body + updated_at for merge decisions.
// ShapeMeta is a shape row plus the state of its config-dir mirror.
//
// MirrorSig is the signature of the body last written to MirrorPath. It is what
// lets the mirror tell "the user edited this file" from "the DB moved on" — a
// distinction timestamps cannot make, because both sides have one-second
// resolution and a freeze writes the row and the file within the same second.
type ShapeMeta struct {
	Body       string
	UpdatedAt  int64
	MirrorPath string
	MirrorSig  string
}

func (s *Store) GetShapeMeta(id string) (ShapeMeta, bool) {
	if id == "" {
		return ShapeMeta{}, false
	}
	var m ShapeMeta
	err := s.db.QueryRow(`
SELECT body, COALESCE(updated_at, created_at, 0),
       COALESCE(mirror_path, ''), COALESCE(mirror_sig, '')
FROM shape WHERE id = ?`, id).Scan(&m.Body, &m.UpdatedAt, &m.MirrorPath, &m.MirrorSig)
	return m, err == nil && m.Body != ""
}

// SetShapeMirror records which file this shape was mirrored to and the signature
// of the body written there.
func (s *Store) SetShapeMirror(id, path, sig string) error {
	if id == "" {
		return nil
	}
	_, err := s.db.Exec(`UPDATE shape SET mirror_path = ?, mirror_sig = ? WHERE id = ?`, path, sig, id)
	return err
}

// GetShape returns body by id.
func (s *Store) GetShape(id string) (body string, ok bool) {
	if id == "" {
		return "", false
	}
	err := s.db.QueryRow(`SELECT body FROM shape WHERE id = ?`, id).Scan(&body)
	return body, err == nil && body != ""
}

// PutShape inserts if key unknown. If key exists, returns existing id without rewrite
// (topology dedupe). Use UpsertShapeByID to update body when user edits by id.
func (s *Store) PutShape(id, key, body string) (outID string, created bool, err error) {
	if key == "" || body == "" {
		return "", false, fmt.Errorf("shape needs key and body")
	}
	if exist, _, ok := s.GetShapeByKey(key); ok {
		return exist, false, nil
	}
	if id == "" {
		id = key
		if len(id) > 32 {
			id = id[:32]
		}
	}
	now := time.Now().Unix()
	_, err = s.db.Exec(
		`INSERT INTO shape(id, key, body, created_at, updated_at) VALUES(?,?,?,?,?)`,
		id, key, body, now, now,
	)
	if err != nil {
		if exist, _, ok := s.GetShapeByKey(key); ok {
			return exist, false, nil
		}
		id2 := id + "-" + key[:6]
		_, err = s.db.Exec(
			`INSERT INTO shape(id, key, body, created_at, updated_at) VALUES(?,?,?,?,?)`,
			id2, key, body, now, now,
		)
		if err != nil {
			if exist, _, ok := s.GetShapeByKey(key); ok {
				return exist, false, nil
			}
			return "", false, err
		}
		return id2, true, nil
	}
	return id, true, nil
}

// UpsertShapeByID writes/updates shape at stable id (hand-edit / re-export path).
// If another row already owns the same topology key, keep the existing id (dedupe)
// and drop the duplicate id (rewire sticky/placement).
func (s *Store) UpsertShapeByID(id, key, body string) error {
	if id == "" || body == "" {
		return fmt.Errorf("shape id and body required")
	}
	if key == "" {
		key = id
	}
	now := time.Now().Unix()
	if exist, _, ok := s.GetShapeByKey(key); ok && exist != id {
		// same essence under two ids -> keep exist, merge pointers, delete id
		_, _ = s.db.Exec(`UPDATE shape SET body = ?, updated_at = ? WHERE id = ?`, body, now, exist)
		_, _ = s.db.Exec(`UPDATE sticky SET shape_id = ? WHERE shape_id = ?`, exist, id)
		// drop placement rows for duplicate id (patterns may already exist under exist)
		_, _ = s.db.Exec(`DELETE FROM placement WHERE shape_id = ?`, id)
		_, _ = s.db.Exec(`DELETE FROM shape WHERE id = ?`, id)
		return nil
	}
	_, err := s.db.Exec(`
INSERT INTO shape(id, key, body, created_at, updated_at) VALUES(?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  key = excluded.key,
  body = excluded.body,
  updated_at = excluded.updated_at
`, id, key, body, now, now)
	return err
}

// ListShapes returns all shape ids (for export).
func (s *Store) ListShapes() ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM shape ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// StickyID returns sticky shape id (default if unset).
func (s *Store) StickyID() string {
	var id string
	err := s.db.QueryRow(`SELECT shape_id FROM sticky WHERE id = 1`).Scan(&id)
	if err != nil || id == "" {
		return "default"
	}
	return id
}

// SetSticky points sticky at shape id.
func (s *Store) SetSticky(shapeID string) error {
	if shapeID == "" {
		shapeID = "default"
	}
	_, err := s.db.Exec(`
INSERT INTO sticky(id, shape_id) VALUES(1, ?)
ON CONFLICT(id) DO UPDATE SET shape_id = excluded.shape_id
`, shapeID)
	return err
}

func (s *Store) String() string {
	return fmt.Sprintf("Store{%v}", s.db)
}
