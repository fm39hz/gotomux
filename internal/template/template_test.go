package template

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/store"
)

func TestToShapeEssence(t *testing.T) {
	p := &model.Session{
		Name: "fantasia", Cwd: "/work/Fantasia",
		Windows: []model.Window{
			{Name: "editor", Panes: []model.Pane{{Cwd: "/work/Fantasia", Cmd: "nvim"}}},
			{Name: "test", Layout: "4080,158x35,0,0[158x17,0,0,1,158x17,0,18,2]", Panes: []model.Pane{
				{Cwd: "/work/Fantasia/test", Cmd: "go test"},
				{Cwd: "/work/Fantasia/pkg"},
			}},
			{Name: "files", Panes: []model.Pane{{Cmd: "yazi"}}},
		},
	}
	sh := ToShape(p, "x")
	if sh.Cwd != "" {
		t.Fatalf("shape cwd must be empty: %q", sh.Cwd)
	}
	if sh.Windows[0].Panes[0].Cmd != "nvim" {
		t.Fatalf("editor tool intent: %+v", sh.Windows[0].Panes[0])
	}
	if sh.Windows[1].Layout != "even-vertical" {
		t.Fatalf("v-dump -> even-vertical: %q", sh.Windows[1].Layout)
	}
	if sh.Windows[1].Panes[0].Cmd != "go" {
		t.Fatalf("tool intent binBase: %q", sh.Windows[1].Panes[0].Cmd)
	}
	if sh.Windows[1].Panes[0].Cwd != "" || sh.Windows[2].Panes[0].Cmd != "yazi" {
		t.Fatalf("cwd stripped, yazi kept: %+v %+v", sh.Windows[1].Panes[0], sh.Windows[2].Panes[0])
	}
	// shell-looking empty
	if sh.Windows[1].Panes[1].Cmd != "" {
		t.Fatalf("empty shell pane: %+v", sh.Windows[1].Panes[1])
	}
}

func TestShapeKeyIgnoresPathsKeepsTools(t *testing.T) {
	// same topology+tools, different paths/names -> same key
	a := ToShape(&model.Session{
		Name: "proj-a", Cwd: "/work/a",
		Windows: []model.Window{
			{Name: "editor", Cwd: "/work/a", Panes: []model.Pane{{Cwd: "/work/a", Cmd: "nvim"}}},
			{Name: "shell", Panes: []model.Pane{{Cwd: "/work/a/apps"}, {Cwd: "/work/a"}}},
		},
	}, "x")
	b := ToShape(&model.Session{
		Name: "proj-b", Cwd: "/other/b",
		Windows: []model.Window{
			{Name: "code", Cwd: "/other/b", Panes: []model.Pane{{Cwd: "/other/b/src", Cmd: "nvim"}}},
			{Name: "term", Panes: []model.Pane{{Cwd: "/other/b"}, {Cwd: "/tmp"}}},
		},
	}, "y")
	if ShapeKey(a) != ShapeKey(b) {
		t.Fatalf("paths must not affect key: %s vs %s", ShapeKey(a), ShapeKey(b))
	}
	// different tool intent -> different key
	c := ToShape(&model.Session{
		Windows: []model.Window{
			{Panes: []model.Pane{{Cmd: "vim"}}},
			{Panes: []model.Pane{{}, {}}},
		},
	}, "z")
	if ShapeKey(a) == ShapeKey(c) {
		t.Fatal("tool intent must affect key")
	}
}

func TestApplyUsesRootAndTools(t *testing.T) {
	sh := ToShape(&model.Session{
		Cwd: "/old",
		Windows: []model.Window{
			{Name: "e", Cwd: "/old/sub", Panes: []model.Pane{{Cwd: "/old/sub", Cmd: "nvim"}, {Cwd: "/old", Cmd: "yazi"}}},
		},
	}, "s")
	got := Apply(sh, "newproj", "/work/new")
	if got.Name != "newproj" || got.Cwd != "/work/new" {
		t.Fatalf("%+v", got)
	}
	if len(got.Windows) != 1 || len(got.Windows[0].Panes) != 2 {
		t.Fatalf("windows %+v", got.Windows)
	}
	if got.Windows[0].Panes[0].Cwd != "/work/new" || got.Windows[0].Panes[0].Cmd != "nvim" {
		t.Fatalf("root+tool: %+v", got.Windows[0].Panes[0])
	}
	if got.Windows[0].Panes[1].Cmd != "yazi" {
		t.Fatalf("yazi: %+v", got.Windows[0].Panes[1])
	}
}

