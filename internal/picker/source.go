package picker

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	mod "github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
	"github.com/fm39hz/gotomux/internal/toolclass"
	"github.com/fm39hz/gotomux/internal/zoxide"
)

// FlattenFilter controls how flattenSources processes items from this source.
type FlattenFilter struct {
	Cap  int  // max items (0 = unlimited)
	Hide bool // hide all items when query is non-empty
}

type Source interface {
	Snapshot() []Item
	Refresh() tea.Cmd
	FlattenFilter(query string) FlattenFilter
}

type sourceMsg struct {
	src   Source
	items []Item
	// gen is the sourceCache generation the producing Refresh() captured at
	// dispatch time. Update drops the message when it no longer matches, so a
	// result computed before a kill/delete/freeze can never merge rows back
	// into the model after invalidate() removed them.
	gen uint64
}

type sourceCache struct {
	// seeded marks the caches as supplied by the daemon. When set, empty means
	// empty: sources must not "self-heal" by re-running tmux or SQLite.
	//
	// The old guards keyed off length (len(tmuxSnap) == 0), so a daemon that
	// legitimately reported zero sessions or zero presets silently reintroduced a
	// tmux fork and a SQLite query onto the path documented as performing
	// neither — and did so invisibly, because the result looked correct.
	seeded bool

	tmuxSnap   []tmux.LiveSession
	tmuxDone   atomic.Bool
	presetM    []store.PresetMeta
	presetDone atomic.Bool
	zoxMem     []Item
	zoxAt      time.Time
	zoxSig     string
	zoxMu      *sync.Mutex
	zoxSt      store.Storer
	zoxCap     int
	// gen counts invalidations. Every Refresh() stamps the value it saw at
	// dispatch; Update drops deliveries whose stamp no longer matches. One
	// field plus one compare — deliberately nothing else on any hot path.
	//
	// Single-writer discipline: only the UI goroutine writes gen (Update
	// handling a mutating action) and reads it (cmd creation also runs there).
	// Command closures carry the value away instead of re-reading it.
	gen uint64
}

// invalidate drops everything derived from an earlier read. Called after a
// mutating action (kill/delete/freeze), which also invalidates the daemon's
// payload — so seeded is cleared too and the next read goes to the source.
func (c *sourceCache) invalidate() {
	// Gen bumps before anything clears: a Refresh dispatched nanoseconds earlier
	// captured the pre-increment value, and it is the increment — not the
	// clearing below — that condemns that delivery when it finally arrives.
	c.gen++
	c.seeded = false
	c.tmuxDone.Store(false)
	c.presetDone.Store(false)
}

func (c *sourceCache) cap() int {
	if c.zoxCap > 0 {
		return c.zoxCap
	}
	return defaultZoxCap
}

func defaultSources(ctl tmux.Connector, st store.Storer, createName, createCwd string, cache *sourceCache) []Source {
	if !cache.seeded && !cache.tmuxDone.Load() {
		cache.tmuxDone.Store(true)
		cache.tmuxSnap = nil
		if ctl != nil {
			if live, err := ctl.ListLive(context.Background()); err == nil {
				cache.tmuxSnap = live
			}
		}
	}
	return []Source{
		&createSource{ctl: ctl, name: createName, cwd: createCwd, live: cache.tmuxSnap},
		&tmuxSource{live: cache.tmuxSnap},
		&presetSource{store: st, cache: cache},
		&zoxideSource{cache: cache},
	}
}

type createSource struct {
	ctl  tmux.Connector
	name string
	cwd  string
	live []tmux.LiveSession
}

func (s *createSource) Snapshot() []Item {
	if s.name == "" {
		return nil
	}
	projectTarget := mod.EntityIdentity{ProjectPath: s.cwd}
	for _, ls := range s.live {
		if projectTarget.SameProject(mod.EntityIdentity{LogicalID: ls.LogicalID, ServerKey: ls.ServerKey, RuntimeID: ls.ID, ProjectPath: ls.Path}) {
			return nil
		}
	}
	return []Item{{
		Kind:     KindCreate,
		Title:    fmt.Sprintf("[Create] %s", s.name),
		Desc:     s.cwd,
		Name:     s.name,
		Path:     s.cwd,
		Identity: projectTarget,
		Recency:  time.Now().Unix(),
	}}
}

func (s *createSource) Refresh() tea.Cmd { return nil }
func (s *createSource) FlattenFilter(query string) FlattenFilter {
	return FlattenFilter{Hide: query != ""}
}

type tmuxSource struct {
	live []tmux.LiveSession
}

func (s *tmuxSource) Snapshot() []Item {
	out := make([]Item, 0, len(s.live))
	for _, ls := range s.live {
		rec := ls.LastAttached
		if ls.Activity > rec {
			rec = ls.Activity
		}
		if ls.Created > rec {
			rec = ls.Created
		}
		busy := mkBusy(ls.ActiveCmd)
		out = append(out, Item{
			Kind:      KindActive,
			Title:     fmt.Sprintf("[Active] %s", ls.Name),
			Desc:      badgeFromBusy(busy),
			Name:      ls.Name,
			Path:      ls.Path,
			Identity:  mod.EntityIdentity{LogicalID: ls.LogicalID, ServerKey: ls.ServerKey, RuntimeID: ls.ID, ProjectPath: ls.Path},
			RuntimeID: ls.ID,
			ServerKey: ls.ServerKey,
			Windows:   ls.Windows,
			Recency:   rec,
		})
	}
	return out
}

