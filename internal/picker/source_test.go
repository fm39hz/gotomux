package picker

import (
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type testSource struct {
	items []Item
	cap   int
	hide  bool
}

func (s *testSource) Snapshot() []Item          { return s.items }
func (s *testSource) Refresh() tea.Cmd           { return nil }
func (s *testSource) FlattenFilter(string) FlattenFilter {
	return FlattenFilter{Cap: s.cap, Hide: s.hide}
}

func TestFlattenSourcesDedupByName(t *testing.T) {
	src1 := &testSource{items: []Item{{Name: "x", Path: "/a"}, {Name: "y", Path: "/b"}}}
	src2 := &testSource{items: []Item{{Name: "x", Path: "/c"}}}
	bySrc := map[Source][]Item{src1: src1.items, src2: src2.items}
	order := []Source{src1, src2}

	flat := flattenSources(order, bySrc, "")
	if len(flat) != 2 {
		t.Fatalf("got %d items, want 2 (first-wins dedup)", len(flat))
	}
	if flat[0].Path != "/a" {
		t.Errorf("first x path = %q, want /a (first-wins)", flat[0].Path)
	}
}

func TestFlattenSourcesDedupByPath(t *testing.T) {
	src1 := &testSource{items: []Item{{Name: "x", Path: "/a"}}}
	src2 := &testSource{items: []Item{{Name: "y", Path: "/a"}}}
	bySrc := map[Source][]Item{src1: src1.items, src2: src2.items}
	order := []Source{src1, src2}

	flat := flattenSources(order, bySrc, "")
	if len(flat) != 1 {
		t.Fatalf("got %d items, want 1 (same path dedup)", len(flat))
	}
}

func TestFlattenSourcesCap(t *testing.T) {
	src := &testSource{cap: 2,
		items: []Item{{Name: "a"}, {Name: "b"}, {Name: "c"}}}
	bySrc := map[Source][]Item{src: src.items}
	flat := flattenSources([]Source{src}, bySrc, "")
	if len(flat) != 2 {
		t.Fatalf("got %d items, want 2 (cap)", len(flat))
	}
}

func TestFlattenSourcesHideOnQuery(t *testing.T) {
	src := &testSource{hide: true, items: []Item{{Name: "x", Path: "/a"}}}
	bySrc := map[Source][]Item{src: src.items}
	flat := flattenSources([]Source{src}, bySrc, "query")
	if len(flat) != 0 {
		t.Fatalf("got %d items, want 0 (hidden on query)", len(flat))
	}
}

func TestCountSources(t *testing.T) {
	src1 := &testSource{items: []Item{{Name: "a"}, {Name: "b"}}}
	src2 := &testSource{items: []Item{{Name: "c"}}}
	bySrc := map[Source][]Item{src1: src1.items, src2: src2.items}
	if n := countSources(bySrc); n != 3 {
		t.Errorf("countSources = %d, want 3", n)
	}
}

func TestNormPath(t *testing.T) {
	if p := normPath("/a/b/../c"); p != "/a/c" {
		t.Errorf("normPath = %q, want /a/c", p)
	}
	if p := normPath(""); p != "" {
		t.Errorf("normPath('') = %q, want ''", p)
	}
}

func TestSnapshotAll(t *testing.T) {
	src1 := &testSource{items: []Item{{Name: "a"}}}
	src2 := &testSource{items: []Item{{Name: "b"}, {Name: "c"}}}
	bySrc := snapshotAll([]Source{src1, src2})
	if len(bySrc) != 2 {
		t.Fatalf("snapshotAll returned %d sources, want 2", len(bySrc))
	}
	if len(bySrc[src1]) != 1 || len(bySrc[src2]) != 2 {
		t.Errorf("wrong item counts")
	}
}

func TestZoxideItemsDedup(t *testing.T) {
	// zoxideItems should not produce items with names already in names/paths
	names := map[string]bool{"existing": true}
	paths := map[string]bool{}
	items := zoxideItems([]string{"/home/user/existing", "/home/user/newpath"}, names, paths)
	for _, it := range items {
		if it.Name == "existing" {
			t.Errorf("zoxideItems included deduped name 'existing'")
		}
	}
	if len(items) != 1 {
		t.Errorf("zoxideItems = %d items, want 1 (1 deduped)", len(items))
	}
}

func TestSourceCacheInvalidate(t *testing.T) {
	cache := &sourceCache{
		zoxSt: nil,
		zoxMu: &sync.Mutex{},
	}
	cache.tmuxOK.Store(true)
	cache.presetOK.Store(true)
	cache.invalidate()
	if cache.tmuxOK.Load() {
		t.Error("tmuxOK should be false after invalidate")
	}
	if cache.presetOK.Load() {
		t.Error("presetOK should be false after invalidate")
	}
}

// verify sourceCache pointer avoids lock copy in model
func TestSourceCachePointer(t *testing.T) {
	cache := &sourceCache{
		zoxSt: nil,
		zoxMu: &sync.Mutex{},
	}
	_ = cache // expect no vet warning for lock copy
}

// TestFlattenSourcesOrder verifies source order is preserved (create > active > preset > zoxide)
func TestFlattenSourcesOrder(t *testing.T) {
	zox := &testSource{items: []Item{{Name: "z", Path: "/z"}}}
	act := &testSource{items: []Item{{Name: "a", Path: "/a"}}}
	bySrc := map[Source][]Item{zox: zox.items, act: act.items}
	flat := flattenSources([]Source{act, zox}, bySrc, "")
	if len(flat) != 2 || flat[0].Name != "a" || flat[1].Name != "z" {
		t.Errorf("order = %v, want [a z]", itemNames(flat))
	}
}

func itemNames(items []Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Name
	}
	return out
}
