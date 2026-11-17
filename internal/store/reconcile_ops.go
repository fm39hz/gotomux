package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/store/dbgen"
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
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.q.WithTx(tx)
	// The desired document in the plan is its optimistic concurrency token.
	// Check it in the same transaction that claims the active journal, closing
	// the observation-to-journal gap without timestamps or runtime bindings.
	var envelope struct{ DesiredBaseline model.Baseline }
	if err := json.Unmarshal([]byte(journal.PlanJSON), &envelope); err != nil {
		return err
	}
	if envelope.DesiredBaseline.Desired.LogicalID != "" {
		row, err := q.BaselineByLogicalID(storeContext, journal.SessionLogicalID)
		if err != nil {
			return fmt.Errorf("claim reconcile baseline: %w", err)
		}
		current, err := decodeBaselineEnvelope(row.Body, row.LogicalID)
		if err != nil {
			return err
		}
		expected := envelope.DesiredBaseline.Desired
		actual := model.RecipeFromSession(current)
		// Session rename is a presentation change applied during planning.
		actual.Name = expected.Name
		if !expected.SameReplay(actual) || expected.LogicalID != actual.LogicalID {
			return fmt.Errorf("baseline changed after reconcile planning; retry from a fresh snapshot")
		}
		for wi, window := range expected.Windows {
			if window.SlotID != actual.Windows[wi].SlotID {
				return fmt.Errorf("baseline window identity changed after planning")
			}
			for pi, pane := range window.Panes {
				if pane.SlotID != actual.Windows[wi].Panes[pi].SlotID {
					return fmt.Errorf("baseline pane identity changed after planning")
				}
			}
		}
	}
	err = q.InsertReconcileJournal(storeContext, dbgen.InsertReconcileJournalParams{
		ID: journal.ID, SessionKey: journal.SessionKey, SessionName: journal.SessionName,
		SessionLogicalID: journal.SessionLogicalID, Policy: journal.Policy, Status: string(journal.Status),
		PlanJson: journal.PlanJSON, CompletedStep: int64(journal.CompletedStep), LastError: journal.LastError,
		CreatedAt: journal.CreatedAt, UpdatedAt: journal.UpdatedAt,
	})
	if err != nil {
		return fmt.Errorf("start reconcile journal: %w", err)
	}
	return tx.Commit()
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
	q := s.q.WithTx(tx)
	row, err := q.ReconcileStatusAndStep(storeContext, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("update reconcile journal: unknown operation %q", id)
		}
		return err
	}
	if completedStep < int(row.CompletedStep) {
		return fmt.Errorf("update reconcile journal: completed step cannot move backwards")
	}
	old := model.ReconcileJournalStatus(row.Status)
	if !validJournalTransition(old, status) {
		return fmt.Errorf("update reconcile journal: invalid transition %s -> %s", old, status)
	}
	if err := q.UpdateReconcileJournal(storeContext, dbgen.UpdateReconcileJournalParams{Status: string(status), CompletedStep: int64(completedStep), LastError: lastError, UpdatedAt: time.Now().Unix(), ID: id}); err != nil {
		return fmt.Errorf("update reconcile journal: %w", err)
	}
	return tx.Commit()
}

func (s *Store) GetReconcile(id string) (*model.ReconcileJournal, error) {
	if id == "" {
		return nil, nil
	}
	row, err := s.q.ReconcileJournalByID(storeContext, id)
	return reconcileFromDB(row, err)
}

func (s *Store) GetActiveReconcile(sessionKey string) (*model.ReconcileJournal, error) {
	if sessionKey == "" {
		return nil, nil
	}
	row, err := s.q.ActiveReconcileBySessionKey(storeContext, sessionKey)
	return reconcileFromDB(dbgen.ReconcileJournal(row), err)
}

func (s *Store) ListActiveReconciles() ([]model.ReconcileJournal, error) {
	rows, err := s.q.ActiveReconciles(storeContext)
	if err != nil {
		return nil, err
	}
	journals := make([]model.ReconcileJournal, 0, len(rows))
	for _, row := range rows {
		journals = append(journals, *toReconcile(row))
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
	q := s.q.WithTx(tx)
	row, err := q.ReconcileIdentityAndStatus(storeContext, journalID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("commit reconcile: unknown journal %q", journalID)
		}
		return err
	}
	if model.ReconcileJournalStatus(row.Status) != model.JournalRunning {
		return fmt.Errorf("commit reconcile: journal %q is %s, expected running", journalID, row.Status)
	}
	if row.SessionLogicalID == "" || sess.LogicalID != row.SessionLogicalID {
		return fmt.Errorf("commit reconcile: baseline logical session ID does not match journal")
	}
	if row.SessionKey != row.SessionLogicalID {
		return fmt.Errorf("commit reconcile: journal session key does not match its logical identity")
	}
	if err := saveBaselineTx(tx, sess, false); err != nil {
		return err
	}
	if err := q.CompleteReconcileJournal(storeContext, dbgen.CompleteReconcileJournalParams{Status: string(model.JournalComplete), UpdatedAt: time.Now().Unix(), ID: journalID, Status_2: string(model.JournalRunning)}); err != nil {
		return fmt.Errorf("complete reconcile journal: %w", err)
	}
	return tx.Commit()
}

func (s *Store) CommitReconcileState(state model.Baseline, journalID string) error {
	session := state.Session()
	return s.CommitReconcile(&session, journalID)
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

func reconcileFromDB(row dbgen.ReconcileJournal, err error) (*model.ReconcileJournal, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toReconcile(row), nil
}

func toReconcile(row dbgen.ReconcileJournal) *model.ReconcileJournal {
	return &model.ReconcileJournal{
		ID: row.ID, SessionKey: row.SessionKey, SessionName: row.SessionName,
		SessionLogicalID: row.SessionLogicalID, Policy: row.Policy,
		Status: model.ReconcileJournalStatus(row.Status), PlanJSON: row.PlanJson,
		CompletedStep: int(row.CompletedStep), LastError: row.LastError,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
