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

func (s *testSource) Snapshot() []Item { return s.items }
func (s *testSource) Refresh() tea.Cmd { return nil }
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

func TestSourceCacheInvalidate(t *testing.T) {
	cache := &sourceCache{
		zoxSt:  nil,
		zoxMu:  &sync.Mutex{},
		seeded: true,
	}
	cache.tmuxDone.Store(true)
	cache.presetDone.Store(true)
	cache.invalidate()
	if cache.tmuxDone.Load() || cache.presetDone.Load() {
		t.Error("done flags should be cleared by invalidate")
	}
	// seeded must clear too: invalidate runs after a kill/delete/freeze, which
	// makes the daemon's payload wrong. Leaving it set would re-serve the stale
	// snapshot instead of re-reading tmux and SQLite.
	if cache.seeded {
		t.Error("seeded should be cleared by invalidate")
	}
}

func TestSeededCacheDoesNotFallBackToSources(t *testing.T) {
	// A daemon legitimately reporting zero sessions and zero presets must not
	// cause the sources to re-run tmux/SQLite behind our back. The old guards
	// keyed off length, so "empty" was indistinguishable from "unknown".
	cache := &sourceCache{zoxMu: &sync.Mutex{}, seeded: true}
	cache.tmuxDone.Store(true)
	cache.presetDone.Store(true)

	ctl := &countingConnector{}
	st := &countingStore{}
	srcs := defaultSources(ctl, st, "", "", cache)
	_ = snapshotAll(srcs)

	if ctl.listLive != 0 {
		t.Errorf("ListLive called %d times on a seeded cache, want 0", ctl.listLive)
	}
	if st.listMeta != 0 {
		t.Errorf("ListMeta called %d times on a seeded cache, want 0", st.listMeta)
	}
}

func TestFlattenAppliesCapAfterDedup(t *testing.T) {
	// Cap counts rows that survive dedup. Applying it to the raw slice first let
	// duplicates consume the budget, so ZoxideCap=2 could yield fewer than 2 rows.
	earlier := &testSource{items: []Item{{Name: "dup", Path: "/dup"}}}
	capped := &testSource{
		items: []Item{
			{Name: "dup", Path: "/dup"}, // deduped against earlier
			{Name: "a", Path: "/a"},
			{Name: "b", Path: "/b"},
			{Name: "c", Path: "/c"},
		},
		cap: 2,
	}
	bySrc := map[Source][]Item{earlier: earlier.items, capped: capped.items}
	got := flattenSources([]Source{earlier, capped}, bySrc, "")

	// earlier's "dup" plus exactly cap=2 surviving rows from capped.
	if len(got) != 3 {
		t.Fatalf("flattened %d items, want 3: %+v", len(got), got)
	}
	if got[1].Name != "a" || got[2].Name != "b" {
		t.Errorf("capped source contributed %q,%q; want a,b", got[1].Name, got[2].Name)
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
