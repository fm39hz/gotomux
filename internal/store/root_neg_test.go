package store

import (
	"testing"
	"time"

	"github.com/fm39hz/gotomux/internal/config"
)

func TestRootNegSaveLoad(t *testing.T) {
	s := isolatedStore(t)
	if _, ok := s.LoadRootNeg("/nope"); ok {
		t.Fatal("empty table must report a miss")
	}
	if err := s.SaveRootNeg("/home/user"); err != nil {
		t.Fatalf("SaveRootNeg: %v", err)
	}
	checked, ok := s.LoadRootNeg("/home/user")
	if !ok {
		t.Fatal("freshly saved row must hit")
	}
	if time.Since(time.Unix(checked, 0)) > time.Minute {
		t.Fatalf("checked = %d, want ~now", checked)
	}
}

func TestRootNegExpiresAfterTTL(t *testing.T) {
	s := isolatedStore(t)
	if err := s.SaveRootNeg("/home/user"); err != nil {
		t.Fatalf("SaveRootNeg: %v", err)
	}
	// Backdate past RootNegTTL, same trick as agePlacements.
	old := time.Now().Add(-(RootNegTTL + time.Minute)).Unix()
	if _, err := s.db.Exec(`UPDATE root_neg SET checked = ?`, old); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if _, ok := s.LoadRootNeg("/home/user"); ok {
		t.Fatal("row older than RootNegTTL must read as a miss")
	}
}

func TestRootNegForgetRoot(t *testing.T) {
	s := isolatedStore(t)
	if err := s.SaveRootNeg("/home/user"); err != nil {
		t.Fatalf("SaveRootNeg: %v", err)
	}
	if err := s.ForgetRootNeg("/home/user"); err != nil {
		t.Fatalf("ForgetRootNeg: %v", err)
	}
	if _, ok := s.LoadRootNeg("/home/user"); ok {
		t.Fatal("forgotten row must read as a miss")
	}
	// Forgetting an absent row is a no-op, not an error.
	if err := s.ForgetRootNeg("/home/user"); err != nil {
		t.Fatalf("repeated forget: %v", err)
	}
}

func TestMigrateReopensVersion5DB(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir}

	s, err := OpenWithConfig(cfg)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := s.SaveRootNeg("/home/user"); err != nil {
		t.Fatalf("SaveRootNeg: %v", err)
	}
	// Pretend this DB predates schema 6; the reopen below must re-run
	// migrateAll idempotently instead of erroring on existing tables.
	if _, err := s.db.Exec(`PRAGMA user_version = 5`); err != nil {
		t.Fatalf("downgrade user_version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := OpenWithConfig(cfg)
	if err != nil {
		t.Fatalf("reopen at version 5: %v", err)
	}
	defer s2.Close()
	var v int
	if err := s2.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != schemaVersion {
		t.Fatalf("user_version after reopen = %d (%v), want %d", v, err, schemaVersion)
	}
	if err := s2.SaveRootNeg("/opt/work"); err != nil {
		t.Fatalf("SaveRootNeg after remigration: %v", err)
	}
	if _, ok := s2.LoadRootNeg("/opt/work"); !ok {
		t.Fatal("root_neg unusable after remigration")
	}
	// Rows written before the downgrade survive an additive migration.
	if _, ok := s2.LoadRootNeg("/home/user"); !ok {
		t.Fatal("pre-existing root_neg row lost on remigration")
	}
}
