package store

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/model"
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
		Name: "zz-acid",
		Cwd:  "/tmp/zz-acid",
		Windows: []model.Window{
			{Name: "w", Panes: []model.Pane{{Cwd: "/tmp/zz-acid", Cmd: "true"}}},
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
	if err != nil || baseline == nil || baseline.SchemaVersion != 2 || baseline.ServerKey != p.ServerKey || baseline.Windows[0].TmuxID != "@17" || baseline.Windows[0].Panes[0].TmuxID != "%42" {
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

func TestGetBaselineReadsLegacySessionJSON(t *testing.T) {
	st := isolatedStore(t)
	legacy := `{"Name":"legacy","Cwd":"/tmp","Windows":[{"Idx":1,"Name":"shell","Panes":[{"Idx":0,"Cwd":"/tmp"}]}]}`
	if _, err := st.db.Exec(`INSERT INTO baseline(name,body,updated_at) VALUES(?,?,1)`, "legacy", legacy); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetBaseline("legacy")
	if err != nil || got == nil || got.Name != "legacy" || got.SchemaVersion != 0 || got.Windows[0].TmuxID != "" {
		t.Fatalf("legacy baseline = %+v, err=%v", got, err)
	}
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

// Regression for the 2026-08-26 freeze failure ("table pane has no column
// named cmd_path"): migrate() short-circuits on user_version == schemaVersion,
// so EVERY new statement in migrateAll requires a schemaVersion bump — a fresh
// test DB always runs the full migration and can never catch a forgotten bump.
// This stamps a legacy DB at the CURRENT version, reopens it, and demands the
// latest column: if you added a statement without bumping schemaVersion, this
// fails exactly like production did.
func TestMigrateRunsOnCurrentVersionStamp(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	// Pre-cmd_path pane schema (window.cwd present — that ship sailed long ago).
	if _, err := db.Exec(`
CREATE TABLE session (
  name       TEXT PRIMARY KEY,
  cwd        TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_used  INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE window (
  id      INTEGER PRIMARY KEY,
  session TEXT NOT NULL REFERENCES session(name) ON DELETE CASCADE,
  idx     INTEGER NOT NULL,
  name    TEXT NOT NULL DEFAULT '',
  cwd     TEXT NOT NULL DEFAULT '',
  layout  TEXT,
  UNIQUE(session, idx)
);
CREATE TABLE pane (
  id        INTEGER PRIMARY KEY,
  window_id INTEGER NOT NULL REFERENCES window(id) ON DELETE CASCADE,
  idx       INTEGER NOT NULL,
  cwd       TEXT,
  cmd       TEXT,
  UNIQUE(window_id, idx)
);
PRAGMA user_version = ` + strconv.Itoa(schemaVersion) + `;`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	st, err := OpenWithConfig(&config.Config{DataDir: dir}) // must migrate, not skip
	if err != nil {
		t.Fatal(err)
	}
	var uv int
	_ = st.db.QueryRow(`PRAGMA user_version`).Scan(&uv)
	t.Logf("DEBUG user_version=%d", uv)
	defer st.Close()

	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('pane') WHERE name='cmd_path'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("pane.cmd_path missing after reopen: a migrateAll statement was added without bumping schemaVersion")
	}
}
