package template

import (
	"strings"
	"testing"

	"github.com/fm39hz/gotomux/internal/model"
)

func TestShapeLabelFromEssence(t *testing.T) {
	p := ToShape(&model.Session{
		Name: "sess", Cwd: "/work/x",
		Windows: []model.Window{
			{Name: "editor", Panes: []model.Pane{{Cmd: "nvim"}}},
			{Name: "shell", Layout: "even-vertical", Panes: []model.Pane{{}, {}}},
			{Name: "yazi", Panes: []model.Pane{{Cmd: "yazi"}}},
		},
	}, "shape-abc")
	lab := ShapeLabel(p)
	// class-based labels: nvim→editor, yazi→files
	if lab != "editor+v2+files" {
		t.Fatalf("got %q", lab)
	}
	if !strings.Contains(LabelFileSlug(lab), "editor") {
		t.Fatal(LabelFileSlug(lab))
	}
}

func TestShapeLabelDefault(t *testing.T) {
	if ShapeLabel(builtinDefault()) != "default" {
		t.Fatal(ShapeLabel(builtinDefault()))
	}
}

func TestFormatEmitsIDAndLabel(t *testing.T) {
	p := ToShape(&model.Session{
		Windows: []model.Window{
			{Panes: []model.Pane{{Cmd: "nvim"}}},
			{Layout: "tiled", Panes: []model.Pane{{}, {}, {}, {}}},
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
