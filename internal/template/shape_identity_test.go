package template

import (
	"testing"

	"github.com/fm39hz/gotomux/internal/model"
)

func TestClassificationBoundaryKeepsExistingShapeAndForkIdentity(t *testing.T) {
	p := &model.Session{Name: "demo", Cwd: "/work/demo", Windows: []model.Window{
		{Name: "editor", Layout: "", Panes: []model.Pane{{Cwd: "/work/demo", Cmd: "nvim"}}},
		{Name: "shell", Layout: "tiled", Panes: []model.Pane{{Cwd: "/work/demo"}, {Cwd: "/work/demo", Cmd: "make"}}},
		{Name: "files", Panes: []model.Pane{{Cwd: "/work/demo", Cmd: "yazi"}}},
	}}
	if got, want := ShapeKey(p), "792407ca7cb4e122"; got != want {
		t.Errorf("shape key changed: got %s, want %s", got, want)
	}
	if got, want := ShapeForkKey(p), "4883e83c6024fa17"; got != want {
		t.Errorf("fork key changed: got %s, want %s", got, want)
	}
	wantBody := `{"key":"4883e83c6024fa17","nWindows":3,"windows":[{"fork":"editor","panes":[{"tool":"nvim"}]},{"fork":"shell,make","split":"tiled","panes":[{},{"tool":"make"}]},{"fork":"files","panes":[{"tool":"yazi"}]}]}`
	if got := ShapeForkBody(p); got != wantBody {
		t.Errorf("fork body changed:\n got %s\nwant %s", got, wantBody)
	}
}
