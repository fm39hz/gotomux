package picker

import (
	"time"

	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/zoxide"
)

func loadZoxItemsSync(cache *sourceCache) ([]Item, time.Duration, bool) {
	cache.zoxMu.Lock()
	if len(cache.zoxMem) > 0 {
		age := time.Since(cache.zoxAt)
		items := cache.zoxMem
		cache.zoxMu.Unlock()
		return items, age, true
	}
	cache.zoxMu.Unlock()

	if cache.zoxSt == nil {
		return nil, 0, false
	}
	rows, updated, ok := cache.zoxSt.LoadZox()
	if !ok {
		return nil, 0, false
	}
	items := ZoxRowsToItems(rows)
	age := time.Duration(0)
	if updated > 0 {
		age = time.Since(time.Unix(updated, 0))
		if age < 0 {
			age = 0
		}
	}
	cache.zoxMu.Lock()
	cache.zoxMem = items
	cache.zoxAt = time.Now().Add(-age)
	cache.zoxMu.Unlock()
	return items, age, true
}

func saveZoxItems(items []Item, cache *sourceCache) {
	if len(items) == 0 {
		return
	}
	if cache.zoxSt != nil {
		_ = cache.zoxSt.SaveZox(itemsToZoxRows(items))
	}
	cache.zoxMu.Lock()
	cache.zoxMem = items
	cache.zoxAt = time.Now()
	cache.zoxMu.Unlock()
}

// rebuildZoxItems re-queries zoxide and refreshes both caches.
//
// Row derivation lives in internal/zoxide so the daemon produces byte-identical
// rows; deriving them here independently is what made daemon-fed and
// standalone-fed zoxide entries rank differently.
func rebuildZoxItems(cache *sourceCache) []Item {
	items := ZoxRowsToItems(zoxide.Rows(zoxide.Query()))
	if len(items) > 0 {
		saveZoxItems(items, cache)
	}
	return items
}

func zoxideList(cache *sourceCache) []string {
	if items, _, ok := loadZoxItemsSync(cache); ok {
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