func (s *tmuxSource) Refresh() tea.Cmd                   { return nil }
func (s *tmuxSource) FlattenFilter(string) FlattenFilter { return FlattenFilter{} }

type presetSource struct {
	store store.Storer
	cache *sourceCache
}

func (s *presetSource) Snapshot() []Item {
	var meta []store.PresetMeta
	switch {
	case s.cache.seeded || s.cache.presetDone.Load():
		meta = s.cache.presetM
	case s.store != nil:
		var err error
		meta, err = s.store.ListMeta()
		if err != nil {
			return nil
		}
		s.cache.presetM = meta
		s.cache.presetDone.Store(true)
	}
	if len(meta) == 0 {
		return nil
	}
	out := make([]Item, 0, len(meta))
	for _, m := range meta {
		out = append(out, Item{
			Kind:     KindPreset,
			Title:    fmt.Sprintf("[Preset] %s", m.Name),
			Desc:     "saved preset",
			Name:     m.Name,
			Path:     m.Cwd,
			Identity: mod.EntityIdentity{LogicalID: m.LogicalID},
			Recency:  m.LastUsed,
		})
	}
	return out
}

func (s *presetSource) Refresh() tea.Cmd                   { return nil }
func (s *presetSource) FlattenFilter(string) FlattenFilter { return FlattenFilter{} }

type zoxideSource struct {
	cache *sourceCache
}

// zoxRevalidate is how long the persisted rows are served before the background
// validator bothers re-querying zoxide. It is NOT an expiry: rows older than this
// are still painted immediately, they are just checked afterwards.
const zoxRevalidate = 30 * time.Second

func (s *zoxideSource) Snapshot() []Item {
	items, _, _, ok := loadZoxItemsSync(s.cache)
	if !ok {
		if s.cache.seeded {
			// The daemon owns zoxide in this mode; never exec from the hot path.
			return nil
		}
		// Nothing persisted yet — a genuinely first-ever run. Query synchronously so
		// even that first paint is complete, and accept the one-time ~340ms.
		//
		// This is the ONLY synchronous rebuild. It used to also fire whenever the
		// cache was older than 30s, which in practice meant almost every
		// invocation paid the full query plus a re-derivation of every path.
		items = rebuildZoxItems(s.cache)
		if len(items) == 0 {
			return nil
		}
	}
	// Return a copy. loadZoxItemsSync hands back the cache's own backing array,
	// and downstream code writes through it: applyRankMeta compacts in place
	// (items[n] = it; bySrc[key] = items[:n]) when filtering out the current
	// session. That shifted cache.zoxMem's elements, so the next Snapshot — after
	// any kill/delete/reload — saw duplicated rows and lost distinct ones. The
	// Kind write below was also unsynchronized against the refresh goroutine.
	out := slices.Clone(items)
	for i := range out {
		out[i].Kind = KindZoxide
	}
	return out
}

func (s *zoxideSource) FlattenFilter(query string) FlattenFilter {
	if query != "" {
		return FlattenFilter{}
	}
	return FlattenFilter{Cap: s.cache.cap()}
}

// Refresh validates the painted rows in the background.
//
// It re-queries zoxide off the render path and re-derives only if the path list
// changed, so the common case costs one fork and a string compare instead of
// ~270ms of project-root resolution. Returning nil items means "nothing changed",
// and Update skips the repaint — so an unchanged list never reorders the view
// under the user.
func (s *zoxideSource) Refresh() tea.Cmd {
	if s.cache.seeded {
		// The daemon revalidates on its own schedule; the client must not fork.
		return nil
	}
	src := Source(s)
	// Captured NOW, not inside the closure: invalidate() may run while this
	// command sits in flight, and the stamp must reflect the world the query
	// was launched from, not the world it lands in.
	gen := s.cache.gen
	return func() tea.Msg {
		s.cache.zoxMu.Lock()
		fresh := len(s.cache.zoxMem) > 0 && time.Since(s.cache.zoxAt) < zoxRevalidate
		sig := s.cache.zoxSig
		s.cache.zoxMu.Unlock()
		if fresh {
			return nil
		}
		return sourceMsg{src: src, items: validateZoxItems(s.cache, sig, zoxide.Query()), gen: gen}
	}
}

func snapshotAll(srcs []Source) map[Source][]Item {
	type slot struct {
		src   Source
		items []Item
	}
	ch := make(chan slot, len(srcs))
	for _, s := range srcs {
		s := s
		go func() {
			ch <- slot{s, s.Snapshot()}
		}()
	}
	out := make(map[Source][]Item, len(srcs))
	for range srcs {
		r := <-ch
		out[r.src] = r.items
	}
	return out
}

