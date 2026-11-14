package template

import (
	"path/filepath"
	"testing"

	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/store"
)

func TestWindowForkKeyStableAcrossProjects(t *testing.T) {
	a := model.Window{
		Name: "editor", Cwd: "/work/a",
		Panes: []model.Pane{{Cwd: "/work/a", Cmd: "nvim"}},
	}
	b := model.Window{
		Name: "code", Cwd: "/other/b",
		Panes: []model.Pane{{Cwd: "/other/b/src", Cmd: "nvim"}},
	}
	if WindowForkKey(a) != WindowForkKey(b) {
		t.Fatalf("nvimx1 must be one fork: %s vs %s", WindowForkKey(a), WindowForkKey(b))
	}
	shell := model.Window{
		Layout: "even-vertical",
		Panes:  []model.Pane{{}, {}},
	}
	if WindowForkKey(a) == WindowForkKey(shell) {
		t.Fatal("editor != shell-v2")
	}
}

func TestObserveForksLearnsFromFreeze(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	st, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	p := &model.Session{
		Name: "proj", LogicalID: "00000000-0000-4000-8000-000000000001", SchemaVersion: model.CurrentBaselineSchemaVersion, Cwd: "/work/x",
		Windows: []model.Window{
			{SlotID: "00000000-0000-4000-8000-000000000002", Name: "editor", Panes: []model.Pane{{SlotID: "00000000-0000-4000-8000-000000000003", Cmd: "nvim"}}},
			{SlotID: "00000000-0000-4000-8000-000000000004", Name: "shell", Layout: "4080,158x35,0,0[1,2]", Panes: []model.Pane{{SlotID: "00000000-0000-4000-8000-000000000005"}, {SlotID: "00000000-0000-4000-8000-000000000006"}}},
			{SlotID: "00000000-0000-4000-8000-000000000007", Name: "yazi", Panes: []model.Pane{{SlotID: "00000000-0000-4000-8000-000000000008", Cmd: "yazi"}}},
		},
	}
	// two freezes -> hit counts on the multi-window fork
	if _, _, err := FreezeSave(st, p, false); err != nil {
		t.Fatal(err)
	}
	p2 := &model.Session{
		Name: "other", LogicalID: "00000000-0000-4000-8000-000000000009", SchemaVersion: model.CurrentBaselineSchemaVersion, Cwd: "/work/y",
		Windows: []model.Window{
			{SlotID: "00000000-0000-4000-8000-000000000010", Name: "ed", Panes: []model.Pane{{SlotID: "00000000-0000-4000-8000-000000000011", Cmd: "nvim"}}},
			{SlotID: "00000000-0000-4000-8000-000000000012", Name: "sh", Layout: "even-vertical", Panes: []model.Pane{{SlotID: "00000000-0000-4000-8000-000000000013"}, {SlotID: "00000000-0000-4000-8000-000000000014"}}},
			{SlotID: "00000000-0000-4000-8000-000000000015", Name: "files", Panes: []model.Pane{{SlotID: "00000000-0000-4000-8000-000000000016", Cmd: "yazi"}}},
		},
	}
	if _, _, err := FreezeSave(st, p2, true); err != nil {
		t.Fatal(err)
	}

	// same class pattern across projects → same shape-level fork
	fk := ShapeForkKey(p)
	if st.ForkHits(fk) < 2 {
		t.Fatalf("shape fork hits %d want >=2", st.ForkHits(fk))
	}

	// divergence: different tool set → different fork
	p3 := &model.Session{
		Name: "z", LogicalID: "00000000-0000-4000-8000-000000000017", SchemaVersion: model.CurrentBaselineSchemaVersion, Cwd: "/z",
		Windows: []model.Window{
			{SlotID: "00000000-0000-4000-8000-000000000018", Panes: []model.Pane{{SlotID: "00000000-0000-4000-8000-000000000019", Cmd: "nvim"}}},
			{SlotID: "00000000-0000-4000-8000-000000000020", Panes: []model.Pane{{SlotID: "00000000-0000-4000-8000-000000000021", Cmd: "opencode"}}},
		},
	}
	ObserveForks(st, p3)
	ak := ShapeForkKey(p3)
	if st.ForkHits(ak) < 1 {
		t.Fatal("shape fork not learned")
	}
	if ak == fk {
		t.Fatal("nvim+opencode != nvim+v2+yazi")
	}
}