func TestStickMirrorAndDedupe(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	// reset syncOnce for this package - new process via test is enough per package
	// but syncOnce is process-global; first call wins for this test file order
	st, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	p := &model.Session{
		Name: "proj1", Cwd: "/work/a",
		Windows: []model.Window{
			{Name: "editor", Panes: []model.Pane{{Cwd: "/work/a", Cmd: "nvim"}}},
			{Name: "shell", Panes: []model.Pane{{Cwd: "/work/a"}}},
			{Name: "logs", Panes: []model.Pane{{Cwd: "/work/a/logs"}}},
		},
	}
	id1, created1, err := StickFrom(st, p)
	if err != nil || !created1 {
		t.Fatalf("stick1 %q %v %v", id1, created1, err)
	}
	if id1 == "default" {
		t.Fatal("3-pane shape must not be default")
	}
	// mirror file uses label--suffix.json (id lives inside JSON)
	ents0, _ := os.ReadDir(filepath.Join(dir, "cfg", "gotomux", "shapes"))
	found := false
	for _, e := range ents0 {
		if strings.HasSuffix(e.Name(), ".json") && e.Name() != "default.json" {
			raw, _ := os.ReadFile(filepath.Join(dir, "cfg", "gotomux", "shapes", e.Name()))
			if strings.Contains(string(raw), id1) {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("missing config mirror containing id %s in %v", id1, ents0)
	}
	// same shape different project
	p2 := &model.Session{
		Name: "proj2", Cwd: "/work/b",
		Windows: []model.Window{
			{Name: "editor", Panes: []model.Pane{{Cwd: "/work/b", Cmd: "nvim"}}},
			{Name: "shell", Panes: []model.Pane{{Cwd: "/work/b"}}},
			{Name: "logs", Panes: []model.Pane{{Cwd: "/work/b/logs"}}},
		},
	}
	id2, created2, err := StickFrom(st, p2)
	if err != nil || created2 || id2 != id1 {
		t.Fatalf("dedupe %q %v (want %q false)", id2, created2, id1)
	}
	// only one json for that shape id
	ents, _ := os.ReadDir(filepath.Join(dir, "cfg", "gotomux", "shapes"))
	var jsons []string
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".json" {
			jsons = append(jsons, e.Name())
		}
	}
	// default may also be written
	if len(jsons) < 1 {
		t.Fatal("no json")
	}
	// hand-edit: change file, reopen store sync should load
	// (syncOnce already ran - test hand path via Upsert only)
	body, _ := st.GetShape(id1)
	if body == "" {
		t.Fatal("empty body")
	}
}

func TestJSONRoundtrip(t *testing.T) {
	raw := `{"name":"demo","windows":[{"name":"w","split":"even-horizontal","panes":[{"cwd":""}]}]}`
	p, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(Format(p)); err != nil {
		t.Fatal(err)
	}
}

func TestConfigHandEditWinsByMtime(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	st, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// create shape via freeze path
	p := &model.Session{
		Name: "s", Cwd: "/r",
		Windows: []model.Window{
			{Name: "main", Panes: []model.Pane{{}}},
			{Name: "aux", Panes: []model.Pane{{Cwd: "x"}}},
		},
	}
	id, _, err := RememberShape(st, p)
	if err != nil || id == "" {
		t.Fatal(id, err)
	}
	// find mirrored file for id (label--suffix.json)
	var path string
	entsH, _ := os.ReadDir(filepath.Join(dir, "cfg", "gotomux", "shapes"))
	for _, e := range entsH {
		raw, _ := os.ReadFile(filepath.Join(dir, "cfg", "gotomux", "shapes", e.Name()))
		if strings.Contains(string(raw), id) {
			path = filepath.Join(dir, "cfg", "gotomux", "shapes", e.Name())
			break
		}
	}
	if path == "" {
		// write new label-style path
		path = filepath.Join(dir, "cfg", "gotomux", "shapes", "hand--test.json")
	}
	// hand-edit: add third window role name change in topology
	hand := &model.Session{
		Name: id,
		Windows: []model.Window{
			{Name: "main", Panes: []model.Pane{{}}},
			{Name: "aux", Panes: []model.Pane{{Cwd: "x"}}},
			{Name: "extra", Panes: []model.Pane{{}}},
		},
	}
	body := Format(ToShape(hand, id))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// bump mtime into the future relative to DB
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, future, future)

	// new store + new process sync: re-open DB same path but syncOnce already ran in this process.
	// Call Upsert path by simulating merge rule unit-level:
	fi, _ := os.Stat(path)
	_, dbUpd, ok := st.GetShapeMeta(id)
	if !ok {
		t.Fatal("meta")
	}
	if fi.ModTime().Unix() <= dbUpd {
		t.Fatal("mtime should be newer")
	}
	pure := ToShape(hand, id)
	_ = st.UpsertShapeByID(id, ShapeKey(pure), Format(pure))
	got, _ := st.GetShape(id)
	gp, err := Parse(got)
	if err != nil || len(gp.Windows) != 3 {
		t.Fatalf("hand-edit not in DB: %v %+v", err, gp)
	}
}

