package store

import (
	"context"
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/pressly/goose/v3"
)

func TestSaveOverwritesAliasAndCwd(t *testing.T) {
	s := isolatedStore(t)
	a := &model.Session{
		Name: "zztestalias",
		Cwd:  "/tmp/gotomux-save-test-root",
		Windows: []model.Window{
			{Name: "w", Panes: []model.Pane{{Cwd: "/tmp/gotomux-save-test-root", Cmd: "true"}}},
		},
	}
	if err := s.Save(a); err != nil {
		t.Fatal(err)
	}
	// legacy-style alias name, same cwd, more panes
	b := &model.Session{
		Name: "zz-test-alias", // different spelling, same alias key if we strip -
		Cwd:  "/tmp/gotomux-save-test-root",
		Windows: []model.Window{
			{Name: "w1", Panes: []model.Pane{{Cwd: "/tmp/gotomux-save-test-root"}}},
			{Name: "w2", Panes: []model.Pane{{Cwd: "/tmp/gotomux-save-test-root"}}},
		},
	}
	// force alias collision: zztestalias vs zztestalias after strip of zz-test-alias
	// sessionAliasKey("zz-test-alias") == "zztestalias"
	if sessionAliasKey("zz-test-alias") != sessionAliasKey("zztestalias") {
		t.Fatalf("alias keys %q %q", sessionAliasKey("zz-test-alias"), sessionAliasKey("zztestalias"))
	}
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}
	// old name must be gone
	if _, err := s.Get("zztestalias"); err == nil {
		t.Fatal("legacy alias row should be deleted")
	}
	got, err := s.Get("zz-test-alias")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Windows) != 2 {
		t.Fatalf("want 2 windows after overwrite, got %d", len(got.Windows))
	}
	_ = s.Delete("zz-test-alias")
}

func TestSessionAliasKey(t *testing.T) {
	if sessionAliasKey("tmux-project") != sessionAliasKey("tmuxproject") {
		t.Fatal("expected alias match")
	}
	if sessionAliasKey("tmux_project") == sessionAliasKey("tmux-project") {
		// _ already removed in key; both become tmuxproject
	}
	if sessionAliasKey("tmux_project") != "tmuxproject" {
		t.Fatalf("got %q", sessionAliasKey("tmux_project"))
	}
}

func TestSaveFreezeAtomic(t *testing.T) {
	st := isolatedStore(t)

	p := &model.Session{
		Name: "zz-acid", Cwd: "/tmp/zz-acid",
		LogicalID: "00000000-0000-4000-8000-000000000001",
		Windows: []model.Window{
			{Name: "w", SlotID: "00000000-0000-4000-8000-000000000002", Panes: []model.Pane{{Cwd: "/tmp/zz-acid", Cmd: "true", SlotID: "00000000-0000-4000-8000-000000000003"}}},
		},
	}
	// pure shape body minimal
	body := `{"name":"w","windows":[{"name":"w","panes":[{"cwd":""}]}]}`
	p.ServerKey = "socket:device:inode"
	p.Windows[0].TmuxID = "@17"
	p.Windows[0].Panes[0].TmuxID = "%42"
	sid, created, err := st.SaveFreezeWithBaseline(p, p, "w", "keyacid01", body, true)
	if err != nil || !created || sid == "" {
		t.Fatalf("savefreeze %q %v %v", sid, created, err)
	}
	if _, err := st.Get("zz-acid"); err != nil {
		t.Fatal("preset missing after commit")
	}
	if st.StickyID() != sid {
		t.Fatalf("sticky %q want %q", st.StickyID(), sid)
	}
	baseline, err := st.GetBaseline("zz-acid")
	if err != nil || baseline == nil || baseline.SchemaVersion != model.CurrentBaselineSchemaVersion || baseline.ServerKey != p.ServerKey || baseline.Windows[0].TmuxID != "@17" || baseline.Windows[0].Panes[0].TmuxID != "%42" {
		t.Fatalf("atomic baseline missing runtime identity: %+v, err=%v", baseline, err)
	}
	// same key again: no new shape, preset still updates
	p.Windows[0].Panes[0].Cmd = "false"
	sid2, created2, err := st.SaveFreeze(p, "w", "keyacid01", body, false)
	if err != nil || created2 || sid2 != sid {
		t.Fatalf("dedupe %q %v %v", sid2, created2, err)
	}
	got, _ := st.Get("zz-acid")
	if got.Windows[0].Panes[0].Cmd != "false" {
		t.Fatal("preset not updated")
	}
	_ = st.Delete("zz-acid")
}

func TestRebindNameMergesUsageAndPairs(t *testing.T) {
	st := isolatedStore(t)

	if err := st.RecordOpen("old-sess"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordOpen("old-sess"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordOpen("new-sess"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordPair("old-sess", "other"); err != nil {
		t.Fatal(err)
	}

	if err := st.RebindName("old-sess", "new-sess"); err != nil {
		t.Fatal(err)
	}

	us, err := st.AllUsage()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := us["old-sess"]; ok {
		t.Fatal("old usage should be gone")
	}
	u := us["new-sess"]
	if u.Opens != 3 { // 2 from old + 1 from new
		t.Fatalf("opens=%d want 3", u.Opens)
	}

	scores, err := st.PairScores("new-sess", 0)
	if err != nil {
		t.Fatal(err)
	}
	if scores["other"] <= 0 {
		t.Fatalf("pair should follow rename: %+v", scores)
	}
	// old endpoint gone
	scoresOld, _ := st.PairScores("old-sess", 0)
	if len(scoresOld) != 0 {
		t.Fatalf("old pair scores should be empty: %+v", scoresOld)
	}
}

// A database at schema version 7 predates baseline.logical_id. OpenWithConfig
// must run the additive migration at the actual resolved data path.
func TestMigrateVersion7AddsBaselineLogicalID(t *testing.T) {
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
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE goose_db_version;
INSERT INTO baseline(name, body, updated_at) VALUES ('old-session', '{"Name":"old-session"}', 1);
PRAGMA user_version = 7;`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	st, err := OpenWithConfig(&config.Config{DataDir: dir}) // resolves to dir/gotomux
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('pane') WHERE name='cmd_path'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("pane.cmd_path missing after opening a version-7 database")
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('baseline') WHERE name='logical_id'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("baseline.logical_id missing after opening a version-7 database")
	}
	var body, logicalID string
	if err := st.db.QueryRow(`SELECT body, logical_id FROM baseline WHERE name='old-session'`).Scan(&body, &logicalID); err != nil {
		t.Fatal(err)
	}
	if body != `{"Name":"old-session"}` || logicalID != "" {
		t.Fatalf("version-7 baseline was converted: body=%q logical_id=%q", body, logicalID)
	}
	var userVersion int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil || userVersion != schemaVersion {
		t.Fatalf("user_version=%d err=%v, want %d", userVersion, err, schemaVersion)
	}
}
