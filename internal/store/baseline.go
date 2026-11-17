package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fm39hz/gotomux/internal/identity"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/store/dbgen"
	"github.com/fm39hz/gotomux/internal/tmuxlayout"
)

// Baseline: the recorded starting layout of a live session, stored as a full
// instance preset (windows / panes / commands / cwds). ReconcileSession diffs the
// live session against it and rebuilds missing windows at their baseline
// positions. JSON keeps the whole tree readable in one row — baseline is always
// read wholesale, never queried by parts.
//
// Written when a session is first created (ConnectProject), when a preset is
// loaded, on every freeze, and after reconciliation refreshes runtime bindings.
func (s *Store) SaveBaseline(sess *model.Session) error {
	if sess == nil {
		return fmt.Errorf("save baseline: nil session")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := saveBaselineTx(tx, sess, false); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveInitialBaseline binds a legacy recipe only when the first verified
// instantiation creates its baseline, in one transaction. Later baseline
// refreshes never rewrite or reinterpret the independent recipe.
func (s *Store) SaveInitialBaseline(sess *model.Session) error {
	if sess == nil {
		return fmt.Errorf("save initial baseline: nil session")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := saveBaselineTx(tx, sess, false); err != nil {
		return err
	}
	if err := bindPresetIdentityTx(tx, sess); err != nil {
		return fmt.Errorf("bind initial recipe identity: %w", err)
	}
	return tx.Commit()
}

func (s *Store) GetBaselineState(name string) (*model.Baseline, error) {
	session, err := s.GetBaseline(name)
	if err != nil || session == nil {
		return nil, err
	}
	state := model.BaselineFromSession(*session)
	return &state, nil
}

func (s *Store) GetBaselineStateByLogicalID(id string) (*model.Baseline, error) {
	session, err := s.GetBaselineByLogicalID(id)
	if err != nil || session == nil {
		return nil, err
	}
	state := model.BaselineFromSession(*session)
	return &state, nil
}

func (s *Store) SaveBaselineState(state model.Baseline) error {
	session := state.Session()
	return s.SaveBaseline(&session)
}

func saveBaselineTx(tx *sql.Tx, sess *model.Session, allowSessionReplacement bool) error {
	if sess == nil {
		return fmt.Errorf("save baseline: nil session")
	}
	return saveBaselineContractTx(tx, model.BaselineFromSession(*sess), allowSessionReplacement)
}

func saveBaselineContractTx(tx *sql.Tx, baseline model.Baseline, allowSessionReplacement bool) error {
	session := baseline.Session()
	sess := &session
	if sess.Name == "" {
		return fmt.Errorf("save baseline: empty session name")
	}
	copy := *sess
	copy.Windows = append([]model.Window(nil), sess.Windows...)
	for i := range copy.Windows {
		copy.Windows[i].Panes = append([]model.Pane(nil), sess.Windows[i].Panes...)
	}
	if copy.SchemaVersion == 0 {
		copy.SchemaVersion = model.CurrentBaselineSchemaVersion
	}
	if err := validateBaselineIdentityTree(copy); err != nil {
		return fmt.Errorf("save baseline: %w", err)
	}
	if err := normalizeBaselineLayouts(&copy); err != nil {
		return fmt.Errorf("save baseline layout: %w", err)
	}
	q := dbgen.New(tx)
	body, err := json.Marshal(&copy)
	if err != nil {
		return fmt.Errorf("save baseline: %w", err)
	}
	var oldName string
	if copy.LogicalID != "" {
		oldName, err = q.BaselineNameByLogicalID(storeContext, copy.LogicalID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("save baseline lookup by logical ID: %w", err)
		}
	}
	if oldName != "" {
		if oldName != copy.Name {
			_, err := q.BaselineLogicalIDByName(storeContext, copy.Name)
			if err == nil {
				return fmt.Errorf("save baseline: name %q already belongs to another baseline", copy.Name)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("save baseline name check: %w", err)
			}
		}
		err = q.UpdateBaselineByLogicalID(storeContext, dbgen.UpdateBaselineByLogicalIDParams{
			Name: copy.Name, Body: string(body), UpdatedAt: time.Now().Unix(), LogicalID: copy.LogicalID, LogicalID_2: copy.LogicalID,
		})
		if err != nil {
			return fmt.Errorf("save baseline: %w", err)
		}
		return nil
	}
	var existingID string
	existingID, err = q.BaselineLogicalIDByName(storeContext, copy.Name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("save baseline lookup by name: %w", err)
	}
	if err == nil {
		if existingID != "" && existingID != copy.LogicalID && !allowSessionReplacement {
			return fmt.Errorf("save baseline: session name %q belongs to logical session %s; freeze the intended replacement before saving", copy.Name, existingID)
		}
		err = q.UpdateBaselineByName(storeContext, dbgen.UpdateBaselineByNameParams{Body: string(body), UpdatedAt: time.Now().Unix(), LogicalID: copy.LogicalID, Name: copy.Name})
	} else {
		err = q.InsertBaseline(storeContext, dbgen.InsertBaselineParams{Name: copy.Name, Body: string(body), UpdatedAt: time.Now().Unix(), LogicalID: copy.LogicalID})
	}
	if err != nil {
		return fmt.Errorf("save baseline: %w", err)
	}
	return nil
}

func validateBaselineIdentityTree(session model.Session) error {
	if session.LogicalID == "" {
		return fmt.Errorf("baseline has no session identity")
	}
	if len(session.Windows) == 0 {
		return fmt.Errorf("baseline has no windows")
	}
	seen := map[string]string{}
	add := func(kind, id string) error {
		if id == "" {
			return fmt.Errorf("baseline %s has no identity", kind)
		}
		if err := identity.Validate(id); err != nil {
			return fmt.Errorf("baseline %s has malformed identity: %w", kind, err)
		}
		if previous, ok := seen[id]; ok {
			return fmt.Errorf("baseline identity %s is shared by %s and %s", id, previous, kind)
		}
		seen[id] = kind
		return nil
	}
	if err := add("session", session.LogicalID); err != nil {
		return err
	}
	for wi, window := range session.Windows {
		if err := add(fmt.Sprintf("window %d", wi), window.SlotID); err != nil {
			return err
		}
		if len(window.Panes) == 0 {
			return fmt.Errorf("baseline window %d has no panes", wi)
		}
		for pi, pane := range window.Panes {
			if err := add(fmt.Sprintf("window %d pane %d", wi, pi), pane.SlotID); err != nil {
				return err
			}
		}
	}
	return nil
}

// GetBaseline returns the recorded baseline for a session, or (nil, nil) when
// none has been recorded yet.
func (s *Store) GetBaseline(name string) (*model.Session, error) {
	row, err := s.q.BaselineByName(storeContext, name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get baseline: %w", err)
	}
	sess, err := decodeBaselineEnvelope(row.Body, row.LogicalID)
	if err != nil {
		return nil, fmt.Errorf("get baseline %q: %w", name, err)
	}
	if sess.Name != name {
		return nil, fmt.Errorf("get baseline: stored name disagrees for %q", name)
	}

	if err := normalizeBaselineLayouts(&sess); err != nil {
		return nil, fmt.Errorf("get baseline: invalid layout binding for %q: %w", name, err)
	}
	return &sess, nil
}

func (s *Store) GetBaselineByLogicalID(id string) (*model.Session, error) {
	if id == "" {
		return nil, nil
	}
	row, err := s.q.BaselineByLogicalID(storeContext, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get baseline by logical ID: %w", err)
	}
	sess, err := decodeBaselineEnvelope(row.Body, row.LogicalID)
	if err != nil {
		return nil, fmt.Errorf("get baseline by logical ID: %w", err)
	}
	if row.LogicalID != id {
		return nil, fmt.Errorf("get baseline: lookup identity disagrees")
	}

	if err := normalizeBaselineLayouts(&sess); err != nil {
		return nil, fmt.Errorf("get baseline by logical ID: invalid layout binding: %w", err)
	}
	return &sess, nil
}

func normalizeBaselineLayouts(sess *model.Session) error {
	if sess == nil {
		return nil
	}
	for wi := range sess.Windows {
		window := &sess.Windows[wi]
		bindings, err := tmuxlayout.PaneBindings(window.Layout)
		if err != nil {
			return fmt.Errorf("window %d: %w", wi, err)
		}
		if len(bindings) == 0 {
			continue
		}
		if len(bindings) != len(window.Panes) {
			return fmt.Errorf("window %d JSON layout has %d pane leaves for %d panes", wi, len(bindings), len(window.Panes))
		}
		paneIDsByIndex := make(map[int]string, len(window.Panes))
		paneIndexes := make(map[int]int, len(window.Panes))
		for _, pane := range window.Panes {
			if pane.SlotID == "" {
				return fmt.Errorf("window %d pane index %d has no stable slot ID", wi, pane.Idx)
			}
			if _, duplicate := paneIDsByIndex[pane.Idx]; duplicate {
				return fmt.Errorf("window %d has duplicate pane index %d", wi, pane.Idx)
			}
			paneIDsByIndex[pane.Idx] = pane.SlotID
			paneIndexes[pane.Idx] = pane.Idx
		}
		layout, err := tmuxlayout.RemapPaneBindingsByIndex(window.Layout, paneIDsByIndex, paneIndexes)
		if err != nil {
			return fmt.Errorf("window %d: %w", wi, err)
		}
		window.Layout = layout
	}
	return nil
}

// decodeBaselineEnvelope normalizes the persisted baseline envelope once for
// every repository path, including transactional recipe revision.
func decodeBaselineEnvelope(body, logicalID string) (model.Session, error) {
	var session model.Session
	if err := json.Unmarshal([]byte(body), &session); err != nil {
		return session, fmt.Errorf("corrupt baseline body: %w", err)
	}
	if session.LogicalID == "" {
		session.LogicalID = logicalID
	}
	if session.LogicalID != logicalID {
		return session, fmt.Errorf("baseline envelope/body identity mismatch")
	}
	return session, nil
}
