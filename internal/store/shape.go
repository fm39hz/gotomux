package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/project"
	"github.com/fm39hz/gotomux/internal/store/dbgen"
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
	return s.SaveFreezeWithBaseline(sess, nil, shapeID, shapeKey, shapeBody, setSticky)
}

// SaveFreezeWithBaseline commits the instance preset, pure shape, optional
// sticky pointer and reset baseline atomically.
func (s *Store) SaveFreezeWithBaseline(sess, baseline *model.Session, shapeID, shapeKey, shapeBody string, setSticky bool) (outShapeID string, shapeCreated bool, err error) {
	if sess == nil {
		return "", false, fmt.Errorf("nil preset")
	}
	if !project.ValidSessionName(sess.Name) {
		return "", false, fmt.Errorf("invalid session name %q", sess.Name)
	}
	if baseline != nil && baseline.Name != sess.Name {
		return "", false, fmt.Errorf("baseline name %q does not match frozen session %q", baseline.Name, sess.Name)
	}
	if baseline != nil {
		if !model.RecipeFromSession(*sess).SameReplay(model.RecipeFromSession(*baseline)) {
			return "", false, fmt.Errorf("freeze recipe and baseline replay content differ")
		}
		if sess.LogicalID == "" || sess.LogicalID != baseline.LogicalID {
			return "", false, fmt.Errorf("freeze recipe and baseline must share a non-empty logical identity")
		}
		if len(sess.Windows) != len(baseline.Windows) {
			return "", false, fmt.Errorf("freeze recipe and baseline window identities differ")
		}
		for wi, window := range sess.Windows {
			frozen := baseline.Windows[wi]
			if window.SlotID == "" || window.SlotID != frozen.SlotID || len(window.Panes) != len(frozen.Panes) {
				return "", false, fmt.Errorf("freeze recipe and baseline window %d identities differ", wi)
			}
			for pi, pane := range window.Panes {
				if pane.SlotID == "" || pane.SlotID != frozen.Panes[pi].SlotID {
					return "", false, fmt.Errorf("freeze recipe and baseline window %d pane %d identities differ", wi, pi)
				}
			}
		}
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
	if baseline != nil {
		if err := saveBaselineTx(tx, baseline, true); err != nil {
			return "", false, err
		}
	}
	if baseline != nil {
		if err := dbgen.New(tx).AbortReconcilesForFreeze(storeContext, dbgen.AbortReconcilesForFreezeParams{UpdatedAt: time.Now().Unix(), SessionLogicalID: baseline.LogicalID, SessionName: baseline.Name}); err != nil {
			return "", false, fmt.Errorf("supersede reconcile journal on freeze: %w", err)
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
	return dbgen.New(tx).SetStickyShape(storeContext, shapeID)
}

func putShapeTx(tx *sql.Tx, id, key, body string) (outID string, created bool, err error) {
	if key == "" || body == "" {
		return "", false, fmt.Errorf("shape needs key and body")
	}
	q := dbgen.New(tx)
	exist, err := q.ShapeIDByKey(storeContext, key)
	if err == nil && exist != "" {
		now := time.Now().Unix()
		_ = q.UpdateShapeBodyByID(storeContext, dbgen.UpdateShapeBodyByIDParams{Body: body, UpdatedAt: now, ID: exist})
		return exist, false, nil
	}
	if id == "" {
		id = key
		if len(id) > 32 {
			id = id[:32]
		}
	}
	now := time.Now().Unix()
	err = q.InsertShape(storeContext, dbgen.InsertShapeParams{ID: id, Key: key, Body: body, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		if existing, e2 := q.ShapeIDByKey(storeContext, key); e2 == nil && existing != "" {
			return existing, false, nil
		}
		id2 := id + "-" + key[:min6(key)]
		err = q.InsertShape(storeContext, dbgen.InsertShapeParams{ID: id2, Key: key, Body: body, CreatedAt: now, UpdatedAt: now})
		if err != nil {
			if existing, e2 := q.ShapeIDByKey(storeContext, key); e2 == nil && existing != "" {
				return existing, false, nil
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
	if sess == nil {
		return fmt.Errorf("nil preset")
	}
	return saveRecipeTx(tx, model.RecipeFromSession(*sess))
}

func saveRecipeTx(tx *sql.Tx, recipe model.Recipe) error {
	now := time.Now().Unix()
	if err := deleteSessionsForSave(tx, recipe.Name, recipe.Cwd); err != nil {
		return err
	}
	q := dbgen.New(tx)
	if err := q.InsertSession(storeContext, dbgen.InsertSessionParams{Name: recipe.Name, Cwd: recipe.Cwd, CreatedAt: now, LastUsed: now, LogicalID: recipe.LogicalID}); err != nil {
		return err
	}
	for i, w := range recipe.Windows {
		widx := w.Idx
		if widx == 0 {
			widx = i
		}
		res, err := q.InsertWindow(storeContext, dbgen.InsertWindowParams{Session: recipe.Name, Idx: int64(widx), Name: w.Name, Cwd: w.Cwd, Layout: sql.NullString{String: w.Layout, Valid: true}, SlotID: w.SlotID})
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
			if err := q.InsertPane(storeContext, dbgen.InsertPaneParams{WindowID: wid, Idx: int64(pidx), Cwd: sql.NullString{String: pn.Cwd, Valid: true}, Cmd: sql.NullString{String: pn.Cmd, Valid: true}, CmdPath: pn.CmdPath, StartCmd: pn.StartCmd, SlotID: pn.SlotID}); err != nil {
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
	row, err := s.q.ShapeByKey(storeContext, key)
	if err == nil {
		id, body = row.ID, row.Body
	}
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
	row, err := s.q.ShapeMetaByID(storeContext, id)
	m := ShapeMeta{Body: row.Body, UpdatedAt: row.UpdatedAt, MirrorPath: row.MirrorPath, MirrorSig: row.MirrorSig}
	return m, err == nil && m.Body != ""
}

// SetShapeMirror records which file this shape was mirrored to and the signature
// of the body written there.
func (s *Store) SetShapeMirror(id, path, sig string) error {
	if id == "" {
		return nil
	}
	return s.q.UpdateShapeMirror(storeContext, dbgen.UpdateShapeMirrorParams{MirrorPath: path, MirrorSig: sig, ID: id})
}

// GetShape returns body by id.
func (s *Store) GetShape(id string) (body string, ok bool) {
	if id == "" {
		return "", false
	}
	body, err := s.q.ShapeBodyByID(storeContext, id)
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
	err = s.q.InsertShape(storeContext, dbgen.InsertShapeParams{ID: id, Key: key, Body: body, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		if exist, _, ok := s.GetShapeByKey(key); ok {
			return exist, false, nil
		}
		id2 := id + "-" + key[:6]
		err = s.q.InsertShape(storeContext, dbgen.InsertShapeParams{ID: id2, Key: key, Body: body, CreatedAt: now, UpdatedAt: now})
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
		_ = s.q.UpdateShapeBodyAndTime(storeContext, dbgen.UpdateShapeBodyAndTimeParams{Body: body, UpdatedAt: now, ID: exist})
		_ = s.q.RebindStickyShape(storeContext, dbgen.RebindStickyShapeParams{ShapeID: exist, ShapeID_2: id})
		// drop placement rows for duplicate id (patterns may already exist under exist)
		_ = s.q.DeletePlacementForShape(storeContext, id)
		_ = s.q.DeleteShape(storeContext, id)
		return nil
	}
	return s.q.UpsertShapeByID(storeContext, dbgen.UpsertShapeByIDParams{ID: id, Key: key, Body: body, CreatedAt: now, UpdatedAt: now})
}

// ListShapes returns all shape ids (for export).
func (s *Store) ListShapes() ([]string, error) {
	return s.q.ListShapeIDs(storeContext)
}

// StickyID returns sticky shape id (default if unset).
func (s *Store) StickyID() string {
	id, err := s.q.StickyShapeID(storeContext)
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
	return s.q.SetStickyShape(storeContext, shapeID)
}

func (s *Store) String() string {
	return fmt.Sprintf("Store{%v}", s.db)
}
