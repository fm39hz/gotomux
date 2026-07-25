package picker

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var gitBranchCache sync.Map // path → string ("" = not a git repo, "master | worktree" for linked worktree)

// detectLabel returns the branch display label for a path, or "" if not a git
// repo. Appends " | worktree" when the repo is a git linked worktree.
func detectLabel(path string) string {
	head, worktree := readHEAD(filepath.Clean(path))
	if head == "" {
		return ""
	}
	label := parseBranch(string(head))
	if label != "" && worktree {
		label += " | worktree"
	}
	return label
}

// readHEAD reads .git/HEAD. Works for both regular repos (.git dir)
// and linked worktrees (.git file pointing to the actual git dir via "gitdir: <path>").
// Returns (HEAD content, isWorktree).
func readHEAD(path string) (string, bool) {
	// Regular repo: .git/HEAD
	data, err := os.ReadFile(filepath.Join(path, ".git", "HEAD"))
	if err == nil {
		return strings.TrimSpace(string(data)), false
	}
	// Linked worktree: .git is a file, content "gitdir: <path-to-gitdir>\n"
	fi, err := os.Stat(filepath.Join(path, ".git"))
	if err != nil || fi.IsDir() {
		return "", false
	}
	data, err = os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		return "", false
	}
	raw := strings.TrimSpace(string(data))
	const gitdirPrefix = "gitdir: "
	if !strings.HasPrefix(raw, gitdirPrefix) {
		return "", false
	}
	// gitdir: path may be relative to the .git file's parent dir.
	gitDir := raw[len(gitdirPrefix):]
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(filepath.Dir(filepath.Join(path, ".git")), gitDir)
	}
	data, err = os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}

// parseBranch extracts the branch name from the HEAD ref line.
// Returns "" for detached HEAD.
func parseBranch(head string) string {
	if !strings.HasPrefix(head, "ref: refs/heads/") {
		return ""
	}
	return strings.TrimPrefix(head, "ref: refs/heads/")
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

// PreloadCache bulk-populates gitBranchCache from a pre-computed map.
// Used by daemon IPC to avoid filesystem I/O in the picker.
func PreloadCache(m map[string]string) {
	for path, branch := range m {
		if branch != "" {
			gitBranchCache.Store(path, branch)
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

// enrichPaths reads git branch for all given paths with limited concurrency.
func enrichPaths(paths []string, concurrency int) {
	if len(paths) == 0 {
		return
	}
	var wg sync.WaitGroup
	if concurrency < 1 {
		concurrency = 4
	}
	sem := make(chan struct{}, concurrency)
	for _, p := range paths {
		wg.Add(1)
		sem <- struct{}{}
		go func(path string) {
			defer wg.Done()
			defer func() { <-sem }()
			readGitBranch(path)
		}(p)
	}
	wg.Wait()
}

// enrichAllSync fills the git branch cache for all unique paths in bySrc.
func enrichAllSync(bySrc map[Source][]Item) { enrichAllSyncWith(bySrc, 4) }

func enrichAllSyncWith(bySrc map[Source][]Item, concurrency int) {
	enrichPaths(collectPaths(bySrc), concurrency)
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
