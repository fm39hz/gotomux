package picker

import (
	"os/exec"
	"time"

	"github.com/fm39hz/gotomux/internal/store"
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
	items := zoxRowsToItems(rows)
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

func zoxideQueryFresh() []string {
	out, err := exec.Command("zoxide", "query", "-l").Output()
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range splitLines(string(out)) {
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := trimSpace(s[start:i])
			if line != "" {
				out = append(out, line)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		line := trimSpace(s[start:])
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		c := s[len(s)-1]
		if c != ' ' && c != '\t' && c != '\r' {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

func rebuildZoxItems(cache *sourceCache) []Item {
	paths := zoxideQueryFresh()
	if len(paths) == 0 {
		return nil
	}
	items := zoxideItems(paths, nil, nil)
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
	return zoxideQueryFresh()
}

func zoxRowsToItems(rows []store.ZoxRow) []Item {
	out := make([]Item, 0, len(rows))
	for _, r := range rows {
		title := r.Title
		if title == "" {
			title = "[Zoxide] " + r.Name
		}
		out = append(out, Item{
			Kind: KindZoxide,
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
