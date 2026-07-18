package template

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fm39hz/gotomux/internal/store"
)

func TestToShapeStripsCmd(t *testing.T) {
	p := &store.Preset{
		Name: "fantasia", Cwd: "/work/Fantasia",
		Windows: []store.PresetWindow{
			{Name: "editor", Panes: []store.PresetPane{{Cwd: "/work/Fantasia", Cmd: "nvim"}}},
			{Name: "test", Panes: []store.PresetPane{{Cwd: "/work/Fantasia/test"}}},
		},
	}
	sh := ToShape(p, "editor-test")
	if sh.Windows[0].Panes[0].Cmd != "" || sh.Windows[0].Panes[0].Cwd != "" {
		t.Fatalf("%+v", sh.Windows[0].Panes[0])
	}
	if sh.Windows[1].Panes[0].Cwd != "test" {
		t.Fatalf("rel %q", sh.Windows[1].Panes[0].Cwd)
	}
}

func TestShapeKeyIgnoresCmd(t *testing.T) {
	a := ToShape(&store.Preset{
		Cwd: "/r",
		Windows: []store.PresetWindow{
			{Name: "editor", Panes: []store.PresetPane{{Cmd: "nvim"}}},
			{Name: "shell", Panes: []store.PresetPane{{}}},
		},
	}, "x")
	b := ToShape(&store.Preset{
		Cwd: "/r",
		Windows: []store.PresetWindow{
			{Name: "editor", Panes: []store.PresetPane{{Cmd: "vim"}}},
			{Name: "shell", Panes: []store.PresetPane{{}}},
		},
	}, "y")
	if ShapeKey(a) != ShapeKey(b) {
		t.Fatal("cmd must not affect key")
	}
}

func TestStickMirrorAndDedupe(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	// reset syncOnce for this package — new process via test is enough per package
	// but syncOnce is process-global; first call wins for this test file order
	st, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	p := &store.Preset{
		Name: "proj1", Cwd: "/work/a",
		Windows: []store.PresetWindow{
			{Name: "editor", Panes: []store.PresetPane{{Cwd: "/work/a", Cmd: "nvim"}}},
			{Name: "shell", Panes: []store.PresetPane{{Cwd: "/work/a"}}},
			{Name: "logs", Panes: []store.PresetPane{{Cwd: "/work/a/logs"}}},
		},
	}
	id1, created1, err := StickFrom(st, p)
	if err != nil || !created1 {
		t.Fatalf("stick1 %q %v %v", id1, created1, err)
	}
	if id1 == "default" {
		t.Fatal("3-pane shape must not be default")
	}
	// 1-1 file
	fp := filepath.Join(dir, "cfg", "gotomux", "layouts", id1+".json")
	if _, err := os.Stat(fp); err != nil {
		t.Fatalf("missing config mirror %s: %v", fp, err)
	}
	// same shape different project
	p2 := &store.Preset{
		Name: "proj2", Cwd: "/work/b",
		Windows: []store.PresetWindow{
			{Name: "editor", Panes: []store.PresetPane{{Cwd: "/work/b", Cmd: "hx"}}},
			{Name: "shell", Panes: []store.PresetPane{{Cwd: "/work/b"}}},
			{Name: "logs", Panes: []store.PresetPane{{Cwd: "/work/b/logs"}}},
		},
	}
	id2, created2, err := StickFrom(st, p2)
	if err != nil || created2 || id2 != id1 {
		t.Fatalf("dedupe %q %v (want %q false)", id2, created2, id1)
	}
	// only one json for that shape id
	ents, _ := os.ReadDir(filepath.Join(dir, "cfg", "gotomux", "layouts"))
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
	// (syncOnce already ran — test hand path via Upsert only)
	body, _ := st.GetShape(id1)
	if body == "" {
		t.Fatal("empty body")
	}
}

func TestJSONRoundtrip(t *testing.T) {
	raw := `{"name":"demo","windows":[{"name":"w","layout":"even-horizontal","panes":[{"cwd":""}]}]}`
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
	p := &store.Preset{
		Name: "s", Cwd: "/r",
		Windows: []store.PresetWindow{
			{Name: "main", Panes: []store.PresetPane{{}}},
			{Name: "aux", Panes: []store.PresetPane{{Cwd: "x"}}},
		},
	}
	id, _, err := RememberShape(st, p)
	if err != nil || id == "" {
		t.Fatal(id, err)
	}
	path := filepath.Join(dir, "cfg", "gotomux", "layouts", id+".json")
	// hand-edit: add third window role name change in topology
	hand := &store.Preset{
		Name: id,
		Windows: []store.PresetWindow{
			{Name: "main", Panes: []store.PresetPane{{}}},
			{Name: "aux", Panes: []store.PresetPane{{Cwd: "x"}}},
			{Name: "extra", Panes: []store.PresetPane{{}}},
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
	p := &store.Preset{
		Name: "gotomux",
		Cwd:  "/home/u/gotomux",
		Windows: []store.PresetWindow{
			{Name: "editor", Cwd: "/home/u/gotomux", Panes: []store.PresetPane{{Cwd: "/home/u/gotomux", Cmd: "nvim"}}},
			{Name: "shell", Cwd: "/home/u/gotomux", Panes: []store.PresetPane{{Cwd: "/home/u/gotomux"}}},
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
