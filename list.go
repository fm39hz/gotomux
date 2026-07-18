package main

import (
	"fmt"
	"path/filepath"
	"sort"
)

type kind int

const (
	kindCreate kind = iota
	kindActive
	kindPreset
	kindZoxide
)

type item struct {
	kind    kind
	title   string
	desc    string
	name    string
	path    string
	windows int
	// recency: higher = more recent / more frequent.
	// Preset: last_used unix; Zoxide: inverted list index (zoxide score order);
	// Active/Create: 0 (kind already prefers them).
	recency int64
	// cooccur: decayed pair score with current tmux session (0 if none).
	cooccur int64
}

const zoxCap = 40 // empty query: show top-N only; query uses full m.zox pool

// collectBase: Create → Active → Presets(last_used). No zoxide.
func collectBase(ctl *TmuxCtl, store *Store, create item) []item {
	seenName := map[string]bool{}
	var items []item

	live, _ := ctl.ListLive()
	liveNames := map[string]bool{}
	for _, s := range live {
		liveNames[s.Name] = true
	}

	if create.name != "" && !liveNames[create.name] {
		seenName[create.name] = true
		items = append(items, create)
	}

	for _, s := range live {
		seenName[s.Name] = true
		items = append(items, item{
			kind:    kindActive,
			title:   fmt.Sprintf("[Active] %s", s.Name),
			desc:    fmt.Sprintf("%d windows", s.Windows),
			name:    s.Name,
			path:    s.Path,
			windows: s.Windows,
		})
	}

	if meta, err := store.ListMeta(); err == nil {
		for _, m := range meta {
			if seenName[m.Name] {
				continue
			}
			seenName[m.Name] = true
			items = append(items, item{
				kind:    kindPreset,
				title:   fmt.Sprintf("[Preset] %s", m.Name),
				desc:    "saved layout",
				name:    m.Name,
				path:    m.Cwd,
				recency: m.LastUsed,
			})
		}
	}
	return items
}

func normPath(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

func occupancy(items []item) (names, paths map[string]bool) {
	names = map[string]bool{}
	paths = map[string]bool{}
	for _, it := range items {
		names[it.name] = true
		if p := normPath(it.path); p != "" {
			paths[p] = true
		}
	}
	return names, paths
}

// zoxideItems: skip if session name OR project root already covered.
// Path is project root (not the raw zoxide hit) so connect never creates a "src" session.
// recency: earlier in zoxide list (higher frecency) → larger recency.
func zoxideItems(zpaths []string, names, paths map[string]bool) []item {
	var out []item
	n := len(zpaths)
	for i, p := range zpaths {
		name, root := projectSession(p)
		if name == "" {
			continue
		}
		nr := normPath(root)
		if names[name] || (nr != "" && paths[nr]) {
			continue
		}
		names[name] = true
		if nr != "" {
			paths[nr] = true
		}
		desc := p
		if nr != "" && normPath(p) != nr {
			desc = root // show root when zoxide pointed at a subdir
		}
		out = append(out, item{
			kind:    kindZoxide,
			title:   fmt.Sprintf("[Zoxide] %s", name),
			desc:    desc,
			name:    name,
			path:    root,
			recency: int64(n - i),
		})
	}
	return out
}

// rankItems sorts pool by rankKey.
func rankItems(q string, pool []item) []item {
	type scored struct {
		it item
		k  rankKey
	}
	hits := make([]scored, 0, len(pool))
	for i, it := range pool {
		k, ok := rankOf(q, it, i)
		if !ok {
			continue
		}
		hits = append(hits, scored{it, k})
	}
	sort.SliceStable(hits, func(a, b int) bool {
		return hits[a].k.less(hits[b].k)
	})
	out := make([]item, len(hits))
	for i, h := range hits {
		out[i] = h.it
	}
	return out
}

// applyUsage overlays app frecency onto items when usage rows exist.
// Fallback recency (preset last_used / zoxide order) kept if no usage yet.
func applyUsage(items []item, usages map[string]Usage, now int64) {
	if len(usages) == 0 {
		return
	}
	if now <= 0 {
		now = 0
	}
	for i := range items {
		u, ok := usages[items[i].name]
		if !ok {
			continue
		}
		if s := usageRecency(u, now); s > 0 {
			items[i].recency = s
		}
	}
}

// applyCooccur sets item.cooccur from pair scores for context session.
func applyCooccur(items []item, scores map[string]int64) {
	if len(scores) == 0 {
		return
	}
	for i := range items {
		if s, ok := scores[items[i].name]; ok {
			items[i].cooccur = s
		}
	}
}
