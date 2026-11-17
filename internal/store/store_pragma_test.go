package store

import "testing"

func TestOpenStorePragma(t *testing.T) {
	s := isolatedStore(t)
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode=%q want wal", mode)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_session_last_used'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("missing idx_session_last_used")
	}
}
