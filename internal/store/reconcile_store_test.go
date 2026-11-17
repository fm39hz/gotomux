package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/pressly/goose/v3"
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

func TestRecipeRevisionUpdatesDesiredAndKeepsOnlyUnchangedBindings(t *testing.T) {
	st := isolatedStore(t)
	session := identifiedBaseline("revision-contract", 110)
	session.Windows = append(session.Windows, model.Window{Idx: 2, Name: "shell", SlotID: "00000000-0000-4000-8000-000000000115", TmuxID: "@2", Panes: []model.Pane{{Idx: 0, SlotID: "00000000-0000-4000-8000-000000000116", TmuxID: "%2", Cmd: "sh"}}})
	if err := st.Save(session); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveInitialBaseline(session); err != nil {
		t.Fatal(err)
	}
	if err := st.StartReconcile(model.ReconcileJournal{ID: "edit-old-plan", SessionKey: session.LogicalID, SessionLogicalID: session.LogicalID, SessionName: session.Name, Policy: "preserve", Status: model.JournalPlanned, PlanJSON: `{}`}); err != nil {
		t.Fatal(err)
	}
	recipe := model.RecipeFromSession(*session)
	recipe.Windows[1].Panes[0].Cmd = "fish"
	recipe.Windows[1].SlotID = "00000000-0000-4000-8000-000000000117"
	recipe.Windows[1].Panes[0].SlotID = "00000000-0000-4000-8000-000000000118"
	if err := st.SaveRecipeRevision(recipe, ""); err != nil {
		t.Fatal(err)
	}
	preset, err := st.Get(recipe.Name)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := st.GetBaselineByLogicalID(recipe.LogicalID)
	if err != nil {
		t.Fatal(err)
	}
	if preset.Windows[1].Panes[0].Cmd != "fish" || baseline.Windows[1].Panes[0].Cmd != "fish" {
		t.Fatal("edited recipe and desired baseline diverged")
	}
	if baseline.Windows[0].TmuxID != "@1" || baseline.Windows[0].Panes[0].TmuxID != "%1" {
		t.Fatal("unchanged slot bindings were not retained")
	}
	if baseline.Windows[1].TmuxID != "" || baseline.Windows[1].Panes[0].TmuxID != "" {
		t.Fatal("changed slots retained stale runtime bindings")
	}
	active, err := st.GetActiveReconcile(recipe.LogicalID)
	if err != nil || active != nil {
		t.Fatalf("recipe edit left a stale plan active: %+v %v", active, err)
	}
}

