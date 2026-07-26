package picker

import (
	"sync"

	"github.com/fm39hz/gotomux/internal/gitinfo"
)

// gitBranchCache is per-process: the picker is short-lived, so entries never need
// invalidating within a run. It caches misses as well as hits so a directory that
// is not a repository is not re-stat'ed on every refilter.
var gitBranchCache sync.Map // path -> string ("" = not a repo)

// readGitBranch returns the cached label for a path, reading it if absent.
func readGitBranch(path string) string {
	if path == "" {
		return ""
	}
	if v, ok := gitBranchCache.Load(path); ok {
		return v.(string)
	}
	label := gitinfo.Label(path)
	gitBranchCache.Store(path, label)
	return label
}

// PreloadCache bulk-populates the cache from a pre-computed map, so the picker
// performs no filesystem walk at all on the daemon path.
//
// Absent paths are cached as misses. The daemon reports only real branches, so
// without this every non-repository row would be re-read locally — reintroducing
// exactly the I/O the payload exists to avoid.
func PreloadCache(branches map[string]string) {
	for path, branch := range branches {
		gitBranchCache.Store(path, branch)
	}
}

// PreloadMisses marks paths with no branch, given the set that does have one.
func PreloadMisses(paths []string, branches map[string]string) {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, ok := branches[p]; !ok {
			gitBranchCache.Store(p, "")
		}
	}
}

// collectPaths extracts unique non-empty paths from bySrc.
func collectPaths(bySrc map[Source][]Item) []string {
	seen := map[string]bool{}
	var paths []string
	for _, items := range bySrc {
		for _, it := range items {
			p := it.Path
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			paths = append(paths, p)
		}
	}
	return paths
}

// enrichPaths fills the cache for the given paths with bounded concurrency.
func enrichPaths(paths []string, concurrency int) {
	pending := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, ok := gitBranchCache.Load(p); !ok {
			pending = append(pending, p)
		}
	}
	if len(pending) == 0 {
		return
	}
	found := gitinfo.Labels(pending, concurrency)
	for _, p := range pending {
		gitBranchCache.Store(p, found[p]) // "" for misses
	}
}

// enrichAllSync fills the git branch cache for all unique paths in bySrc.
func enrichAllSync(bySrc map[Source][]Item) { enrichAllSyncWith(bySrc, 4) }

func enrichAllSyncWith(bySrc map[Source][]Item, concurrency int) {
	enrichPaths(collectPaths(bySrc), concurrency)
}

// setGitBranch copies the cached label onto the item. No-op on a miss.
func setGitBranch(it *Item) {
	if it.Path == "" {
		return
	}
	v, ok := gitBranchCache.Load(it.Path)
	if !ok {
		return
	}
	if b := v.(string); b != "" {
		it.GitBranch = b
	}
}
