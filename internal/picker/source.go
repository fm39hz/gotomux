package picker

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// Source IDs — stable keys for bySrc slots and future remote hosts.
const (
	SrcCreate = "create"
	SrcTmux   = "tmux"
	SrcPreset = "preset"
	SrcZoxide = "zoxide"
)

// Source feeds the picker. Snapshot seeds paint; Refresh is optional truth update.
// Add a source: implement this + register in defaultSources; connect via kind/src.
type Source interface {
	ID() string
	Snapshot() []Item
	// Refresh: Cmd → sourceMsg; nil means Snapshot is already complete.
	Refresh() tea.Cmd
}

// sourceMsg replaces one source slot after background truth fetch.
type sourceMsg struct {
	id    string
	items []Item
}

// defaultSources order = dedup priority (first wins name/path).
func defaultSources(ctl *tmux.Ctl, store *store.Store, createName, createCwd string) []Source {
	return []Source{
		&createSource{ctl: ctl, name: createName, cwd: createCwd},
		&tmuxSource{ctl: ctl},
		&presetSource{store: store},
		&zoxideSource{},
	}
}

// --- create ---

type createSource struct {
	ctl  *tmux.Ctl
	name string
	cwd  string
}

func (s *createSource) ID() string { return SrcCreate }

func (s *createSource) Snapshot() []Item {
	if s.name == "" {
		return nil
	}
	// Inside tmux: no Create — cwd spin-up is for outside; jump via zoxide/active.
	if s.ctl != nil && s.ctl.CurrentSession() != "" {
		return nil
	}
	if s.ctl != nil {
		if live, err := s.ctl.ListLive(); err == nil {
			for _, ls := range live {
				if ls.Name == s.name {
					return nil
				}
			}
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

// --- local tmux ---

type tmuxSource struct{ ctl *tmux.Ctl }

func (s *tmuxSource) ID() string { return SrcTmux }

func (s *tmuxSource) Snapshot() []Item {
	if s.ctl == nil {
		return nil
	}
	live, err := s.ctl.ListLive()
	if err != nil {
		return nil
	}
	out := make([]Item, 0, len(live))
	for _, ls := range live {
		// Recency: max(last_attached, activity, created) — "just left" / hot session
		rec := ls.LastAttached
		if ls.Activity > rec {
			rec = ls.Activity
		}
		if ls.Created > rec {
			rec = ls.Created
		}
		out = append(out, Item{
			Src:     SrcTmux,
			Kind:    KindActive,
			Title:   fmt.Sprintf("[Active] %s", ls.Name),
			Desc:    fmt.Sprintf("%d windows", ls.Windows),
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

type presetSource struct{ store *store.Store }

func (s *presetSource) ID() string { return SrcPreset }

func (s *presetSource) Snapshot() []Item {
	if s.store == nil {
		return nil
	}
	meta, err := s.store.ListMeta()
	if err != nil {
		return nil
	}
	out := make([]Item, 0, len(meta))
	for _, m := range meta {
		out = append(out, Item{
			Src:     SrcPreset,
			Kind:    KindPreset,
			Title:   fmt.Sprintf("[Preset] %s", m.Name),
			Desc:    "saved layout",
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
	// ensure src tag (older cache rows may lack it)
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

// snapshotAll: ordered Snapshot from every source (raw, no cross-dedupe).
func snapshotAll(srcs []Source) map[string][]Item {
	out := make(map[string][]Item, len(srcs))
	for _, s := range srcs {
		out[s.ID()] = s.Snapshot()
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
// emptyQuery: hide create; cap zoxide to zoxCap. query: full pools.
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
		// Inside tmux, idle list: demote the session you are already on so
		// "just left" / other MRU actives surface first (smart-pane style).
		if ctxSession != "" && id == SrcTmux {
			for i := range items {
				if items[i].Name == ctxSession && items[i].Kind == KindActive {
					// keep kind Active but push below other actives with real recency
					if items[i].Recency > 0 {
						items[i].Recency = items[i].Recency / 4
					}
				}
			}
		}
		bySrc[id] = items
	}
}

// countSources total raw items (pre-dedupe, pre-cap).
func countSources(bySrc map[string][]Item) int {
	n := 0
	for _, items := range bySrc {
		n += len(items)
	}
	return n
}
