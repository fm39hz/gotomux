package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fm39hz/gotomux/internal/model"
)

// Baseline: the recorded starting layout of a live session, stored as a full
// instance preset (windows / panes / commands / cwds). RestoreSession diffs the
// live session against it and rebuilds missing windows at their baseline
// positions. JSON keeps the whole tree readable in one row — baseline is always
// read wholesale, never queried by parts.
//
// Written when a session is first created (ConnectProject), when a preset is
// loaded, on every freeze, and after a successful restore.
func (s *Store) SaveBaseline(sess *model.Session) error {
	if sess == nil {
		return fmt.Errorf("save baseline: nil session")
	}
	body, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("save baseline: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO baseline (name, body, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET body = excluded.body, updated_at = excluded.updated_at`,
		sess.Name, body, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("save baseline: %w", err)
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
