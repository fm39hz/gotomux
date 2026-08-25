package picker

import (
	"testing"

	"github.com/fm39hz/gotomux/internal/config"
)

// P1.2 staleness guard: an async result must not outlive the invalidation that
// condemned it. The bug shape: kill/freeze/delete calls invalidate()+reload()
// and repaints from a clean snapshot; a sourceMsg/gitDoneMsg dispatched BEFORE
// the mutation lands afterwards and merges the dead row (or stale label) back
// into the model, where it survives until the next refilter.

func staleTestModel(t *testing.T) model {
	t.Helper()
	f := newFixture()
	ctl, st := f.doubles()
	return newModelCore(&config.Config{MaxShow: 12, ZoxideCap: 40, GitConcurrency: 4},
		Deps{Ctl: ctl, Store: st}, "newproj", "/w/newproj", Seed{})
}

func zoxSourceOf(m model) *zoxideSource {
	for _, s := range m.sources {
		if z, ok := s.(*zoxideSource); ok {
			return z
		}
	}
	return nil
}

// TestStaleSourceMsgDropped pins the acceptance scenario end to end at the
// dispatch boundary: Refresh() runs at dispatch (that is when it captures the
// generation), the command sits in flight while a mutating action bumps gen,
// and its eventual delivery carries a dead row that must not reach ui.items.
//
// The delivered message reproduces byte-for-byte what the in-flight closure
// returns: sourceMsg stamped with the gen the dispatch captured — read here
// from the very field the closure copies. Executing the real closure instead
// would fork the developer's actual zoxide binary, which no unit test may
// depend on (see TestSeededZoxideNeverExecs for the same discipline).
func TestStaleSourceMsgDropped(t *testing.T) {
	m := staleTestModel(t)
	zs := zoxSourceOf(m)
	if zs == nil {
		t.Fatal("no zoxide source registered; dispatch cannot race anything")
	}

	cmds := refreshCmds(m.sources)
	if len(cmds) == 0 {
		t.Fatal("no refresh command dispatched")
	}

	before := itemKeys(m.ui.items)
	staleGen := m.cache.gen

	// The mutating action fires while the command is in flight.
	m.cache.invalidate()
	if m.cache.gen != staleGen+1 {
		t.Fatalf("invalidate left gen at %d, want %d", m.cache.gen, staleGen+1)
	}

	stale := sourceMsg{
		src:   zs, // the registered source: identity matters for mergeSource
		items: []Item{{Kind: KindActive, Name: "dead-session", Path: "/w/dead", Recency: 1 << 40}},
		gen:   staleGen,
	}
	next, _ := m.Update(stale)
	got := next.(model)

	if keys := itemKeys(got.ui.items); len(keys) != len(before) {
		t.Fatalf("stale delivery changed the list: %v -> %v", before, keys)
	}
	for i, it := range got.ui.items {
		if it.Name == "dead-session" {
			t.Errorf("dead row resurrected at %d: %v", i, itemKeys(got.ui.items))
		}
		if it.Name != before[i] {
			t.Errorf("row %d changed: %q vs original %q", i, it.Name, before[i])
		}
	}

	// Control: the same payload carrying the CURRENT generation must merge.
	// The guard blocks staleness, not background updates themselves.
	fresh := stale
	fresh.gen = got.cache.gen
	next2, _ := got.Update(fresh)
	got2 := next2.(model)
	found := false
	for _, it := range got2.ui.items {
		if it.Name == "dead-session" {
			found = true
		}
	}
	if !found {
		t.Error("current-generation delivery was dropped; the guard over-blocks")
	}
}

// TestGitDoneMsgCarriesDispatchGeneration exercises the REAL dispatch ->
// execute -> stamp chain (gitinfo.Label is plain file reads, no fork) and the
// matching drop rule, with an outcome observable in both directions: a stale
// relabel must not run, a current one must.
func TestGitDoneMsgCarriesDispatchGeneration(t *testing.T) {
	const probe = "/stale-gen-probe/repo"
	t.Cleanup(func() { gitBranchCache.LoadAndDelete(probe) })
	gitBranchCache.Store(probe, "feat-x")

	m := staleTestModel(t)
	// Point one painted row at the probe path so a relabel has an observable
	// effect. Set the label directly rather than via refilter(): refilter
	// rebuilds ui.items from bySrc and would discard the path rewrite.
	m.ui.items[0].Path = probe
	m.ui.items[0].GitBranch = "feat-x"

	cmd := m.enrichRestCmd()
	if cmd == nil {
		t.Fatal("standalone model returned no enrich command")
	}
	dispatchGen := m.cache.gen

	// Mutation fires and the label changes while the command flies (as if the
	// repo switched branches). Overwrite rather than delete: setGitBranch is a
	// deliberate no-op on a cache miss, so deletion would make even a correctly
	// applied message look like a drop.
	m.cache.invalidate()
	gitBranchCache.Store(probe, "rebased-main")

	msg := cmd()
	gd, ok := msg.(gitDoneMsg)
	if !ok {
		t.Fatalf("enrich command produced %T, want gitDoneMsg", msg)
	}
	if gd.gen != dispatchGen {
		t.Fatalf("gitDoneMsg.gen = %d, want the dispatch-time value %d — the closure re-read gen at execution time", gd.gen, dispatchGen)
	}

	next, _ := m.Update(gd)
	got := next.(model)
	if got.ui.items[0].GitBranch != "feat-x" {
		t.Errorf("stale gitDoneMsg was applied: GitBranch = %q, want untouched feat-x", got.ui.items[0].GitBranch)
	}

	// Control: a current-generation message re-applies labels from the cache,
	// so the row must pick up the new label.
	next2, _ := got.Update(gitDoneMsg{gen: got.cache.gen})
	got2 := next2.(model)
	if got2.ui.items[0].GitBranch != "rebased-main" {
		t.Errorf("current gitDoneMsg dropped: GitBranch = %q, want rebased-main", got2.ui.items[0].GitBranch)
	}
}

// TestNilCacheDropsAsyncMessages: messages delivered to a model built with no
// cache cannot be current by definition — they must be dropped, not dereferenced.
func TestNilCacheDropsAsyncMessages(t *testing.T) {
	m := model{}
	next, _ := m.Update(sourceMsg{items: []Item{{Name: "x"}}, gen: 7})
	if got := next.(model); len(got.ui.items) != 0 {
		t.Error("nil-cache model merged a sourceMsg")
	}
	next2, _ := m.Update(gitDoneMsg{gen: 7})
	_ = next2 // must not panic; nothing to observe on an empty model
}