func TestBaselineTableMigrationAddsIdentityColumnWithoutDataConversion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	storeDir := filepath.Join(dir, "gotomux")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(storeDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migrationFS, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrationFS,
		goose.WithDisableGlobalRegistry(true), goose.WithLogger(goose.NopLogger()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(context.Background(), 7); err != nil {
		t.Fatalf("create version-7 schema: %v", err)
	}
	const baselineBody = `{"Name":"old-session"}`
	if _, err := db.Exec(`INSERT INTO baseline(name, body, updated_at) VALUES ('old-session', ?, 1)`, baselineBody); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 7`); err != nil {
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
	var body, logicalID string
	if err := st.db.QueryRow(`SELECT body, logical_id FROM baseline WHERE name='old-session'`).Scan(&body, &logicalID); err != nil {
		t.Fatalf("read migrated baseline: %v", err)
	}
	if body != baselineBody || logicalID != "" {
		t.Fatalf("baseline migration converted existing data: body=%q logical_id=%q", body, logicalID)
	}
}

func TestExplicitFreezeAtomicallySupersedesOldJournal(t *testing.T) {
	st := isolatedStore(t)
	baseline := identifiedBaseline("freeze-supersedes", 90)
	if err := st.SaveBaseline(baseline); err != nil {
		t.Fatal(err)
	}
	journal := model.ReconcileJournal{ID: "superseded", SessionKey: baseline.LogicalID, SessionLogicalID: baseline.LogicalID, SessionName: baseline.Name, Policy: "preserve", PlanJSON: `{}`}
	if err := st.StartReconcile(journal); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SaveFreezeWithBaseline(baseline, baseline, "shape-freeze", "freeze-key", `{}`, false); err != nil {
		t.Fatal(err)
	}
	active, err := st.GetActiveReconcile(baseline.LogicalID)
	if err != nil || active != nil {
		t.Fatalf("freeze left old active journal: %+v %v", active, err)
	}
	old, err := st.GetReconcile(journal.ID)
	if err != nil || old.Status != model.JournalAborted {
		t.Fatalf("superseded journal not archived: %+v %v", old, err)
	}
}

func TestFreezeRejectsConflictingIdentityWithoutReplacingRecipe(t *testing.T) {
	st := isolatedStore(t)
	original := identifiedBaseline("freeze-contract", 301)
	if err := st.Save(original); err != nil {
		t.Fatal(err)
	}
	conflicting := identifiedBaseline(original.Name, 401)
	if _, _, err := st.SaveFreezeWithBaseline(original, conflicting, "shape-test", "test-key", `{}`, false); err == nil {
		t.Fatal("freeze accepted different logical identity trees")
	}
	got, err := st.Get(original.Name)
	if err != nil || got.LogicalID != original.LogicalID {
		t.Fatalf("rejected freeze changed recipe: %v %v", got, err)
	}
}

func TestFreezeRejectsDifferentReplayForSameIdentity(t *testing.T) {
	st := isolatedStore(t)
	recipe := identifiedBaseline("freeze-replay", 501)
	baseline := model.RecipeFromSession(*recipe).Session()
	baseline.Windows[0].Panes[0].StartCmd = "different --command"
	if _, _, err := st.SaveFreezeWithBaseline(recipe, &baseline, "shape-test", "test-key", `{}`, false); err == nil {
		t.Fatal("freeze accepted inconsistent desired replay under the same identity")
	}
	if _, err := st.Get(recipe.Name); err == nil {
		t.Fatal("rejected freeze persisted recipe")
	}
}

func TestNormalizeRecipeIdentityRejectsUnpersistedBaseline(t *testing.T) {
	st := isolatedStore(t)
	baseline := identifiedBaseline("normalize-canonical", 601)
	recipe := model.RecipeFromSession(*baseline)
	recipe.LogicalID = ""
	for wi := range recipe.Windows {
		recipe.Windows[wi].SlotID = ""
		for pi := range recipe.Windows[wi].Panes {
			recipe.Windows[wi].Panes[pi].SlotID = ""
		}
	}
	if err := st.SaveRecipe(recipe); err != nil {
		t.Fatal(err)
	}
	if _, err := st.NormalizeRecipeIdentity(recipe, model.RecipeFromSession(*baseline)); err == nil {
		t.Fatal("normalization accepted an unpersisted baseline")
	}
	if err := st.SaveBaseline(baseline); err != nil {
		t.Fatal(err)
	}
	stale := model.RecipeFromSession(*baseline)
	stale.Windows[0].Panes[0].StartCmd = "unpersisted command"
	if _, err := st.NormalizeRecipeIdentity(recipe, stale); err == nil {
		t.Fatal("normalization accepted stale baseline replay")
	}
	saved, err := st.GetRecipe(recipe.Name)
	if err != nil || saved.LogicalID != "" {
		t.Fatalf("rejected normalization changed ownership: %+v %v", saved, err)
	}
}

func TestJournalClaimRejectsDesiredStateChangedAfterPlanning(t *testing.T) {
	st := isolatedStore(t)
	original := identifiedBaseline("stale-plan", 701)
	if err := st.SaveBaseline(original); err != nil {
		t.Fatal(err)
	}
	planned := model.BaselineFromSession(*original)
	replacement := model.BaselineFromSession(*original).Session()
	replacement.Windows[0].Panes[0].StartCmd = "new-command"
	if err := st.SaveBaseline(&replacement); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(struct{ DesiredBaseline model.Baseline }{planned})
	if err != nil {
		t.Fatal(err)
	}
	err = st.StartReconcile(model.ReconcileJournal{ID: "stale", SessionKey: original.LogicalID, SessionLogicalID: original.LogicalID, SessionName: original.Name, Policy: "preserve", PlanJSON: string(body)})
	if err == nil {
		t.Fatal("stale desired document claimed an active journal")
	}
	active, err := st.GetActiveReconcile(original.LogicalID)
	if err != nil || active != nil {
		t.Fatalf("rejected claim persisted: %+v %v", active, err)
	}
}

func TestCheckedRecipeRevisionRejectsConcurrentEdit(t *testing.T) {
	st := isolatedStore(t)
	original := identifiedBaseline("concurrent-edit", 801)
	if err := st.Save(original); err != nil {
		t.Fatal(err)
	}
	expected := model.RecipeFromSession(*original)
	first := model.RecipeFromSession(*original)
	first.Windows[0].Panes[0].StartCmd = "first"
	if err := st.SaveRecipeRevisionChecked(first, expected); err != nil {
		t.Fatal(err)
	}
	second := model.RecipeFromSession(*original)
	second.Windows[0].Panes[0].StartCmd = "second"
	if err := st.SaveRecipeRevisionChecked(second, expected); err == nil {
		t.Fatal("stale editor overwrote concurrent revision")
	}
	got, err := st.GetRecipe(original.Name)
	if err != nil || got.Windows[0].Panes[0].StartCmd != "first" {
		t.Fatalf("first revision lost: %+v %v", got, err)
	}
}