func refreshCmds(srcs []Source) []tea.Cmd {
	var cmds []tea.Cmd
	for _, s := range srcs {
		if c := s.Refresh(); c != nil {
			cmds = append(cmds, c)
		}
	}
	return cmds
}

func flattenSources(order []Source, bySrc map[Source][]Item, query string) []Item {
	q := query != ""
	var out []Item
	positions := map[string]int{}
	// Project navigation resolves an existing live session or saved recipe
	// before creating anything. Suppress that redundant navigation action while
	// retaining every independently identified session and recipe.
	coveredProjects := map[string]bool{}
	for _, items := range bySrc {
		for _, item := range items {
			if (item.Kind == KindActive || item.Kind == KindPreset) && item.Path != "" {
				coveredProjects[item.Host+"\x00"+normPath(item.Path)] = true
			}
		}
	}
	for _, s := range order {
		items := bySrc[s]
		ff := s.FlattenFilter(query)
		// Cap counts items that actually make the list. Applying it to the raw
		// slice first meant duplicates consumed budget and were then discarded,
		// so ZoxideCap=40 yielded fewer than 40 zoxide rows for no stated reason.
		kept := 0
		for _, it := range items {
			if (it.Kind == KindCreate || it.Kind == KindZoxide) && it.Path != "" && coveredProjects[it.Host+"\x00"+normPath(it.Path)] {
				continue
			}
			if ff.Hide && q {
				continue
			}
			if ff.Cap > 0 && kept >= ff.Cap {
				break
			}
			key := itemDedupKey(it)
			if pos, exists := positions[key]; exists {
				if sourcePriority(it.Kind) < sourcePriority(out[pos].Kind) {
					out[pos] = it
					kept++
				}
				continue
			}
			positions[key] = len(out)
			out = append(out, it)
			kept++
		}
	}
	return out
}

func itemDedupKey(item Item) string {
	scope := item.Host + "\x00"
	switch item.Kind {
	case KindActive:
		runtimeID, serverKey := item.Identity.RuntimeID, item.Identity.ServerKey
		if runtimeID == "" {
			runtimeID = item.RuntimeID
		}
		if serverKey == "" {
			serverKey = item.ServerKey
		}
		if runtimeID != "" && serverKey != "" {
			return scope + "live:" + serverKey + "\x00" + runtimeID
		}
		return scope + "live-name:" + item.Name
	case KindPreset:
		if item.Identity.LogicalID != "" {
			return scope + "recipe:" + item.Identity.LogicalID
		}
		return scope + "recipe-name:" + item.Name
	default:
		if path := normPath(item.Path); path != "" {
			return scope + "project:" + path
		}
		return scope + "project-name:" + item.Name
	}
}

func sourcePriority(kind Kind) int {
	switch kind {
	case KindActive:
		return 0
	case KindPreset:
		return 1
	case KindCreate:
		return 2
	case KindZoxide:
		return 3
	default:
		return int(kind) + 4
	}
}

func applyRankMeta(bySrc map[Source][]Item, st store.Storer, ctx Context) {
	var us map[string]store.Usage
	if len(ctx.Usage) > 0 {
		us = ctx.Usage
	} else if st != nil {
		us, _ = st.AllUsage()
	}
	var active []mod.SessionCandidate
	for _, items := range bySrc {
		for _, it := range items {
			if it.Kind == KindActive {
				active = append(active, mod.SessionCandidate{Name: it.Name, Identity: it.Identity})
			}
		}
	}

	for key, items := range bySrc {
		if len(us) > 0 {
			applyUsage(items, us, ctx.Now)
		}
		applyCooccur(items, ctx.Pairs)
		applyTransitions(items, ctx.Transitions)
		n := 0
		for _, it := range items {
			if ctx.Identity.SameEntity(it.Identity) || presetShadowedByLive(it, active) {
				continue
			}
			items[n] = it
			n++
		}
		bySrc[key] = items[:n]
	}
}

// A preset is an instantiation action. A live row with the same logical
// identity already provides the corresponding attach action. A same-name live
// session without a matching logical owner makes instantiation fail its
// identity preflight, so that unusable preset action is omitted as well. Name
// is used here only for tmux's namespace collision check, never to equate two
// session identities.
func presetShadowedByLive(item Item, active []mod.SessionCandidate) bool {
	if item.Kind != KindPreset {
		return false
	}
	return mod.ResolveSavedTarget(item.Name, item.Identity, active).Disposition != mod.TargetCreate
}

func mkBusy(cmd string) string {
	if cmd == "" {
		return ""
	}
	base := toolclass.Base(cmd)
	if base == "" || toolclass.IsShell(base) {
		return ""
	}
	if len(base) > 20 {
		base = base[:20]
	}
	return base
}

func badgeFromBusy(busy string) string {
	if busy == "" {
		return ""
	}
	return busy + " *"
}

func countSources(bySrc map[Source][]Item) int {
	n := 0
	for _, items := range bySrc {
		n += len(items)
	}
	return n
}
