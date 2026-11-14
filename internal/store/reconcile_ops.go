package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fm39hz/gotomux/internal/model"
)

func (s *Store) StartReconcile(journal model.ReconcileJournal) error {
	if journal.ID == "" || journal.SessionKey == "" || journal.SessionName == "" || journal.SessionLogicalID == "" || journal.SessionKey != journal.SessionLogicalID || journal.Policy == "" {
		return fmt.Errorf("start reconcile journal: missing or inconsistent session identity, ID, name, or policy")
	}
	if !json.Valid([]byte(journal.PlanJSON)) {
		return fmt.Errorf("start reconcile journal: plan is not valid JSON")
	}
	if journal.Status == "" {
		journal.Status = model.JournalPlanned
	}
	if journal.Status != model.JournalPlanned {
		return fmt.Errorf("start reconcile journal: initial status must be planned")
	}
	now := time.Now().Unix()
	if journal.CreatedAt == 0 {
		journal.CreatedAt = now
	}
	journal.UpdatedAt = now
	_, err := s.db.Exec(`INSERT INTO reconcile_journal
		(id, session_key, session_name, session_logical_id, policy, status, plan_json, completed_step, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		journal.ID, journal.SessionKey, journal.SessionName, journal.SessionLogicalID, journal.Policy, journal.Status, journal.PlanJSON, journal.CompletedStep, journal.LastError, journal.CreatedAt, journal.UpdatedAt)
	if err != nil {
		return fmt.Errorf("start reconcile journal: %w", err)
	}
	return nil
}

func (s *Store) UpdateReconcile(id string, status model.ReconcileJournalStatus, completedStep int, lastError string) error {
	if id == "" || completedStep < 0 {
		return fmt.Errorf("update reconcile journal: invalid ID or step")
	}
	if status != model.JournalRunning && status != model.JournalNeedsAttention && status != model.JournalAborted {
		return fmt.Errorf("update reconcile journal: invalid transition target %q", status)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var old model.ReconcileJournalStatus
	var oldStep int
	if err := tx.QueryRow(`SELECT status, completed_step FROM reconcile_journal WHERE id = ?`, id).Scan(&old, &oldStep); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("update reconcile journal: unknown operation %q", id)
		}
		return err
	}
	if completedStep < oldStep {
		return fmt.Errorf("update reconcile journal: completed step cannot move backwards")
	}
	if !validJournalTransition(old, status) {
		return fmt.Errorf("update reconcile journal: invalid transition %s -> %s", old, status)
	}
	if _, err := tx.Exec(`UPDATE reconcile_journal SET status = ?, completed_step = ?, last_error = ?, updated_at = ? WHERE id = ?`, status, completedStep, lastError, time.Now().Unix(), id); err != nil {
		return fmt.Errorf("update reconcile journal: %w", err)
	}
	return tx.Commit()
}

func (s *Store) GetReconcile(id string) (*model.ReconcileJournal, error) {
	if id == "" {
		return nil, nil
	}
	return scanReconcile(s.db.QueryRow(`SELECT id, session_key, session_name, session_logical_id, policy, status, plan_json, completed_step, last_error, created_at, updated_at FROM reconcile_journal WHERE id = ?`, id))
}

func (s *Store) GetActiveReconcile(sessionKey string) (*model.ReconcileJournal, error) {
	if sessionKey == "" {
		return nil, nil
	}
	return scanReconcile(s.db.QueryRow(`SELECT id, session_key, session_name, session_logical_id, policy, status, plan_json, completed_step, last_error, created_at, updated_at FROM reconcile_journal WHERE session_key = ? AND status IN ('planned','running','needs_attention')`, sessionKey))
}

func (s *Store) ListActiveReconciles() ([]model.ReconcileJournal, error) {
	rows, err := s.db.Query(`SELECT id, session_key, session_name, session_logical_id, policy, status, plan_json, completed_step, last_error, created_at, updated_at FROM reconcile_journal WHERE status IN ('planned','running','needs_attention') ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var journals []model.ReconcileJournal
	for rows.Next() {
		var j model.ReconcileJournal
		if err := rows.Scan(&j.ID, &j.SessionKey, &j.SessionName, &j.SessionLogicalID, &j.Policy, &j.Status, &j.PlanJSON, &j.CompletedStep, &j.LastError, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		journals = append(journals, j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return journals, nil
}

func (s *Store) CommitReconcile(sess *model.Session, journalID string) error {
	if sess == nil || journalID == "" {
		return fmt.Errorf("commit reconcile: missing baseline or journal ID")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var sessionKey, sessionLogicalID string
	var status model.ReconcileJournalStatus
	if err := tx.QueryRow(`SELECT session_key, session_logical_id, status FROM reconcile_journal WHERE id = ?`, journalID).Scan(&sessionKey, &sessionLogicalID, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("commit reconcile: unknown journal %q", journalID)
		}
		return err
	}
	if status != model.JournalRunning {
		return fmt.Errorf("commit reconcile: journal %q is %s, expected running", journalID, status)
	}
	if sessionLogicalID == "" || sess.LogicalID != sessionLogicalID {
		return fmt.Errorf("commit reconcile: baseline logical session ID does not match journal")
	}
	if sessionKey != sessionLogicalID {
		return fmt.Errorf("commit reconcile: journal session key does not match its logical identity")
	}
	if err := saveBaselineTx(tx, sess, false); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE reconcile_journal SET status = ?, last_error = '', updated_at = ? WHERE id = ? AND status = ?`, model.JournalComplete, time.Now().Unix(), journalID, model.JournalRunning); err != nil {
		return fmt.Errorf("complete reconcile journal: %w", err)
	}
	return tx.Commit()
}

func validJournalTransition(old, next model.ReconcileJournalStatus) bool {
	switch old {
	case model.JournalPlanned:
		return next == model.JournalRunning || next == model.JournalNeedsAttention || next == model.JournalAborted
	case model.JournalRunning:
		return next == model.JournalRunning || next == model.JournalNeedsAttention || next == model.JournalAborted
	case model.JournalNeedsAttention:
		return next == model.JournalNeedsAttention || next == model.JournalRunning || next == model.JournalAborted
	default:
		return false
	}
}

func scanReconcile(row interface{ Scan(...any) error }) (*model.ReconcileJournal, error) {
	var journal model.ReconcileJournal
	err := row.Scan(&journal.ID, &journal.SessionKey, &journal.SessionName, &journal.SessionLogicalID, &journal.Policy, &journal.Status, &journal.PlanJSON, &journal.CompletedStep, &journal.LastError, &journal.CreatedAt, &journal.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &journal, nil
}
