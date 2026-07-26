package picker

import (
	"sync"
	"testing"
	"time"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// fixture is one set of inputs, used to drive BOTH run modes: read locally in one
// case, handed over as a daemon payload in the other. Anything that makes the two
// disagree is a divergence, which is the failure this file exists to catch.
type fixture struct {
	sessions []tmux.LiveSession
	presets  []store.PresetMeta
	zox      []store.ZoxRow
	usage    map[string]store.Usage
}

func newFixture() fixture {
	return fixture{
		sessions: []tmux.LiveSession{
			{ID: "$0", Name: "alpha", Windows: 2, Path: "/w/alpha", LastAttached: 1000, Activity: 1100, Created: 900},
			{ID: "$1", Name: "beta", Windows: 1, Path: "/w/beta", Activity: 1050, Created: 1050},
		},
		presets: []store.PresetMeta{
			{Name: "gamma", Cwd: "/w/gamma", LastUsed: 800},
			{Name: "delta", Cwd: "/w/delta", LastUsed: 700},
		},
		zox: []store.ZoxRow{
			{Name: "eps", Path: "/w/eps", Title: "[Zoxide] eps", Recency: 3},
			{Name: "zeta", Path: "/w/zeta", Title: "[Zoxide] zeta", Recency: 2},
			{Name: "alpha", Path: "/w/alpha", Title: "[Zoxide] alpha", Recency: 1},
		},
		usage: map[string]store.Usage{
			"beta":  {Name: "beta", Opens: 5, LastOpen: 1200},
			"gamma": {Name: "gamma", Opens: 2, LastOpen: 1150},
		},
	}
}

func (f fixture) doubles() (*countingConnector, *countingStore) {
	return &countingConnector{live: f.sessions},
		&countingStore{presets: f.presets, zox: f.zox, usage: f.usage}
}

// seed mirrors what main.go assembles from a daemon.Response.
func (f fixture) seed(env *Context) Seed {
	return Seed{
		Sessions:    f.sessions,
		Presets:     f.presets,
		ZoxideItems: ZoxRowsToItems(f.zox),
		StickyLabel: "default",
		Env:         env,
	}
}

func itemKeys(items []Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, string(it.ID()))
	}
	return out
}

// TestPathParity is the structural guarantee behind "both run modes stay
// behaviorally identical". That invariant used to be a comment over three
// copy-pasted model literals with no test comparing them; the two paths could and
// did drift (git labels, co-occurrence, zoxide recency).
func TestPathParity(t *testing.T) {
	cfg := &config.Config{MaxShow: 12, ZoxideCap: 40, GitConcurrency: 4}

	for _, tc := range []struct {
		name    string
		session string
		path    string
		pairs   map[string]int64
	}{
		{name: "outside tmux"},
		{name: "inside tmux", session: "alpha", path: "/w/alpha",
			pairs: map[string]int64{"beta": 500, "gamma": 200}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()

			// Local path: sources read through the doubles.
			ctlA, stA := f.doubles()
			stA.pairs = tc.pairs
			ctlA.session, ctlA.path = tc.session, tc.path
			local := newModelCore(cfg, Deps{Ctl: ctlA, Store: stA}, "newproj", "/w/newproj", Seed{})

			// Daemon path: identical data, handed over instead of read.
			ctlB, stB := f.doubles()
			env := &Context{
				Session: tc.session, Path: tc.path,
				Pairs: tc.pairs, Usage: f.usage, Now: 0,
			}
			seeded := NewModelFromDaemon(cfg, Deps{Ctl: ctlB, Store: stB}, "newproj", "/w/newproj", f.seed(env))

			gotLocal, gotSeeded := itemKeys(local.ui.items), itemKeys(seeded.ui.items)
			if len(gotLocal) == 0 {
				t.Fatal("local path produced an empty list; fixture is not exercising anything")
			}
			if len(gotLocal) != len(gotSeeded) {
				t.Fatalf("list length differs: local %d %v vs seeded %d %v",
					len(gotLocal), gotLocal, len(gotSeeded), gotSeeded)
			}
			for i := range gotLocal {
				if gotLocal[i] != gotSeeded[i] {
					t.Errorf("order differs at %d: local %q vs seeded %q\nlocal:  %v\nseeded: %v",
						i, gotLocal[i], gotSeeded[i], gotLocal, gotSeeded)
				}
			}

			// Ranking inputs must match too, not just the final order — equal output
			// from different Recency/Cooccur would be luck, not parity.
			for i := range local.ui.items {
				a, b := local.ui.items[i], seeded.ui.items[i]
				if a.Recency != b.Recency {
					t.Errorf("%s: Recency %d vs %d", a.Name, a.Recency, b.Recency)
				}
				if a.Cooccur != b.Cooccur {
					t.Errorf("%s: Cooccur %d vs %d", a.Name, a.Cooccur, b.Cooccur)
				}
				if a.Kind != b.Kind {
					t.Errorf("%s: Kind %v vs %v", a.Name, a.Kind, b.Kind)
				}
			}
		})
	}
}

