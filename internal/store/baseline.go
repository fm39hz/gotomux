package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fm39hz/gotomux/internal/identity"
	"github.com/fm39hz/gotomux/internal/model"
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

func saveBaselineTx(tx *sql.Tx, sess *model.Session, allowSessionReplacement bool) error {
	if sess == nil {
		return fmt.Errorf("save baseline: nil session")
	}
	if sess.Name == "" {
		return fmt.Errorf("save baseline: empty session name")
	}
	copy := *sess
	if copy.SchemaVersion == 0 {
		copy.SchemaVersion = model.CurrentBaselineSchemaVersion
	}
	if err := validateBaselineIdentityTree(copy); err != nil {
		return fmt.Errorf("save baseline: %w", err)
	}
	body, err := json.Marshal(&copy)
	if err != nil {
		return fmt.Errorf("save baseline: %w", err)
	}
	var oldName string
	if copy.LogicalID != "" {
		err = tx.QueryRow(`SELECT name FROM baseline WHERE logical_id = ?`, copy.LogicalID).Scan(&oldName)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("save baseline lookup by logical ID: %w", err)
		}
	}
	if oldName != "" {
		if oldName != copy.Name {
			var occupyingID string
			err := tx.QueryRow(`SELECT logical_id FROM baseline WHERE name = ?`, copy.Name).Scan(&occupyingID)
			if err == nil {
				return fmt.Errorf("save baseline: name %q already belongs to another baseline", copy.Name)
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("save baseline name check: %w", err)
			}
		}
		_, err = tx.Exec(`UPDATE baseline SET name = ?, body = ?, updated_at = ?, logical_id = ? WHERE logical_id = ?`,
			copy.Name, body, time.Now().Unix(), copy.LogicalID, copy.LogicalID)
		if err != nil {
			return fmt.Errorf("save baseline: %w", err)
		}
		return nil
	}
	var existingID string
	err = tx.QueryRow(`SELECT logical_id FROM baseline WHERE name = ?`, copy.Name).Scan(&existingID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("save baseline lookup by name: %w", err)
	}
	if err == nil {
		if existingID != "" && existingID != copy.LogicalID && !allowSessionReplacement {
			return fmt.Errorf("save baseline: session name %q belongs to logical session %s; freeze the intended replacement before saving", copy.Name, existingID)
		}
		_, err = tx.Exec(`UPDATE baseline SET body = ?, updated_at = ?, logical_id = ? WHERE name = ?`, body, time.Now().Unix(), copy.LogicalID, copy.Name)
	} else {
		_, err = tx.Exec(`INSERT INTO baseline (name, body, updated_at, logical_id) VALUES (?, ?, ?, ?)`, copy.Name, body, time.Now().Unix(), copy.LogicalID)
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
	var body string
	err := s.db.QueryRow(`SELECT body FROM baseline WHERE name = ?`, name).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get baseline: %w", err)
	}
	var sess model.Session
	if err := json.Unmarshal([]byte(body), &sess); err != nil {
		return nil, fmt.Errorf("get baseline: corrupt baseline for %q: %w", name, err)
	}
	return &sess, nil
}

func (s *Store) GetBaselineByLogicalID(id string) (*model.Session, error) {
	if id == "" {
		return nil, nil
	}
	var body string
	err := s.db.QueryRow(`SELECT body FROM baseline WHERE logical_id = ?`, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get baseline by logical ID: %w", err)
	}
	var sess model.Session
	if err := json.Unmarshal([]byte(body), &sess); err != nil {
		return nil, fmt.Errorf("get baseline by logical ID: corrupt body: %w", err)
	}
	return &sess, nil
}
