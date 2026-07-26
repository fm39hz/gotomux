// Package zoxide turns `zoxide query -l` output into the rows that both the
// picker and the daemon serve.
//
// It exists because those two derived rows independently and disagreed on all
// three things that matter:
//
//   - Recency. The picker used list order (n-i); the daemon stamped
//     time.Now().Unix() on every row. Ranking compares recency before kind, so
//     daemon-fed zoxide rows outranked every live session and every preset.
//   - Identity. The picker collapsed each path to its project root via
//     project.Session and sanitized the name; the daemon used filepath.Base and
//     the raw path. Different name and path means different dedup outcomes, and
//     connecting would create a session in the wrong directory.
//   - Dedup. The picker dropped intra-list duplicates by name and cleaned path;
//     the daemon did not.
//
// One derivation, one set of rules, both callers.
package zoxide

import (
	"fmt"
	"hash/fnv"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fm39hz/gotomux/internal/project"
	"github.com/fm39hz/gotomux/internal/store"
)

// Query runs `zoxide query -l`. A missing or failing zoxide yields nil: it is an
// optional dependency, not an error.
func Query() []string {
	out, err := exec.Command("zoxide", "query", "-l").Output()
	if err != nil {
		return nil
	}
	return SplitLines(string(out))
}

// SplitLines trims and drops empties. Exported so callers can feed Rows from a
// source other than the zoxide binary (tests, fixtures).
func SplitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// Rows converts zoxide paths, most-frecent first, into rows.
//
// Recency is the reverse list index, not a timestamp: zoxide exposes ordering,
// not scores, and a timestamp here would place every directory ahead of every
// live tmux session in the ranking tuple.
func Rows(paths []string) []store.ZoxRow {
	rows := make([]store.ZoxRow, 0, len(paths))
	seenName := make(map[string]bool, len(paths))
	seenPath := make(map[string]bool, len(paths))
	n := len(paths)
	for i, p := range paths {
		name, root := project.Session(p)
		if name == "" {
			continue
		}
		nr := clean(root)
		if seenName[name] || (nr != "" && seenPath[nr]) {
			continue
		}
		seenName[name] = true
		if nr != "" {
			seenPath[nr] = true
		}
		desc := p
		if nr != "" && clean(p) != nr {
			desc = root
		}
		rows = append(rows, store.ZoxRow{
			Name:    name,
			Path:    root,
			Title:   fmt.Sprintf("[Zoxide] %s", name),
			Desc:    desc,
			Recency: int64(n - i),
		})
	}
	return rows
}

func clean(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

// Signature fingerprints a raw zoxide path list.
//
// Deriving rows from paths costs ~0.9ms per path — FindProjectRoot walks up
// stat-ing for project markers, roughly 10k stat() calls for a 300-entry list, or
// ~270ms cold. The derived rows are already persisted, so that work only needs to
// happen when the *list itself* changes. Comparing signatures turns "has anything
// changed?" into a single string compare.
//
// Order matters: zoxide returns paths most-frecent first, and Rows derives Recency
// from position, so a reordering is a real change.
func Signature(paths []string) string {
	h := fnv.New64a()
	for _, p := range paths {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{'\n'})
	}
	return strconv.FormatUint(h.Sum64(), 16)
}
