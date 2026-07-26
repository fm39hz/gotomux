package picker

import (
	"time"

	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/zoxide"
)

// loadZoxItemsSync returns cached rows from memory, else from the store.
//
// It reports the cache's signature so the background validator can tell whether
// re-deriving is needed. Age is returned for diagnostics only: rows are no longer
// discarded for being old. A time-based expiry (30s) meant a stale cache forced a
// full `zoxide query -l` plus a re-derivation of every path — ~340ms — on
// essentially every real invocation, because 30s is shorter than the gap between
// two picker opens.
func loadZoxItemsSync(cache *sourceCache) (items []Item, age time.Duration, sig string, ok bool) {
	cache.zoxMu.Lock()
	if len(cache.zoxMem) > 0 {
		items, sig = cache.zoxMem, cache.zoxSig
		age = time.Since(cache.zoxAt)
		cache.zoxMu.Unlock()
		return items, age, sig, true
	}
	cache.zoxMu.Unlock()

	if cache.zoxSt == nil {
		return nil, 0, "", false
	}
	rows, updated, storedSig, found := cache.zoxSt.LoadZox()
	if !found {
		return nil, 0, "", false
	}
	items = ZoxRowsToItems(rows)
	if updated > 0 {
		if age = time.Since(time.Unix(updated, 0)); age < 0 {
			age = 0
		}
	}
	cache.zoxMu.Lock()
	cache.zoxMem, cache.zoxSig = items, storedSig
	cache.zoxAt = time.Now().Add(-age)
	cache.zoxMu.Unlock()
	return items, age, storedSig, true
}

func saveZoxItems(items []Item, sig string, cache *sourceCache) {
	if len(items) == 0 {
		return
	}
	if cache.zoxSt != nil {
		_ = cache.zoxSt.SaveZox(itemsToZoxRows(items), sig)
	}
	cache.zoxMu.Lock()
	cache.zoxMem, cache.zoxSig = items, sig
	cache.zoxAt = time.Now()
	cache.zoxMu.Unlock()
}

// rebuildZoxItems queries zoxide and derives rows. This is the expensive path:
// ~50ms for the query plus ~0.9ms per path to resolve project roots.
//
// Row derivation lives in internal/zoxide so the daemon produces byte-identical
// rows; deriving them here independently is what made daemon-fed and
// standalone-fed zoxide entries rank differently.
func rebuildZoxItems(cache *sourceCache) []Item {
	paths := zoxide.Query()
	if len(paths) == 0 {
		return nil
	}
	sig := zoxide.Signature(paths)
	items := ZoxRowsToItems(zoxide.Rows(paths))
	if len(items) > 0 {
		saveZoxItems(items, sig, cache)
	}
	return items
}

// validateZoxItems re-derives rows only when the path list actually changed.
// Returns nil when nothing changed, so the caller can skip the repaint entirely.
//
// paths is passed in rather than queried here so the decision is testable without
// executing zoxide.
func validateZoxItems(cache *sourceCache, knownSig string, paths []string) []Item {
	if len(paths) == 0 {
		return nil
	}
	sig := zoxide.Signature(paths)
	if sig == knownSig {
		// Same list: the persisted rows are already correct, and re-deriving them
		// would spend ~270ms to produce identical output. Just mark the cache as
		// freshly checked.
		cache.zoxMu.Lock()
		cache.zoxAt = time.Now()
		cache.zoxMu.Unlock()
		return nil
	}
	items := ZoxRowsToItems(zoxide.Rows(paths))
	if len(items) == 0 {
		return nil
	}
	saveZoxItems(items, sig, cache)
	return items
}

func zoxideList(cache *sourceCache) []string {
	if items, _, _, ok := loadZoxItemsSync(cache); ok {
		out := make([]string, 0, len(items))
		for _, it := range items {
			if it.Path != "" {
				out = append(out, it.Path)
			}
		}
		return out
	}
	return zoxide.Query()
}

func ZoxRowsToItems(rows []store.ZoxRow) []Item {
	out := make([]Item, 0, len(rows))
	for _, r := range rows {
		title := r.Title
		if title == "" {
			title = "[Zoxide] " + r.Name
		}
		out = append(out, Item{
			Kind:  KindZoxide,
			Title: title, Desc: r.Desc, Name: r.Name, Path: r.Path, Recency: r.Recency,
		})
	}
	return out
}

func itemsToZoxRows(items []Item) []store.ZoxRow {
	out := make([]store.ZoxRow, 0, len(items))
	for _, it := range items {
		if it.Name == "" {
			continue
		}
		out = append(out, store.ZoxRow{
			Name: it.Name, Path: it.Path, Title: it.Title,
			Desc: it.Desc, Recency: it.Recency,
		})
	}
	return out
}
