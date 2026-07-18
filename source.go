package main

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Source IDs — stable keys for bySrc slots and future remote hosts.
const (
	srcCreate = "create"
	srcTmux   = "tmux"
	srcPreset = "preset"
	srcZoxide = "zoxide"
)

// Source feeds the picker. Snapshot seeds paint; Refresh is optional truth update.
// Add a source: implement this + register in defaultSources; connect via kind/src.
type Source interface {
	ID() string
	Snapshot() []item
	// Refresh: Cmd → sourceMsg; nil means Snapshot is already complete.
	Refresh() tea.Cmd
}

// sourceMsg replaces one source slot after background truth fetch.
type sourceMsg struct {
	id    string
	items []item
}

// defaultSources order = dedup priority (first wins name/path).
func defaultSources(ctl *TmuxCtl, store *Store, createName, createCwd string) []Source {
	return []Source{
		&createSource{ctl: ctl, name: createName, cwd: createCwd},
		&tmuxSource{ctl: ctl},
		&presetSource{store: store},
		&zoxideSource{},
	}
}

// --- create ---

type createSource struct {
	ctl  *TmuxCtl
	name string
	cwd  string
}

func (s *createSource) ID() string { return srcCreate }

func (s *createSource) Snapshot() []item {
	if s.name == "" {
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
	return []item{{
		src:   srcCreate,
		kind:  kindCreate,
		title: fmt.Sprintf("[Create] %s", s.name),
		desc:  s.cwd,
		name:  s.name,
		path:  s.cwd,
	}}
}

func (s *createSource) Refresh() tea.Cmd { return nil }

// --- local tmux ---

type tmuxSource struct{ ctl *TmuxCtl }

func (s *tmuxSource) ID() string { return srcTmux }

func (s *tmuxSource) Snapshot() []item {
	if s.ctl == nil {
		return nil
	}
	live, err := s.ctl.ListLive()
	if err != nil {
		return nil
	}
	out := make([]item, 0, len(live))
	for _, ls := range live {
		out = append(out, item{
			src:     srcTmux,
			kind:    kindActive,
			title:   fmt.Sprintf("[Active] %s", ls.Name),
			desc:    fmt.Sprintf("%d windows", ls.Windows),
			name:    ls.Name,
			path:    ls.Path,
			windows: ls.Windows,
		})
	}
	return out
}

func (s *tmuxSource) Refresh() tea.Cmd { return nil }

// --- presets ---

type presetSource struct{ store *Store }

func (s *presetSource) ID() string { return srcPreset }

func (s *presetSource) Snapshot() []item {
	if s.store == nil {
		return nil
	}
	meta, err := s.store.ListMeta()
	if err != nil {
		return nil
	}
	out := make([]item, 0, len(meta))
	for _, m := range meta {
		out = append(out, item{
			src:     srcPreset,
			kind:    kindPreset,
			title:   fmt.Sprintf("[Preset] %s", m.Name),
			desc:    "saved layout",
			name:    m.Name,
			path:    m.Cwd,
			recency: m.LastUsed,
		})
	}
	return out
}

func (s *presetSource) Refresh() tea.Cmd { return nil }

// --- zoxide (cache paint + full truth refresh) ---

type zoxideSource struct{}

func (s *zoxideSource) ID() string { return srcZoxide }

func (s *zoxideSource) Snapshot() []item {
	items, _, ok := loadZoxItemsSync()
	if !ok {
		return nil
	}
	// ensure src tag (older cache rows may lack it)
	for i := range items {
		items[i].src = srcZoxide
		items[i].kind = kindZoxide
	}
	return items
}

func (s *zoxideSource) Refresh() tea.Cmd {
	return func() tea.Msg {
		return sourceMsg{id: srcZoxide, items: rebuildZoxItems()}
	}
}

// snapshotAll: ordered Snapshot from every source (raw, no cross-dedupe).
func snapshotAll(srcs []Source) map[string][]item {
	out := make(map[string][]item, len(srcs))
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
func flattenSources(order []Source, bySrc map[string][]item, query string) []item {
	q := query != ""
	names := map[string]bool{}
	paths := map[string]bool{}
	var out []item
	for _, s := range order {
		id := s.ID()
		items := bySrc[id]
		if id == srcZoxide && !q {
			n := zoxCap
			if n > len(items) {
				n = len(items)
			}
			items = items[:n]
		}
		for _, it := range items {
			if id == srcCreate && q {
				continue
			}
			nr := normPath(it.path)
			if names[it.name] || (nr != "" && paths[nr]) {
				continue
			}
			names[it.name] = true
			if nr != "" {
				paths[nr] = true
			}
			out = append(out, it)
		}
	}
	return out
}

// applyRankMeta overlays usage + cooccur on all slots.
func applyRankMeta(bySrc map[string][]item, store *Store, pairs map[string]int64) {
	if store == nil {
		return
	}
	now := time.Now().Unix()
	us, _ := store.AllUsage()
	for id, items := range bySrc {
		if len(us) > 0 {
			applyUsage(items, us, now)
		}
		applyCooccur(items, pairs)
		bySrc[id] = items
	}
}

// countSources total raw items (pre-dedupe, pre-cap).
func countSources(bySrc map[string][]item) int {
	n := 0
	for _, items := range bySrc {
		n += len(items)
	}
	return n
}
