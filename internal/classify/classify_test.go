package classify

import (
	"path/filepath"
	"testing"

	"github.com/fm39hz/gotomux/internal/toolclass"
)

func TestClassifyPaneToolKindAndScope(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	nested := filepath.Join(child, "nested")
	ctx := ProjectContext{Root: root, Children: []string{child, nested}}

	got := ClassifyPane("yazi --cwd", child, ctx)
	if got.Tool != "yazi" || got.Kind != toolclass.KindFiles || got.Scope != ChildScope(0) {
		t.Fatalf("yazi child classification = %+v", got)
	}
	got = ClassifyPane("nvim", root, ctx)
	if got.Tool != "nvim" || got.Kind != toolclass.KindEditor || got.Scope != ScopeRoot {
		t.Fatalf("nvim root classification = %+v", got)
	}
	got = ClassifyPane("bash", filepath.Join(root, "src"), ctx)
	if got.Tool != "" || got.Kind != toolclass.KindUnknown || got.Scope != ScopeRoot {
		t.Fatalf("shell descendant classification = %+v", got)
	}
	got = ClassifyPane("nvim", filepath.Join(root, "..", "outside"), ctx)
	if got.Scope != ScopeUnknown {
		t.Fatalf("outside path scope = %q, want unknown", got.Scope)
	}
}

func TestScopeForPathLongestChildPrefixAndStableKey(t *testing.T) {
	root := t.TempDir()
	outer := filepath.Join(root, "packages")
	inner := filepath.Join(outer, "app")
	ctx := ProjectContext{Root: root, Children: []string{outer, inner}}
	path := filepath.Join(inner, "src")
	if got := ScopeForPath(path, ctx); got != ChildScope(1) {
		t.Fatalf("scope = %q, want %q", got, ChildScope(1))
	}
	if got := StableScopeKey(path, ctx); got != "C:packages/app" {
		t.Fatalf("stable scope = %q, want C:packages/app", got)
	}
}

func TestChildIndexValidation(t *testing.T) {
	for _, tc := range []struct {
		in   ScopeRef
		want int
		ok   bool
	}{{"C0", 0, true}, {"C12", 12, true}, {"C-1", 0, false}, {"C01", 0, false}, {"CX", 0, false}, {"R", 0, false}, {"?", 0, false}} {
		got, ok := tc.in.ChildIndex()
		if got != tc.want || ok != tc.ok {
			t.Errorf("%q.ChildIndex() = %d,%v, want %d,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
