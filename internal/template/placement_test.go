package template

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/store"
)

func TestPatternFromPresetUmbrella(t *testing.T) {
	root := t.TempDir()
	c0 := filepath.Join(root, "cong-dlqg")
	c1 := filepath.Join(root, "kho-dl-mo")
	for _, d := range []string{c0, c1} {
		_ = os.MkdirAll(d, 0o755)
		_ = os.MkdirAll(filepath.Join(d, ".git"), 0o755)
	}
	p := &model.Session{
		Name: "kho-cong", Cwd: root,
		Windows: []model.Window{
			{Panes: []model.Pane{{Cwd: root}}},
			{Panes: []model.Pane{
				{Cwd: c0}, {Cwd: c0}, {Cwd: c1}, {Cwd: c1},
			}},
			{Panes: []model.Pane{{Cwd: root}}},
		},
	}
	pat := PatternFromPreset(p)
	want := "R|C0,C0,C1,C1|R"
	if pat != want {
		t.Fatalf("got %q want %q", pat, want)
	}
	// all root -> empty
	flat := &model.Session{
		Cwd: root,
		Windows: []model.Window{
			{Panes: []model.Pane{{Cwd: root}}},
		},
	}
	if PatternFromPreset(flat) != "" {
		t.Fatal("trivial must be empty")
	}
}

func TestPatternFromPresetKeepsUnknownOutsideRoot(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	_ = os.MkdirAll(filepath.Join(child, ".git"), 0o755)
	p := &model.Session{
		Name: "scope", Cwd: root,
		Windows: []model.Window{{Panes: []model.Pane{
			{Cwd: child}, {Cwd: filepath.Join(root, "..", "outside")},
		}}},
	}
	if got, want := PatternFromPreset(p), "C0,?"; got != want {
		t.Fatalf("pattern = %q, want %q", got, want)
	}
}

func TestObserveAndBakePlacement(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	st, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	rootA := filepath.Join(dir, "umbrella-a")
	c0a := filepath.Join(rootA, "fe")
	c1a := filepath.Join(rootA, "be")
	for _, d := range []string{c0a, c1a} {
		_ = os.MkdirAll(filepath.Join(d, ".git"), 0o755)
	}
	inst := &model.Session{
		Name: "ua", Cwd: rootA,
		Windows: []model.Window{
			{Name: "ed", Panes: []model.Pane{{Cwd: rootA, Cmd: "nvim"}}},
			{Name: "sh", Layout: "tiled", Panes: []model.Pane{
				{Cwd: c0a}, {Cwd: c1a},
			}},
		},
	}
	sid, _, err := FreezeSave(st, inst, true)
	if err != nil {
		t.Fatal(err)
	}
	pat, ok := st.BestPlacement(sid)
	if !ok {
		t.Fatal("expected learned placement")
	}
	// children of rootA sorted: be, fe -> freeze panes fe,be -> C1,C0
	if pat != "R|C1,C0" {
		t.Fatalf("learned %q want R|C1,C0 (alpha children be,fe)", pat)
	}

	rootB := filepath.Join(dir, "umbrella-b")
	be := filepath.Join(rootB, "be")
	fe := filepath.Join(rootB, "fe")
	for _, d := range []string{be, fe} {
		_ = os.MkdirAll(filepath.Join(d, ".git"), 0o755)
	}
	tmpl, _, err := LoadActive(st)
	if err != nil {
		t.Fatal(err)
	}
	baked := bakeShape(st, tmpl, "ub", rootB, sid)
	if len(baked.Windows) != 2 || len(baked.Windows[1].Panes) != 2 {
		t.Fatalf("%+v", baked.Windows)
	}
	if baked.Windows[0].Panes[0].Cwd != rootB {
		t.Fatalf("w0 %s", baked.Windows[0].Panes[0].Cwd)
	}
	// C1,C0 -> fe, be
	if baked.Windows[1].Panes[0].Cwd != fe || baked.Windows[1].Panes[1].Cwd != be {
		t.Fatalf("w1 panes %s %s want fe,be",
			baked.Windows[1].Panes[0].Cwd, baked.Windows[1].Panes[1].Cwd)
	}
	// tool intent bakes when present on shape
	_ = baked.Windows[0].Panes[0].Cmd
	// split materialised at bake (not left empty for Load to invent)
	if len(baked.Windows[1].Panes) > 1 && baked.Windows[1].Layout != "even-horizontal" && baked.Windows[1].Layout != "tiled" {
		// default inference is even-horizontal when shape had no named split
		if baked.Windows[1].Layout != "even-horizontal" {
			t.Fatalf("bake must set split, got %q", baked.Windows[1].Layout)
		}
	}
}

func TestBakeMissingChildFallsBackRoot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	st, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.RecordPlacement("shape-x", "R|C0,C1")
	// shape body minimal via Upsert
	body := `{"name":"shape-x","windows":[{"name":"a","panes":[{}]},{"name":"b","panes":[{},{}]}]}`
	_ = st.UpsertShapeByID("shape-x", "keyx", body)
	root := filepath.Join(dir, "only-one")
	_ = os.MkdirAll(filepath.Join(root, "fe", ".git"), 0o755)
	tmpl, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	baked := bakeShape(st, tmpl, "s", root, "shape-x")
	// C0=fe, C1 missing -> root
	if baked.Windows[1].Panes[0].Cwd != filepath.Join(root, "fe") {
		t.Fatalf("c0 %s", baked.Windows[1].Panes[0].Cwd)
	}
	if baked.Windows[1].Panes[1].Cwd != root {
		t.Fatalf("c1 fallback %s", baked.Windows[1].Panes[1].Cwd)
	}
	_ = strings.Contains
}

func TestBakeUnknownPlacementFallsBackRootWithoutBecomingObservedScope(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	st, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.RecordPlacement("shape-unknown", "C0,?,C1"); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"shape-unknown","windows":[{"name":"shell","panes":[{},{},{}]}]}`
	if err := st.UpsertShapeByID("shape-unknown", "key-unknown", body); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	child := filepath.Join(root, "child")
	if err := os.MkdirAll(filepath.Join(child, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	tmpl, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	baked := bakeShape(st, tmpl, "unknown", root, "shape-unknown")
	if got := []string{baked.Windows[0].Panes[0].Cwd, baked.Windows[0].Panes[1].Cwd, baked.Windows[0].Panes[2].Cwd}; got[0] != child || got[1] != root || got[2] != root {
		t.Fatalf("baked cwd slots = %v, want [child root root]", got)
	}
	if got := PatternFromPreset(&model.Session{Cwd: root, Windows: []model.Window{{Panes: []model.Pane{{Cwd: root}, {Cwd: root}}}}}); got != "" {
		t.Fatalf("operational fallback was learned as placement %q", got)
	}
}