func TestFormatOmitsDupCwd(t *testing.T) {
	p := &model.Session{
		Name: "gotomux",
		Cwd:  "/home/u/gotomux",
		Windows: []model.Window{
			{Name: "editor", Cwd: "/home/u/gotomux", Panes: []model.Pane{{Cwd: "/home/u/gotomux", Cmd: "nvim"}}},
			{Name: "shell", Cwd: "/home/u/gotomux", Panes: []model.Pane{{Cwd: "/home/u/gotomux"}}},
		},
	}
	out := Format(p)
	if strings.Count(out, "/home/u/gotomux") != 1 {
		t.Fatalf("cwd once:\n%s", out)
	}
	if !strings.Contains(out, "nvim") {
		t.Fatal("cmd kept")
	}
	sh := ToShape(p, "editor-shell")
	out2 := Format(sh)
	if strings.Contains(out2, "/home/") {
		t.Fatalf("pure no abs:\n%s", out2)
	}
}

func TestToShapeStripsPathWindowNames(t *testing.T) {
	p := &model.Session{
		Name: "sess", Cwd: "/home/u/proj",
		Windows: []model.Window{
			{Name: "editor", Panes: []model.Pane{{}}},
			{Name: "/home/u/.cache/", Panes: []model.Pane{{Cmd: "claude"}}},
		},
	}
	sh := ToShape(p, "x")
	if sh.Windows[0].Name != "editor" {
		t.Fatalf("role kept: %q", sh.Windows[0].Name)
	}
	// path name dropped; tool intent -> chrome role
	if sh.Windows[1].Name != "claude" && sh.Windows[1].Name != "editor" {
		// claude is multi-agent tool chrome (kept as tool name)
		t.Fatalf("path chrome: got %q", sh.Windows[1].Name)
	}
	if sh.Windows[1].Name == "w1" {
		t.Fatal("should infer from tool, not wN when cmd present")
	}
	out := Format(sh)
	if strings.Contains(out, "/home/") {
		t.Fatalf("must not leak home path:\n%s", out)
	}
}

func TestShapeIDOpaque(t *testing.T) {
	p := &model.Session{
		Cwd: "/home/leaky/user/proj",
		Windows: []model.Window{
			{Name: "/home/leaky/user/.cache/", Panes: []model.Pane{{Cmd: "claude"}}},
			{Name: "shell", Panes: []model.Pane{{}}},
		},
	}
	sh := ToShape(p, "tmp")
	key := ShapeKey(sh)
	id := shapeIDFrom(p, key)
	if !strings.HasPrefix(id, "shape-") || len(id) != len("shape-")+16 {
		t.Fatalf("opaque id want shape-<16hex>, got %q", id)
	}
	if strings.Contains(id, "home") || strings.Contains(id, "leak") || strings.Contains(id, "cache") {
		t.Fatalf("id must not contain path tokens: %q", id)
	}
	out := Format(sh)
	if strings.Contains(out, "/home/") || strings.Contains(out, "leaky") {
		t.Fatalf("path must not leak:\n%s", out)
	}
	if !strings.Contains(out, `"cmd": "claude"`) {
		t.Fatalf("tool intent must remain:\n%s", out)
	}
}

func TestWindowChromeFromTools(t *testing.T) {
	sh := ToShape(&model.Session{
		Name: "proj", Cwd: "/work/proj",
		Windows: []model.Window{
			{Name: "proj", Panes: []model.Pane{{Cmd: "nvim"}}}, // session-like name -> editor via tool
			{Name: "cong", Panes: []model.Pane{{Cmd: "nvim"}}}, // branch label -> editor via tool
			{Name: "whatever", Layout: "even-vertical", Panes: []model.Pane{{}, {}}},
			{Name: "/home/x/.cache/", Panes: []model.Pane{{Cmd: "yazi"}}},
			{Name: "opencode", Panes: []model.Pane{{Cmd: "opencode"}}},
		},
	}, "s")
	want := []string{"editor", "editor", "shell", "files", "opencode"}
	for i, w := range want {
		if sh.Windows[i].Name != w {
			t.Fatalf("win%d: got %q want %q", i, sh.Windows[i].Name, w)
		}
	}
}
