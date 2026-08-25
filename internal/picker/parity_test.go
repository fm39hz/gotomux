package picker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fm39hz/gotomux/internal/config"
	"github.com/fm39hz/gotomux/internal/daemon"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
	"github.com/fm39hz/gotomux/internal/zoxide"
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
		sessID  string
		session string
		path    string
		pairs   map[string]int64
	}{
		{name: "outside tmux"},
		{name: "inside tmux", sessID: "$0", session: "alpha", path: "/w/alpha",
			pairs: map[string]int64{"beta": 500, "gamma": 200}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture()

			// Local path: sources read through the doubles.
			ctlA, stA := f.doubles()
			stA.pairs = tc.pairs
			ctlA.session, ctlA.path = tc.session, tc.path
			// SessionID rather than $TMUX: the fixture decides which session we are
			// "in", so the test result does not depend on where it runs.
			local := newModelCore(cfg, Deps{Ctl: ctlA, Store: stA, SessionID: tc.sessID},
				"newproj", "/w/newproj", Seed{})

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

// TestZoxideSnapshotServesStaleRows pins the cold-start fix.
//
// Rows persisted in zox_item used to be discarded once older than 30 seconds,
// which forced a full `zoxide query -l` (≈50ms) plus re-deriving a project root
// per path (≈0.9ms each, ≈270ms for 300 entries) on essentially every invocation,
// because 30s is shorter than the gap between two picker opens. Age must not gate
// the paint; only an empty cache may trigger a synchronous rebuild.
func TestZoxideSnapshotServesStaleRows(t *testing.T) {
	st := &countingStore{
		zox: []store.ZoxRow{
			{Name: "old1", Path: "/o1", Recency: 2},
			{Name: "old2", Path: "/o2", Recency: 1},
		},
		zoxSig: "sig-abc",
	}
	// zoxAt far in the past; zoxMem empty so the store is consulted.
	cache := &sourceCache{zoxMu: &sync.Mutex{}, zoxSt: st, zoxAt: time.Now().Add(-24 * time.Hour)}
	src := &zoxideSource{cache: cache}

	got := src.Snapshot()
	if len(got) != 2 {
		t.Fatalf("Snapshot = %d items, want 2 — stale rows were discarded", len(got))
	}
	if got[0].Name != "old1" || got[1].Name != "old2" {
		t.Errorf("Snapshot returned %v, not the persisted rows", itemNames(got))
	}
	if st.loadZox != 1 {
		t.Errorf("LoadZox called %d times, want exactly 1", st.loadZox)
	}
	if cache.zoxSig != "sig-abc" {
		t.Errorf("signature not carried into the cache: %q", cache.zoxSig)
	}
}

// TestValidateSkipsDerivationOnSameSignature: the background validator must not
// re-derive when the zoxide list is unchanged, and must report "no change" so the
// view is never repainted — and therefore never reordered — for nothing.
func TestValidateSkipsDerivationOnSameSignature(t *testing.T) {
	paths := []string{"/p/one", "/p/two", "/p/three"}
	sig := zoxide.Signature(paths)
	original := []Item{{Name: "one", Path: "/p/one"}}

	cache := &sourceCache{
		zoxMu:  &sync.Mutex{},
		zoxMem: original,
		zoxSig: sig,
		zoxAt:  time.Now().Add(-time.Hour),
	}

	got := validateZoxItems(cache, sig, paths)
	if got != nil {
		t.Errorf("matching signature returned %d items; want nil so the caller skips the repaint", len(got))
	}
	if len(cache.zoxMem) != 1 || cache.zoxMem[0].Name != "one" {
		t.Errorf("rows were re-derived despite an unchanged list: %v", itemNames(cache.zoxMem))
	}
	if time.Since(cache.zoxAt) > time.Minute {
		t.Error("freshness stamp not bumped; the validator would re-run on every keystroke cycle")
	}

	// A changed list must be re-derived. These paths do not exist, so Rows yields
	// whatever FindProjectRoot returns for them — the point is that it ran.
	changed := append(paths, "/p/four")
	if got := validateZoxItems(cache, sig, changed); got == nil {
		t.Error("changed list returned nil; the new entry would never appear")
	}
	if cache.zoxSig == sig {
		t.Error("signature not updated after re-derivation")
	}
}

// --- dual-mode parity matrix (P1.3) ---
//
// The original two scenarios (idle + typed query) guarded the plumbing, not the
// ranking inputs. Each row below varies exactly ONE input dimension and runs
// BOTH modes: standalone reads through the doubles; daemon mode pushes an
// identical dataset through a real wire encoding first — json.Marshal(daemon.
// Response) -> Unmarshal -> NewModelFromDaemon — because tag drift, field
// order, and nil-vs-empty-slice divergence historically hid exactly there
// (git labels, zoxide recency: see TestPathParity's comment).

// parityInputs is everything a scenario may vary besides the base fixture.
type parityInputs struct {
	sessID      string            // Deps.SessionID; "" = outside tmux
	session     string            // current session name the connector reports
	path        string            // its path
	pairs       map[string]int64  // co-occurrence scores
	transitions map[string]int64  // directed switch scores
	branches    map[string]string // daemon-supplied git labels
	query       string            // typed into both models after build
}

type parityScenario struct {
	name  string
	edit  func(t *testing.T, f *fixture) parityInputs
	check func(t *testing.T, f fixture, in parityInputs, local, seeded model)
}

// roundTripResponse forces the payload through the wire encoding.
func roundTripResponse(t *testing.T, resp daemon.Response) daemon.Response {
	t.Helper()
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var out daemon.Response
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return out
}

// payloadPathsOf mirrors main.go's payloadPaths: every path the payload
// mentions — the exact set the daemon resolved git labels for.
func payloadPathsOf(resp daemon.Response) []string {
	out := make([]string, 0, len(resp.Sessions)+len(resp.Presets)+len(resp.Zoxide))
	for _, s := range resp.Sessions {
		out = append(out, s.Path)
	}
	for _, p := range resp.Presets {
		out = append(out, p.Cwd)
	}
	for _, z := range resp.Zoxide {
		out = append(out, z.Path)
	}
	return out
}

// seedFromResponse mirrors main.go's runPickerIPC Seed assembly.
func seedFromResponse(resp daemon.Response, sessID string) Seed {
	env := Context{Pairs: resp.Pairs, Transitions: resp.Transitions, Usage: resp.Usage, Now: time.Now().Unix()}
	if cur, ok := tmux.FindByID(resp.Sessions, sessID); ok {
		env.Session, env.Path = cur.Name, cur.Path
	}
	return Seed{
		Sessions:    resp.Sessions,
		Presets:     resp.Presets,
		ZoxideItems: ZoxRowsToItems(resp.Zoxide),
		StickyLabel: resp.StickyLabel,
		Env:         &env,
	}
}

// assertParityRows demands the full tuple, not just presence: identical keys,
// identical order, and identical ranking metadata per row — equal output from
// different inputs would be luck, not parity.
func assertParityRows(t *testing.T, label string, local, seeded model) {
	t.Helper()
	gotL, gotS := itemKeys(local.ui.items), itemKeys(seeded.ui.items)
	if len(gotL) == 0 {
		t.Fatalf("%s: local list empty; scenario exercises nothing", label)
	}
	if len(gotL) != len(gotS) {
		t.Fatalf("%s: length differs:\nlocal:  %v\nseeded: %v", label, gotL, gotS)
	}
	for i := range gotL {
		a, b := local.ui.items[i], seeded.ui.items[i]
		if gotL[i] != gotS[i] {
			t.Errorf("%s: order differs at %d: local %q vs seeded %q\nlocal:  %v\nseeded: %v",
				label, i, gotL[i], gotS[i], gotL, gotS)
			continue
		}
		if a.Kind != b.Kind {
			t.Errorf("%s: %s Kind %v vs %v", label, a.Name, a.Kind, b.Kind)
		}
		if a.Recency != b.Recency {
			t.Errorf("%s: %s Recency %d vs %d", label, a.Name, a.Recency, b.Recency)
		}
		if a.Cooccur != b.Cooccur {
			t.Errorf("%s: %s Cooccur %d vs %d", label, a.Name, a.Cooccur, b.Cooccur)
		}
		if a.Transition != b.Transition {
			t.Errorf("%s: %s Transition %d vs %d", label, a.Name, a.Transition, b.Transition)
		}
		if a.GitBranch != b.GitBranch {
			t.Errorf("%s: %s GitBranch %q vs %q", label, a.Name, a.GitBranch, b.GitBranch)
		}
	}
}

func indexOfName(items []Item, name string) int {
	for i, it := range items {
		if it.Name == name {
			return i
		}
	}
	return -1
}

func TestDualModeParityMatrix(t *testing.T) {
	cfg := &config.Config{MaxShow: 12, ZoxideCap: 40, GitConcurrency: 4}

	var gitRepoA, gitRepoB string // set by the git-labels scenario

	scenarios := []parityScenario{
		{
			name: "idle_outside_tmux",
			edit: func(*testing.T, *fixture) parityInputs { return parityInputs{} },
		},
		{
			name: "inside_tmux_drops_current_session_and_path",
			edit: func(*testing.T, *fixture) parityInputs {
				return parityInputs{
					sessID: "$0", session: "alpha", path: "/w/alpha",
					pairs:       map[string]int64{"beta": 500, "gamma": 200},
					transitions: map[string]int64{"beta": 300},
				}
			},
			check: func(t *testing.T, _ fixture, in parityInputs, local, seeded model) {
				// Parity alone would not catch both modes silently STOPPING
				// the drop; assert the rule itself in each mode.
				for label, m := range map[string]model{"local": local, "seeded": seeded} {
					for _, it := range m.ui.items {
						if it.Name == in.session || normPath(it.Path) == normPath(in.path) {
							t.Errorf("%s: current-session row leaked into the list: %+v", label, it)
						}
					}
				}
			},
		},
		{
			name: "transition_breaks_recency_tie",
			edit: func(_ *testing.T, f *fixture) parityInputs {
				// Equal LastUsed and p2 earlier in input order: without the
				// directed switch score, idx would keep p2 ahead — so the
				// ordering assertion below fails if transitions stop counting.
				f.presets = []store.PresetMeta{
					{Name: "p2", Cwd: "/w/p2", LastUsed: 800},
					{Name: "p1", Cwd: "/w/p1", LastUsed: 800},
				}
				return parityInputs{
					sessID: "$0", session: "alpha", path: "/w/alpha",
					transitions: map[string]int64{"p1": 950},
				}
			},
			check: func(t *testing.T, _ fixture, _ parityInputs, local, seeded model) {
				for label, m := range map[string]model{"local": local, "seeded": seeded} {
					i1, i2 := indexOfName(m.ui.items, "p1"), indexOfName(m.ui.items, "p2")
					if i1 < 0 || i2 < 0 {
						t.Fatalf("%s: p1@%d p2@%d — scenario lost its subjects", label, i1, i2)
					}
					if i1 > i2 {
						t.Errorf("%s: p1 (trans 950) ranks below p2 (trans 0): %d vs %d — transition score not applied",
							label, i1, i2)
					}
				}
			},
		},
		{
			name: "dedup_preset_first_wins_over_zoxide",
			edit: func(_ *testing.T, f *fixture) parityInputs {
				// Same name AND normalized path as preset gamma; sources run
				// create->tmux->preset->zoxide, so the zoxide twin must lose.
				f.zox = append(f.zox, store.ZoxRow{Name: "gamma", Path: "/w/gamma/", Title: "[Zoxide] gamma", Recency: 99})
				return parityInputs{}
			},
			check: func(t *testing.T, _ fixture, _ parityInputs, local, seeded model) {
				for label, m := range map[string]model{"local": local, "seeded": seeded} {
					n := 0
					for _, it := range m.ui.items {
						if it.Name == "gamma" {
							n++
							if it.Kind != KindPreset {
								t.Errorf("%s: gamma survived as Kind %v, want the preset slot", label, it.Kind)
							}
						}
					}
					if n != 1 {
						t.Errorf("%s: gamma appears %d times, want exactly 1 (first-wins dedup)", label, n)
					}
				}
			},
		},
		{
			name: "git_labels_parity",
			edit: func(t *testing.T, f *fixture) parityInputs {
				// gitinfo.Label reads .git/HEAD directly (no git subprocess), so
				// a hand-written HEAD makes both modes resolve real labels.
				gitRepoA = filepath.Join(t.TempDir(), "repo-a")
				gitRepoB = filepath.Join(t.TempDir(), "repo-b")
				for repo, branch := range map[string]string{gitRepoA: "main", gitRepoB: "feature/x"} {
					if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"),
						[]byte("ref: refs/heads/"+branch+"\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				f.presets[0].Cwd = gitRepoA // gamma lives in repo-a
				f.zox[0].Path = gitRepoB    // eps resolves to repo-b
				return parityInputs{branches: map[string]string{gitRepoA: "main", gitRepoB: "feature/x"}}
			},
			check: func(t *testing.T, _ fixture, _ parityInputs, local, seeded model) {
				for label, m := range map[string]model{"local": local, "seeded": seeded} {
					for _, it := range m.ui.items {
						switch it.Name {
						case "gamma":
							if it.Path != gitRepoA || it.GitBranch != "main" {
								t.Errorf("%s: gamma = (%s, %q), want repo-a + main", label, it.Path, it.GitBranch)
							}
						case "eps":
							if it.Path != gitRepoB || it.GitBranch != "feature/x" {
								t.Errorf("%s: eps = (%s, %q), want repo-b + feature/x", label, it.Path, it.GitBranch)
							}
						case "beta":
							if it.GitBranch != "" {
								t.Errorf("%s: non-repo row invented label %q", label, it.GitBranch)
							}
						}
					}
				}
			},
		},
		{
			name: "query_multi_token_and",
			edit: func(*testing.T, *fixture) parityInputs {
				// "w" only hits path segments (/w/...), "al" only the name:
				// survival requires the AND across both tokens, and alpha is
				// the sole row satisfying both.
				return parityInputs{query: "w al"}
			},
			check: func(t *testing.T, _ fixture, in parityInputs, local, seeded model) {
				_ = in
				for label, m := range map[string]model{"local": local, "seeded": seeded} {
					keys := itemKeys(m.ui.items)
					if len(keys) != 1 || keys[0] != "alpha" {
						t.Errorf("%s: multi-token AND yielded %v, want [alpha]", label, keys)
					}
				}
			},
		},
		{
			name: "empty_sessions_and_presets_stay_empty",
			edit: func(_ *testing.T, f *fixture) parityInputs {
				// Seeded empties mean empty — the length-based guards of the
				// past silently re-read tmux/store here and repainted ghosts.
				f.sessions = nil
				f.presets = nil
				return parityInputs{}
			},
			check: func(t *testing.T, _ fixture, _ parityInputs, local, seeded model) {
				for label, m := range map[string]model{"local": local, "seeded": seeded} {
					create, zox := 0, 0
					for _, it := range m.ui.items {
						switch it.Kind {
						case KindActive, KindPreset:
							t.Errorf("%s: ghost row %+v survived an empty payload", label, it)
						case KindCreate:
							create++
						case KindZoxide:
							zox++
						}
					}
					if create != 1 || zox != 3 {
						t.Errorf("%s: create=%d zox=%d, want 1/3", label, create, zox)
					}
				}
			},
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			f := newFixture()
			in := sc.edit(t, &f)

			// Mode A — standalone: sources read through the doubles.
			ctlA, stA := f.doubles()
			stA.pairs, stA.transitions = in.pairs, in.transitions
			ctlA.session, ctlA.path = in.session, in.path
			local := newModelCore(cfg, Deps{Ctl: ctlA, Store: stA, SessionID: in.sessID},
				"newproj", "/w/newproj", Seed{})

			// Mode B — daemon: identical data through the wire encoding first,
			// then preloaded exactly the way runPickerIPC does it.
			resp := roundTripResponse(t, daemon.Response{
				Sessions:    f.sessions,
				Presets:     f.presets,
				Pairs:       in.pairs,
				Transitions: in.transitions,
				StickyLabel: "default",
				Usage:       f.usage,
				GitBranches: in.branches,
				Zoxide:      f.zox,
			})
			PreloadCache(resp.GitBranches)
			PreloadMisses(payloadPathsOf(resp), resp.GitBranches)
			ctlB, stB := f.doubles()
			seeded := NewModelFromDaemon(cfg, Deps{Ctl: ctlB, Store: stB},
				"newproj", "/w/newproj", seedFromResponse(resp, in.sessID))

			if in.query != "" {
				local.ui.queryInput.SetValue(in.query)
				local.refilterFromQuery()
				seeded.ui.queryInput.SetValue(in.query)
				seeded.refilterFromQuery()
			}

			assertParityRows(t, sc.name, local, seeded)
			if sc.check != nil {
				sc.check(t, f, in, local, seeded)
			}
		})
	}
}
