package picker

import (
	"path/filepath"
	"slices"

	"github.com/fm39hz/gotomux/internal/store"
)

type Kind int

const (
	KindCreate Kind = iota
	KindActive
	KindPreset
	KindZoxide
)

// ID uniquely identifies a session/project. Session names are unique
// within tmux; sources deduplicate by Name. This is the stable identity
// for cursor tracking, animation, and dedup — not a convention string.
type ID string

func (it Item) ID() ID { return ID(it.Name) }

// Item is one picker row from any Source.
type Item struct {
	// Host is reserved for the remote-tmux source in docs/todo.md; nothing sets
	// it yet. Busy used to live here too but was never read — the non-shell tool
	// badge is rendered through Desc (see mkBusy/badgeFromBusy).
	Host    string // "" = local; remote: "hostname"
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

// defaultZoxCap is the fallback when Config.ZoxideCap is unset. The effective
// cap lives on sourceCache, not in a package-level var mutated during model
// construction.
const defaultZoxCap = 40

func normPath(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
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
	slices.SortStableFunc(hits, func(a, b scored) int {
		switch {
		case a.k.less(b.k):
			return -1
		case b.k.less(a.k):
			return 1
		default:
			return 0
		}
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
