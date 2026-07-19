package template

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fm39hz/gotomux/internal/store"
)

func TestReconcileConfigShapesRenamesAndPrunes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "cfg"))
	st, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	p := &store.Preset{
		Name: "s", Cwd: "/r",
		Windows: []store.PresetWindow{
			{Panes: []store.PresetPane{{Cmd: "nvim"}}},
			{Layout: "even-vertical", Panes: []store.PresetPane{{}, {}}},
			{Panes: []store.PresetPane{{Cmd: "yazi"}}},
		},
	}
	id, _, err := FreezeSave(st, p, true)
	if err != nil {
		t.Fatal(err)
	}
	shapes := filepath.Join(dir, "cfg", "gotomux", "shapes")
	// litter orphan + legacy id file
	_ = os.WriteFile(filepath.Join(shapes, "orphan-junk.json"), []byte(`{}`), 0o644)
	_ = os.WriteFile(filepath.Join(shapes, id+".json"), []byte(`{"name":"old"}`), 0o644)

	reconcileConfigShapes(st)

	ents, _ := os.ReadDir(shapes)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	// no orphan, no pure-id filename (unless default)
	for _, n := range names {
		if n == "orphan-junk.json" {
			t.Fatal("orphan not pruned")
		}
		if n == id+".json" {
			t.Fatal("legacy id filename should be replaced by label--suffix")
		}
	}
	// has label style file containing id
	ok := false
	for _, n := range names {
		raw, _ := os.ReadFile(filepath.Join(shapes, n))
		if strings.Contains(string(raw), id) && strings.Contains(string(raw), "nvim") {
			ok = true
			if !strings.Contains(n, "--") && n != "default.json" {
				// label--suffix
				t.Fatalf("want label--suffix name, got %s", n)
			}
		}
	}
	if !ok {
		t.Fatalf("missing mirrored shape in %v", names)
	}
}

func TestReconcileUserConfig(t *testing.T) {
	if os.Getenv("RECONCILE_USER") != "1" {
		t.Skip("set RECONCILE_USER=1 to rebuild ~/.config/gotomux/shapes")
	}
	// use real XDG (unset test overrides)
	st, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reconcileConfigShapes(st)
}
