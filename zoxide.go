package main

import (
	"os/exec"
	"sync"
	"time"
)

// Zoxide list is slow (~50–150ms + project walk). Cache pre-built items in
// SQLite so open paints Create/Active/Zoxide from last run; full list rebuilds
// in background and merges into the search pool (empty query still UI-caps).
//
//  1. loadZoxItemsSync reads DB (or memory) — first paint.
//  2. Init always runs rebuildZoxItems (zoxide query -l + all paths).
//  3. mergeZoxide swaps pool; rank on query sees low-frecency hits.

const zoxCacheTTL = 60 * time.Second // kept for reload() age checks only

var (
	zoxItemMem   []item
	zoxItemMemAt time.Time
	zoxItemMu    sync.Mutex
	// set by openStore path so cache uses same DB as presets
	zoxStore *Store
)

func setZoxStore(s *Store) { zoxStore = s }

func loadZoxItemsSync() ([]item, time.Duration, bool) {
	zoxItemMu.Lock()
	if len(zoxItemMem) > 0 {
		age := time.Since(zoxItemMemAt)
		items := zoxItemMem
		zoxItemMu.Unlock()
		return items, age, true
	}
	zoxItemMu.Unlock()

	if zoxStore == nil {
		return nil, 0, false
	}
	items, updated, ok := zoxStore.LoadZoxItems()
	if !ok {
		return nil, 0, false
	}
	age := time.Duration(0)
	if updated > 0 {
		age = time.Since(time.Unix(updated, 0))
		if age < 0 {
			age = 0
		}
	}
	zoxItemMu.Lock()
	zoxItemMem = items
	zoxItemMemAt = time.Now().Add(-age)
	zoxItemMu.Unlock()
	return items, age, true
}

func saveZoxItems(items []item) {
	if len(items) == 0 {
		return
	}
	if zoxStore != nil {
		_ = zoxStore.SaveZoxItems(items)
	}
	zoxItemMu.Lock()
	zoxItemMem = items
	zoxItemMemAt = time.Now()
	zoxItemMu.Unlock()
}

func zoxItemsStale(age time.Duration, ok bool) bool {
	return !ok || age >= zoxCacheTTL
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

func rebuildZoxItems() []item {
	paths := zoxideQueryFresh()
	if len(paths) == 0 {
		return nil
	}
	// full list — low-rank paths stay searchable after merge; paint already done
	items := zoxideItems(paths, map[string]bool{}, map[string]bool{})
	if len(items) > 0 {
		saveZoxItems(items)
	}
	return items
}

func zoxideList() []string {
	if items, _, ok := loadZoxItemsSync(); ok {
		out := make([]string, 0, len(items))
		for _, it := range items {
			if it.path != "" {
				out = append(out, it.path)
			}
		}
		return out
	}
	return zoxideQueryFresh()
}

func zoxideListCachedOnly() []string {
	if items, _, ok := loadZoxItemsSync(); ok {
		out := make([]string, 0, len(items))
		for _, it := range items {
			if it.path != "" {
				out = append(out, it.path)
			}
		}
		return out
	}
	return nil
}
