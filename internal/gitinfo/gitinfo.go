// Package gitinfo reads a repository's current branch label from disk.
//
// It exists because the picker and the daemon each had their own reader with the
// same name and different capabilities: the picker's understood linked worktrees
// (`.git` as a file with a `gitdir:` pointer) and appended " | worktree", the
// daemon's read only `.git/HEAD`. Which one you got depended on whether a daemon
// happened to be running, so the same session showed a different label — or none
// — across run modes.
//
// Everything here is plain file reads: no git subprocess, no repository parsing.
package gitinfo

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Label returns the branch display label for a path, or "" when the path is not a
// git repository or HEAD is detached. Linked worktrees get a " | worktree" suffix.
func Label(path string) string {
	if path == "" {
		return ""
	}
	head, worktree := readHEAD(filepath.Clean(path))
	if head == "" {
		return ""
	}
	label := parseBranch(head)
	if label != "" && worktree {
		label += " | worktree"
	}
	return label
}

// readHEAD returns HEAD's contents and whether the path is a linked worktree.
//
// A regular repository has .git/HEAD. A linked worktree has .git as a *file*
// holding "gitdir: <path>", where the path may be relative to the .git file's
// parent.
func readHEAD(path string) (head string, worktree bool) {
	if data, err := os.ReadFile(filepath.Join(path, ".git", "HEAD")); err == nil {
		return strings.TrimSpace(string(data)), false
	}

	dotGit := filepath.Join(path, ".git")
	fi, err := os.Stat(dotGit)
	if err != nil || fi.IsDir() {
		return "", false
	}
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return "", false
	}
	raw := strings.TrimSpace(string(data))
	const prefix = "gitdir: "
	if !strings.HasPrefix(raw, prefix) {
		return "", false
	}
	gitDir := raw[len(prefix):]
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(filepath.Dir(dotGit), gitDir)
	}
	data, err = os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}

// parseBranch extracts the branch from a HEAD ref line. Detached HEAD yields "".
func parseBranch(head string) string {
	const ref = "ref: refs/heads/"
	if !strings.HasPrefix(head, ref) {
		return ""
	}
	return strings.TrimPrefix(head, ref)
}

// Labels reads labels for a set of paths concurrently, skipping duplicates and
// empties. Absent entries mean "not a repo"; the map only holds real branches.
func Labels(paths []string, concurrency int) map[string]string {
	uniq := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		uniq = append(uniq, p)
	}
	if len(uniq) == 0 {
		return map[string]string{}
	}
	if concurrency < 1 {
		concurrency = 4
	}

	out := make(map[string]string, len(uniq))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)
	for _, p := range uniq {
		wg.Add(1)
		sem <- struct{}{}
		go func(path string) {
			defer wg.Done()
			defer func() { <-sem }()
			if b := Label(path); b != "" {
				mu.Lock()
				out[path] = b
				mu.Unlock()
			}
		}(p)
	}
	wg.Wait()
	return out
}
