package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/model"
)

func identifiedBaseline(name string, seed int) *model.Session {
	id := func(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012x", n) }
	return &model.Session{Name: name, LogicalID: id(seed), TmuxID: "$1", ServerKey: "server-a", Cwd: "/work", Windows: []model.Window{{Idx: 1, Name: "editor", SlotID: id(seed + 1), TmuxID: "@1", Panes: []model.Pane{{Idx: 0, Cmd: "nvim", SlotID: id(seed + 2), TmuxID: "%1", Cwd: "/work"}}}}}
}

func TestBaselineIdentityIDLookupSurvivesSessionRename(t *testing.T) {
	st := isolatedStore(t)
	session := identifiedBaseline("before", 1)
	if err := st.SaveBaseline(session); err != nil {
		t.Fatalf("save initial baseline: %v", err)
	}
	rename := *session
	rename.Name = "after"
	rename.TmuxID = "$9"
	rename.ServerKey = "server-b"
	if err := st.SaveBaseline(&rename); err != nil {
		t.Fatalf("save renamed baseline: %v", err)
	}
	if old, err := st.GetBaseline("before"); err != nil || old != nil {
		t.Fatalf("old-name lookup = %+v, %v; want nil", old, err)
	}
	got, err := st.GetBaselineByLogicalID(session.LogicalID)
	if err != nil || got == nil || got.Name != "after" || got.TmuxID != "$9" {
		t.Fatalf("logical-ID lookup after rename = %+v, %v", got, err)
	}
}

func TestBaselineNameCollisionWithDifferentLogicalSessionIsRejected(t *testing.T) {
	st := isolatedStore(t)
	first := identifiedBaseline("same", 11)
	second := identifiedBaseline("same", 21)
	if err := st.SaveBaseline(first); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveBaseline(second); err == nil {
		t.Fatal("different logical session silently replaced a baseline with the same name")
	}
}

func TestSaveBaselineRejectsIncompleteIdentityTree(t *testing.T) {
	st := isolatedStore(t)
	for _, session := range []*model.Session{
		{Name: "missing-session-id", Windows: []model.Window{{SlotID: "00000000-0000-4000-8000-000000000002", Panes: []model.Pane{{SlotID: "00000000-0000-4000-8000-000000000003"}}}}},
		{Name: "missing-window-slot", LogicalID: "00000000-0000-4000-8000-000000000001", Windows: []model.Window{{Panes: []model.Pane{{SlotID: "00000000-0000-4000-8000-000000000003"}}}}},
		{Name: "missing-pane-slot", LogicalID: "00000000-0000-4000-8000-000000000001", Windows: []model.Window{{SlotID: "00000000-0000-4000-8000-000000000002", Panes: []model.Pane{{}}}}},
	} {
		if err := st.SaveBaseline(session); err == nil {
			t.Errorf("incomplete baseline %q was persisted", session.Name)
		}
	}
}

func TestExplicitFreezeCanRebindSameNameToNewLogicalSession(t *testing.T) {
	st := isolatedStore(t)
	old := identifiedBaseline("reused", 31)
	if err := st.SaveBaseline(old); err != nil {
		t.Fatal(err)
	}
	newSession := identifiedBaseline("reused", 41)
	if _, _, err := st.SaveFreezeWithBaseline(newSession, newSession, "shape-new", "shape-key", `{}`, false); err != nil {
		t.Fatalf("explicit freeze baseline rebind: %v", err)
	}
	if got, _ := st.GetBaselineByLogicalID(old.LogicalID); got != nil {
		t.Fatalf("old logical baseline remained after explicit freeze rebind: %+v", got)
	}
	if got, _ := st.GetBaselineByLogicalID(newSession.LogicalID); got == nil || got.Name != newSession.Name {
		t.Fatalf("new logical baseline missing after explicit freeze rebind: %+v", got)
	}
}

func TestReconcileJournalSerializesSessionAndCommitsBaselineAtomically(t *testing.T) {
	st := isolatedStore(t)
	session := identifiedBaseline("journal", 51)
	if err := st.SaveBaseline(session); err != nil {
		t.Fatal(err)
	}
	journal := model.ReconcileJournal{ID: "run-1", SessionKey: session.LogicalID, SessionName: session.Name, SessionLogicalID: session.LogicalID, Policy: "preserve", Status: model.JournalPlanned, PlanJSON: `{"ops":[]}`}
	if err := st.StartReconcile(journal); err != nil {
		t.Fatalf("start journal: %v", err)
	}
	journal.ID = "run-2"
	if err := st.StartReconcile(journal); err == nil {
		t.Fatal("concurrent active reconciliation for the same session was accepted")
	}
	if err := st.UpdateReconcile("run-1", model.JournalRunning, 1, ""); err != nil {
		t.Fatal(err)
	}
	updated := *session
	updated.ServerKey = "server-b"
	updated.TmuxID = "$9"
	if err := st.CommitReconcile(&updated, "run-1"); err != nil {
		t.Fatalf("commit baseline+journal: %v", err)
	}
	got, err := st.GetBaselineByLogicalID(session.LogicalID)
	if err != nil || got == nil || got.ServerKey != "server-b" {
		t.Fatalf("committed baseline = %+v, %v", got, err)
	}
	completed, err := st.GetReconcile("run-1")
	if err != nil || completed == nil || completed.Status != model.JournalComplete {
		t.Fatalf("completed journal = %+v, %v", completed, err)
	}
}

func TestCommitReconcileRollbackLeavesBaselineAndJournalUnchanged(t *testing.T) {
	st := isolatedStore(t)
	session := identifiedBaseline("rollback", 61)
	if err := st.SaveBaseline(session); err != nil {
		t.Fatal(err)
	}
	journal := model.ReconcileJournal{ID: "run-rollback", SessionKey: session.LogicalID, SessionName: session.Name, SessionLogicalID: session.LogicalID, Policy: "preserve", Status: model.JournalPlanned, PlanJSON: `{}`}
	if err := st.StartReconcile(journal); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateReconcile(journal.ID, model.JournalRunning, 0, ""); err != nil {
		t.Fatal(err)
	}
	updated := *session
	updated.ServerKey = "server-b"
	if err := st.CommitReconcile(&updated, "missing-journal"); err == nil {
		t.Fatal("commit with unknown journal unexpectedly succeeded")
	}
	got, _ := st.GetBaselineByLogicalID(session.LogicalID)
	if got == nil || got.ServerKey != "server-a" {
		t.Fatalf("baseline changed despite failed transaction: %+v", got)
	}
	active, err := st.GetActiveReconcile(session.LogicalID)
	if err != nil || active == nil || active.Status != model.JournalRunning {
		t.Fatalf("journal changed despite failed transaction: %+v, %v", active, err)
	}
}

func TestReconcileJournalNeedsAttentionRemainsExclusiveUntilResolved(t *testing.T) {
	st := isolatedStore(t)
	sessionID := "00000000-0000-4000-8000-000000000071"
	base := model.ReconcileJournal{ID: "run-a", SessionKey: sessionID, SessionName: "attention", SessionLogicalID: sessionID, Policy: "preserve", Status: model.JournalPlanned, PlanJSON: `{}`}
	if err := st.StartReconcile(base); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateReconcile(base.ID, model.JournalNeedsAttention, 2, "outcome uncertain"); err != nil {
		t.Fatal(err)
	}
	active, err := st.GetActiveReconcile(base.SessionKey)
	if err != nil || active == nil || active.Status != model.JournalNeedsAttention || active.CompletedStep != 2 {
		t.Fatalf("active attention journal = %+v, %v", active, err)
	}
	second := base
	second.ID = "run-b"
	if err := st.StartReconcile(second); err == nil {
		t.Fatal("needs_attention journal did not retain the session claim")
	}
	if err := st.UpdateReconcile(base.ID, model.JournalRunning, 1, ""); err == nil {
		t.Fatal("journal accepted a backwards completed-step count")
	}
	if err := st.UpdateReconcile(base.ID, model.JournalAborted, 2, "operator resolved"); err != nil {
		t.Fatal(err)
	}
	active, err = st.GetActiveReconcile(base.SessionKey)
	if err != nil || active != nil {
		t.Fatalf("aborted journal still active = %+v, %v", active, err)
	}
}

func TestBaselineTableMigrationAddsIdentityColumnWithoutDataConversion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	storeDir := filepath.Join(dir, "gotomux")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(storeDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE baseline (name TEXT PRIMARY KEY, body TEXT NOT NULL, updated_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := OpenWithConfig(&config.Config{DataDir: dir})
	if err != nil {
		t.Fatalf("open migrated DB: %v", err)
	}
	defer st.Close()
	var columnCount int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('baseline') WHERE name='logical_id'`).Scan(&columnCount); err != nil || columnCount != 1 {
		t.Fatalf("logical_id migration column count=%d err=%v", columnCount, err)
	}
	var rowCount int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM baseline`).Scan(&rowCount); err != nil || rowCount != 0 {
		t.Fatalf("baseline migration altered data: rows=%d err=%v", rowCount, err)
	}
}
