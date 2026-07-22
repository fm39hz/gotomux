package template

import (
	"testing"

	"github.com/fm39hz/gotomux/internal/store"
)

func TestNormalizeShapeBody(t *testing.T) {
	body := `{"name":"demo","windows":[{"name":"code","panes":[{"cmd":"nvim"}]}]}`
	got := normalizeShapeBody("shape-x", body)
	if got == "" {
		t.Fatal("normalizeShapeBody returned empty")
	}
	p, err := Parse(got)
	if err != nil {
		t.Fatalf("parse normalized: %v", err)
	}
	if p.Name != "shape-x" {
		t.Errorf("name = %q, want shape-x", p.Name)
	}
}

func TestNormalizeShapeBodyInvalid(t *testing.T) {
	if got := normalizeShapeBody("x", "not valid json"); got != "" {
		t.Errorf("expected empty for invalid json, got %q", got)
	}
}

func TestMustParseShape(t *testing.T) {
	p := mustParseShape("shape-x", `{"name":"x","windows":[{"panes":[{}]}]}`)
	if p == nil || p.Name != "shape-x" {
		t.Errorf("mustParseShape = %+v", p)
	}
}

func TestMustParseShapeInvalid(t *testing.T) {
	p := mustParseShape("x", "bad")
	if p == nil || p.Name != "x" {
		t.Errorf("mustParseShape should return fallback with name")
	}
}

func TestBuiltinDefault(t *testing.T) {
	d := builtinDefault()
	if d.Name != "default" || len(d.Windows) != 2 {
		t.Fatalf("builtinDefault = %+v", d)
	}
	if d.Windows[0].Name != "editor" || d.Windows[1].Name != "shell" {
		t.Errorf("unexpected window names: %q %q", d.Windows[0].Name, d.Windows[1].Name)
	}
}

func TestToShapeNilFallback(t *testing.T) {
	s := ToShape(nil, "test")
	if s == nil || s.Name != "default" {
		t.Errorf("ToShape(nil) = %+v", s)
	}
	if len(s.Windows) != 2 {
		t.Errorf("ToShape(nil) should return builtin default windows, got %d", len(s.Windows))
	}
}

func TestShapeKeyNil(t *testing.T) {
	if k := ShapeKey(nil); k != "" {
		t.Errorf("ShapeKey(nil) = %q, want empty", k)
	}
}

func TestShapeKeyEmptyWindows(t *testing.T) {
	p := &store.Preset{Name: "empty", Windows: nil}
	k := ShapeKey(p)
	if k == "" {
		t.Error("ShapeKey should not be empty for nil windows (default window)")
	}
}

func TestShapeIDFromEmptyKey(t *testing.T) {
	id := shapeIDFrom(nil, "")
	if id != "shape-0000000000000000" {
		t.Errorf("shapeIDFrom('') = %q", id)
	}
}

func TestDefaultChrome(t *testing.T) {
	if s := defaultChrome(1); s != "shell" {
		t.Errorf("defaultChrome(1) = %q", s)
	}
	if s := defaultChrome(3); s != "shell" {
		t.Errorf("defaultChrome(3) = %q", s)
	}
}

func TestNeutralRoleSlug(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"editor", "editor"},
		{"shell", "shell"},
		{"files", "files"},
		{"file", "files"},
		{"term", "shell"},
		{"terminal", "shell"},
		{"agent", "agent"},
		{"/abs/path", ""},
		{"~/home", ""},
		{"custom-name", ""},
		{"", ""},
	}
	for _, tc := range tests {
		got := neutralRoleSlug(tc.in)
		if got != tc.want {
			t.Errorf("neutralRoleSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestShapeBodyDefault(t *testing.T) {
	p := builtinDefault()
	id, key, body := shapeBody(p, true)
	if id != "default" {
		t.Errorf("id = %q, want default", id)
	}
	if key == "" || body == "" {
		t.Error("key or body empty")
	}
}

func TestIsShapeID(t *testing.T) {
	if !isShapeID("default") {
		t.Error("isShapeID(default) should be true")
	}
	if !isShapeID("shape-abc123") {
		t.Error("isShapeID(shape-...) should be true")
	}
	if isShapeID("session-name") {
		t.Error("isShapeID(session-name) should be false")
	}
}

func TestLooksLikeShapeTree(t *testing.T) {
	p := &store.Preset{Name: "s", Windows: []store.PresetWindow{{Panes: []store.PresetPane{{}}}}}
	if !looksLikeShapeTree(p) {
		t.Error("should look like shape tree (no cwd)")
	}
	p.Cwd = "/tmp"
	if looksLikeShapeTree(p) {
		t.Error("should NOT look like shape tree (has cwd)")
	}
}
