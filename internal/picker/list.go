package picker

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/fm39hz/gotomux/internal/project"
	"github.com/fm39hz/gotomux/internal/store"
)

type Kind int

const (
	KindCreate Kind = iota
	KindActive
	KindPreset
	KindZoxide
)

// Item is one picker row from any Source.
type Item struct {
	// Busy is a session-level busy indicator (tool token from ActiveCmd).
	// Set for tmux source; empty for preset/create/zoxide.
	// Used for glance badge and weak rank boost.
	Busy    string

	Src     string // Source.ID(); remote later: "tmux@host"
	Host    string // "" = local
	Kind    Kind
	Title   string
	Desc    string
	Name    string
	Path    string
	Windows int
	// Recency: higher = better. Preset last_used / zoxide order / usage overlay.
	Recency int64
	// Cooccur: decayed pair score with current session.
	Cooccur int64
	// GitBranch: current branch if Path is a git repo; "" otherwise.
	GitBranch string
}

const zoxCap = 40 // empty query: show top-N zoxide only; query uses full pool

func normPath(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

func occupancy(items []Item) (names, paths map[string]bool) {
	names = map[string]bool{}
	paths = map[string]bool{}
	for _, it := range items {
		names[it.Name] = true
		if p := normPath(it.Path); p != "" {
			paths[p] = true
		}
	}
	return names, paths
}

// zoxideItems: collapse path -> project root; recency from zoxide order.
func zoxideItems(zpaths []string, names, paths map[string]bool) []Item {
	if names == nil {
		names = map[string]bool{}
	}
	if paths == nil {
		paths = map[string]bool{}
	}
	var out []Item
	n := len(zpaths)
	for i, p := range zpaths {
		name, root := project.Session(p)
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
			desc = root
		}
		out = append(out, Item{
			Src:     SrcZoxide,
			Kind:    KindZoxide,
			Title:   fmt.Sprintf("[Zoxide] %s", name),
			Desc:    desc,
			Name:    name,
			Path:    root,
			Recency: int64(n - i),
		})
	}
	return out
}

func rankItems(q string, pool []Item) []Item {
	type scored struct {
		it Item
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
	out := make([]Item, len(hits))
	for i, h := range hits {
		out[i] = h.it
	}
	return out
}

func applyUsage(items []Item, usages map[string]store.Usage, now int64) {
	if len(usages) == 0 {
		return
	}
	if now <= 0 {
		now = 0
	}
	for i := range items {
		u, ok := usages[items[i].Name]
		if !ok {
			continue
		}
		if s := usageRecency(u, now); s > 0 {
			// keep stronger of app frecency vs source stamp (tmux last_attached)
			if s > items[i].Recency {
				items[i].Recency = s
			}
		}
	}
}

func applyCooccur(items []Item, scores map[string]int64) {
	if len(scores) == 0 {
		return
	}
	for i := range items {
		if s, ok := scores[items[i].Name]; ok {
			items[i].Cooccur = s
		}
	}
}