// TestPathParityUnderQuery covers the filtering path as well as the idle one.
func TestPathParityUnderQuery(t *testing.T) {
	cfg := &config.Config{MaxShow: 12, ZoxideCap: 40}
	f := newFixture()

	ctlA, stA := f.doubles()
	local := newModelCore(cfg, Deps{Ctl: ctlA, Store: stA}, "newproj", "/w/newproj", Seed{})

	ctlB, stB := f.doubles()
	seeded := NewModelFromDaemon(cfg, Deps{Ctl: ctlB, Store: stB}, "newproj", "/w/newproj",
		f.seed(&Context{Usage: f.usage}))

	for _, q := range []string{"a", "e", "ga", "zzzz"} {
		local.ui.queryInput.SetValue(q)
		local.refilterFromQuery()
		seeded.ui.queryInput.SetValue(q)
		seeded.refilterFromQuery()

		gotLocal, gotSeeded := itemKeys(local.ui.items), itemKeys(seeded.ui.items)
		if len(gotLocal) != len(gotSeeded) {
			t.Fatalf("query %q: %d vs %d items (%v / %v)", q, len(gotLocal), len(gotSeeded), gotLocal, gotSeeded)
		}
		for i := range gotLocal {
			if gotLocal[i] != gotSeeded[i] {
				t.Errorf("query %q: order differs at %d: %q vs %q", q, i, gotLocal[i], gotSeeded[i])
			}
		}
	}
}

// TestNoHotPathIO pins the daemon path's whole point: painting the list must not
// open the store or fork tmux. Both crept back in before — two display-message
// forks for the session context, a full store open for data already in the
// payload, and length-based cache guards that re-ran either one whenever the
// daemon legitimately reported nothing.
func TestNoHotPathIO(t *testing.T) {
	cfg := &config.Config{MaxShow: 12, ZoxideCap: 40}
	f := newFixture()
	ctl, _ := f.doubles()

	opened := 0
	deps := Deps{
		Ctl: ctl,
		OpenStore: func() store.Storer {
			opened++
			return nil
		},
	}

	m := NewModelFromDaemon(cfg, deps, "newproj", "/w/newproj",
		f.seed(&Context{Usage: f.usage}))
	if len(m.ui.items) == 0 {
		t.Fatal("seeded model painted nothing")
	}

	if opened != 0 {
		t.Errorf("store opened %d times while painting; the payload already has everything", opened)
	}
	if ctl.calls() != 0 {
		t.Errorf("tmux called %d times while painting (ListLive=%d CurrentSession=%d CurrentSessionPath=%d)",
			ctl.calls(), ctl.listLive, ctl.currentSession, ctl.currentPath)
	}

	// Typing must not reach for I/O either.
	m.ui.queryInput.SetValue("al")
	m.refilterFromQuery()
	if opened != 0 || ctl.calls() != 0 {
		t.Errorf("filtering performed I/O: opened=%d tmuxCalls=%d", opened, ctl.calls())
	}
}

// TestZoxideSnapshotDoesNotMutateCache pins the aliasing bug.
//
// Snapshot used to hand back the cache's own backing array. applyRankMeta then
// compacted that array in place to drop the current session (items[n] = it; n++),
// which shifted cache.zoxMem's elements without shortening it — so the next
// Snapshot, after any kill/delete/reload, returned a duplicated row and silently
// lost a distinct one until the 30s cache age expired.
func TestZoxideSnapshotDoesNotMutateCache(t *testing.T) {
	rows := []store.ZoxRow{
		{Name: "keep1", Path: "/k1", Recency: 3},
		{Name: "current", Path: "/cur", Recency: 2},
		{Name: "keep2", Path: "/k2", Recency: 1},
	}
	cache := &sourceCache{zoxMu: &sync.Mutex{}, zoxMem: ZoxRowsToItems(rows), zoxAt: time.Now()}
	src := &zoxideSource{cache: cache}

	first := src.Snapshot()
	if len(first) != 3 {
		t.Fatalf("first snapshot = %d items, want 3", len(first))
	}

	// Drop the "current session" row exactly the way the real pipeline does.
	bySrc := map[Source][]Item{src: first}
	applyRankMeta(bySrc, nil, Context{Session: "current", Path: "/cur", Now: 1})
	if got := len(bySrc[src]); got != 2 {
		t.Fatalf("after applyRankMeta = %d items, want 2", got)
	}

	second := src.Snapshot()
	if len(second) != 3 {
		t.Fatalf("second snapshot = %d items, want 3 — the cache was truncated", len(second))
	}
	for i := range first {
		if second[i].Name != rows[i].Name {
			t.Errorf("cache corrupted at %d: got %q, want %q (full: %v)",
				i, second[i].Name, rows[i].Name, itemNames(second))
		}
	}
	seen := map[string]bool{}
	for _, it := range second {
		if seen[it.Name] {
			t.Errorf("duplicate %q in second snapshot: %v", it.Name, itemNames(second))
		}
		seen[it.Name] = true
	}
}

// TestSeededZoxideNeverExecs guards the other half: with a seeded cache the
// zoxide source must not shell out, even when the payload carried no rows.
func TestSeededZoxideNeverExecs(t *testing.T) {
	cache := &sourceCache{zoxMu: &sync.Mutex{}, seeded: true}
	src := &zoxideSource{cache: cache}
	if got := src.Snapshot(); len(got) != 0 {
		t.Errorf("seeded zoxide source produced %d items from an empty payload", len(got))
	}
	if cache.zoxSt != nil {
		t.Error("seeded zoxide source should not have acquired a store")
	}
}
