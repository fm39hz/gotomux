package picker

import (
	"fmt"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
	"github.com/fm39hz/gotomux/internal/toolclass"
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
}

type sourceCache struct {
	tmuxSnap []tmux.LiveSession
	tmuxOK   bool
	presetM  []store.PresetMeta
	presetOK bool
	zoxMem   []Item
	zoxAt    time.Time
	zoxMu    *sync.Mutex
	zoxSt    *store.Store
}

func (c *sourceCache) invalidate() {
	c.tmuxOK = false
	c.presetOK = false
}

var sessionSnap []tmux.LiveSession

func CachedLiveSessions() []tmux.LiveSession { return sessionSnap }

func defaultSources(ctl tmux.Connector, st *store.Store, createName, createCwd string, cache *sourceCache) []Source {
	if !cache.tmuxOK {
		cache.tmuxSnap = nil
		cache.tmuxOK = true
		if ctl != nil {
			if live, err := ctl.ListLive(); err == nil && len(live) > 0 {
				cache.tmuxSnap = live
			}
		}
	}
	sessionSnap = cache.tmuxSnap
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
	for _, ls := range s.live {
		if ls.Name == s.name {
			return nil
		}
	}
	return []Item{{

		Kind:    KindCreate,
		Title:   fmt.Sprintf("[Create] %s", s.name),
		Desc:    s.cwd,
		Name:    s.name,
		Path:    s.cwd,
		Recency: time.Now().Unix(),
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
			Kind:    KindActive,
			Title:   fmt.Sprintf("[Active] %s", ls.Name),
			Desc:    badgeFromBusy(busy),
			Name:    ls.Name,
			Path:    ls.Path,
			Windows: ls.Windows,
			Recency: rec,
		})
	}
	return out
}

func (s *tmuxSource) Refresh() tea.Cmd { return nil }
func (s *tmuxSource) FlattenFilter(string) FlattenFilter { return FlattenFilter{} }

type presetSource struct {
	store *store.Store
	cache *sourceCache
}

func (s *presetSource) Snapshot() []Item {
	var meta []store.PresetMeta
	if s.cache.presetOK {
		meta = s.cache.presetM
	} else if s.store != nil {
		var err error
		meta, err = s.store.ListMeta()
		if err != nil {
			return nil
		}
		s.cache.presetM = meta
		s.cache.presetOK = true
	}
	if len(meta) == 0 {
		return nil
	}
	out := make([]Item, 0, len(meta))
	for _, m := range meta {
		out = append(out, Item{
			Kind:    KindPreset,
			Title:   fmt.Sprintf("[Preset] %s", m.Name),
			Desc:    "saved preset",
			Name:    m.Name,
			Path:    m.Cwd,
			Recency: m.LastUsed,
		})
	}
	return out
}

func (s *presetSource) Refresh() tea.Cmd { return nil }
func (s *presetSource) FlattenFilter(string) FlattenFilter { return FlattenFilter{} }

type zoxideSource struct {
	cache *sourceCache
}

func (s *zoxideSource) Snapshot() []Item {
	items, _, ok := loadZoxItemsSync(s.cache)
	if !ok {
		return nil
	}
	for i := range items {
		items[i].Kind = KindZoxide
	}
	return items
}

func (s *zoxideSource) FlattenFilter(query string) FlattenFilter {
	if query != "" {
		return FlattenFilter{}
	}
	return FlattenFilter{Cap: zoxCap}
}

func (s *zoxideSource) Refresh() tea.Cmd {
	src := Source(s)
	return func() tea.Msg {
		return sourceMsg{src: src, items: rebuildZoxItems(s.cache)}
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
	names := map[string]bool{}
	paths := map[string]bool{}
	var out []Item
	for _, s := range order {
		items := bySrc[s]
		ff := s.FlattenFilter(query)
		if ff.Cap > 0 && ff.Cap < len(items) {
			items = items[:ff.Cap]
		}
		for _, it := range items {
			if ff.Hide && q {
				continue
			}
			nr := normPath(it.Path)
			if names[it.Name] || (nr != "" && paths[nr]) {
				continue
			}
			names[it.Name] = true
			if nr != "" {
				paths[nr] = true
			}
			out = append(out, it)
		}
	}
	return out
}

func applyRankMeta(bySrc map[Source][]Item, st *store.Store, ctx Context) {
	var us map[string]store.Usage
	if len(ctx.Usage) > 0 {
		us = ctx.Usage
	} else if st != nil {
		us, _ = st.AllUsage()
	}

	for key, items := range bySrc {
		if len(us) > 0 {
			applyUsage(items, us, ctx.Now)
		}
		applyCooccur(items, ctx.Pairs)
		if ctx.HasSession() {
			n := 0
			for _, it := range items {
				if it.Name == ctx.Session || (ctx.Path != "" && it.Path == ctx.Path) {
					continue
				}
				items[n] = it
				n++
			}
			bySrc[key] = items[:n]
		}
	}
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
