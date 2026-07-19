package template

import (
	"strings"
	"testing"

	"github.com/fm39hz/gotomux/internal/store"
)

func TestShapeLabelFromEssence(t *testing.T) {
	p := ToShape(&store.Preset{
		Name: "sess", Cwd: "/work/x",
		Windows: []store.PresetWindow{
			{Name: "editor", Panes: []store.PresetPane{{Cmd: "nvim"}}},
			{Name: "shell", Layout: "even-vertical", Panes: []store.PresetPane{{}, {}}},
			{Name: "yazi", Panes: []store.PresetPane{{Cmd: "yazi"}}},
		},
	}, "shape-abc")
	lab := ShapeLabel(p)
	if lab != "nvim+v2+yazi" {
		t.Fatalf("got %q", lab)
	}
	if !strings.Contains(LabelFileSlug(lab), "nvim") {
		t.Fatal(LabelFileSlug(lab))
	}
}

func TestShapeLabelDefault(t *testing.T) {
	if ShapeLabel(builtinDefault()) != "default" {
		t.Fatal(ShapeLabel(builtinDefault()))
	}
}

func TestFormatEmitsIDAndLabel(t *testing.T) {
	p := ToShape(&store.Preset{
		Windows: []store.PresetWindow{
			{Panes: []store.PresetPane{{Cmd: "nvim"}}},
			{Layout: "tiled", Panes: []store.PresetPane{{}, {}, {}, {}}},
		},
	}, "shape-deadbeefdeadbeef")
	p.Name = "shape-deadbeefdeadbeef"
	out := Format(p)
	if !strings.Contains(out, `"id": "shape-deadbeefdeadbeef"`) {
		t.Fatalf("id: %s", out)
	}
	if !strings.Contains(out, `"label":`) {
		t.Fatalf("label: %s", out)
	}
	if strings.Contains(out, "158x35") {
		t.Fatal("dump")
	}
}
