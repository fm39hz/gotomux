package picker

import (
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
	"github.com/fm39hz/gotomux/internal/toolclass"
)

// Source IDs - stable keys for bySrc slots and future remote hosts.
const (
	SrcCreate = "create"
	SrcTmux   = "tmux"
	SrcPreset = "preset"
	SrcZoxide = "zoxide"
)

// Source feeds the picker. Snapshot seeds paint; Refresh is optional truth update.
type Source interface {
	ID() string
	Snapshot() []Item
	Refresh() tea.Cmd
}

// sourceMsg replaces one source slot after background truth fetch.
type sourceMsg struct {
	id    string
	items []Item
}

// tmuxSnapshot: share one live list across sources that need it.
var (
	tmuxSnapshot   []tmux.LiveSession
	tmuxSnapshotOK bool
	presetCache    []store.PresetMeta
	presetCacheOK  bool
)

// InvalidateCaches marks tmux and preset caches as stale so the next
// defaultSources call re-fetches fresh data.
func InvalidateCaches() {
	tmuxSnapshotOK = false
	presetCacheOK = false
}

// defaultSources order = dedup priority (first wins name/path).
func defaultSources(ctl *tmux.Ctl, st *store.Store, createName, createCwd string) []Source {
	// tmux snapshot: reuse cached unless invalidated
	if !tmuxSnapshotOK {
		tmuxSnapshot = nil
		tmuxSnapshotOK = true
		if ctl != nil {
			if live, err := ctl.ListLive(); err == nil && len(live) > 0 {
				tmuxSnapshot = live
			}
		}
	}
	// preset cache: reuse unless invalidated
	if !presetCacheOK && st != nil {
		presetCache = nil
		presetCacheOK = true
		if meta, err := st.ListMeta(); err == nil {
			presetCache = meta
		}
	}
	return []Source{
		&createSource{ctl: ctl, name: createName, cwd: createCwd, live: tmuxSnapshot},
		&tmuxSource{live: tmuxSnapshot},
		&presetSource{store: st, cached: presetCache},
		&zoxideSource{},
	}
}

// --- create ---

type createSource struct {
	ctl  *tmux.Ctl
	name string
	cwd  string
	live []tmux.LiveSession
}

func (s *createSource) ID() string { return SrcCreate }

func (s *createSource) Snapshot() []Item {
	if s.name == "" {
		return nil
	}
	// Inside tmux: hide Create, user already has a session (switch via active).
	if os.Getenv("TMUX") != "" {
		return nil
	}
	// Session already exists: hide Create
	for _, ls := range s.live {
		if ls.Name == s.name {
			return nil
		}
	}
	return []Item{{
		Src:   SrcCreate,
		Kind:  KindCreate,
		Title: fmt.Sprintf("[Create] %s", s.name),
		Desc:  s.cwd,
		Name:  s.name,
		Path:  s.cwd,
	}}
}

func (s *createSource) Refresh() tea.Cmd { return nil }

// --- local tmux (uses shared snapshot) ---

type tmuxSource struct {
	live []tmux.LiveSession
}

func (s *tmuxSource) ID() string { return SrcTmux }

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
			Src:     SrcTmux,
			Kind:    KindActive,
			Busy:    busy,
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

// --- presets ---

type presetSource struct {
	store  *store.Store
	cached []store.PresetMeta
}

func (s *presetSource) ID() string { return SrcPreset }

func (s *presetSource) Snapshot() []Item {
	meta := s.cached
	if meta == nil && s.store != nil {
		var err error
		meta, err = s.store.ListMeta()
		if err != nil {
			return nil
		}
	}
	out := make([]Item, 0, len(meta))
	for _, m := range meta {
		out = append(out, Item{
			Src:     SrcPreset,
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

// --- zoxide (cache paint + full truth refresh) ---

type zoxideSource struct{}

func (s *zoxideSource) ID() string { return SrcZoxide }

func (s *zoxideSource) Snapshot() []Item {
	items, _, ok := loadZoxItemsSync()
	if !ok {
		return nil
	}
	for i := range items {
		items[i].Src = SrcZoxide
		items[i].Kind = KindZoxide
	}
	return items
}

func (s *zoxideSource) Refresh() tea.Cmd {
	return func() tea.Msg {
		return sourceMsg{id: SrcZoxide, items: rebuildZoxItems()}
	}
}

// snapshotAll calls Snapshot on every source in parallel goroutines.
func snapshotAll(srcs []Source) map[string][]Item {
	type slot struct {
		id    string
		items []Item
	}
	ch := make(chan slot, len(srcs))
	for _, s := range srcs {
		s := s
		go func() {
			ch <- slot{s.ID(), s.Snapshot()}
		}()
	}
	out := make(map[string][]Item, len(srcs))
	for range srcs {
		r := <-ch
		out[r.id] = r.items
	}
	return out
}

// refreshCmds: all non-nil Refresh cmds.
func refreshCmds(srcs []Source) []tea.Cmd {
	var cmds []tea.Cmd
	for _, s := range srcs {
		if c := s.Refresh(); c != nil {
			cmds = append(cmds, c)
		}
	}
	return cmds
}

// flattenSources: source-order merge with name/path dedup (first wins).
func flattenSources(order []Source, bySrc map[string][]Item, query string) []Item {
	q := query != ""
	names := map[string]bool{}
	paths := map[string]bool{}
	var out []Item
	for _, s := range order {
		id := s.ID()
		items := bySrc[id]
		if id == SrcZoxide && !q {
			n := zoxCap
			if n > len(items) {
				n = len(items)
			}
			items = items[:n]
		}
		for _, it := range items {
			if id == SrcCreate && q {
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

// applyRankMeta overlays usage + cooccur on all slots.
func applyRankMeta(bySrc map[string][]Item, st *store.Store, pairs map[string]int64, ctxSession string) {
	now := time.Now().Unix()
	var us map[string]store.Usage
	if st != nil {
		us, _ = st.AllUsage()
	}
	for id, items := range bySrc {
		if len(us) > 0 {
			applyUsage(items, us, now)
		}
		applyCooccur(items, pairs)
		if ctxSession != "" && id == SrcTmux {
			for i := range items {
				if items[i].Name == ctxSession && items[i].Kind == KindActive {
					items[i].Recency = 0
				}
			}
		}
		bySrc[id] = items
	}
}

// countSources total raw items (pre-dedupe, pre-cap).
// mkBusy: any non-shell command -> busy marker. Empty otherwise.
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

// badgeFromBusy: "*" if busy tool, empty string if idle.
func badgeFromBusy(busy string) string {
	if busy == "" {
		return ""
	}
	return busy + " *"
}

func countSources(bySrc map[string][]Item) int {
	n := 0
	for _, items := range bySrc {
		n += len(items)
	}
	return n
}
