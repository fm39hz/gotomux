package picker

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/go-git/go-git/v5"
)

var gitBranchCache sync.Map // path → string ("" = not a git repo, "master | worktree" for linked worktree)

// detectLabel returns the branch display label for a path, or "" if not a git
// repo. Appends " | worktree" when the repo is a git linked worktree.
func detectLabel(path string) string {
	r, err := git.PlainOpenWithOptions(filepath.Clean(path), &git.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err != nil {
		return ""
	}
	ref, err := r.Head()
	if err != nil {
		return ""
	}
	if !ref.Name().IsBranch() {
		return ""
	}
	label := ref.Name().Short()

	// Detect linked worktree: .git is a FILE (not directory), content "gitdir: <path>"
	if wt, err := r.Worktree(); err == nil {
		if fi, err := os.Stat(filepath.Join(wt.Filesystem.Root(), ".git")); err == nil && !fi.IsDir() {
			label += " | worktree"
		}
	}
	return label
}

// readGitBranch checks cache first, then opens the repo to detect branch.
func readGitBranch(path string) string {
	if path == "" {
		return ""
	}
	if v, ok := gitBranchCache.Load(path); ok {
		return v.(string)
	}
	label := detectLabel(path)
	gitBranchCache.Store(path, label)
	return label
}

// enrichAllSync fills the git branch cache for all unique paths in bySrc,
// running go-git opens in parallel goroutines for speed.
func enrichAllSync(bySrc map[string][]Item) {
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
	if len(paths) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, p := range paths {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			readGitBranch(path)
		}(p)
	}
	wg.Wait()
}

// setGitBranch looks up the cached branch for the item's Path and sets
// GitBranch. No-op if not cached or not a git repo.
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
