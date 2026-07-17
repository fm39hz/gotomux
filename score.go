package main

import (
	"os"
	"path/filepath"
	"strings"
)

// scoreItem ranks one list entry. -1 = no match (only when q non-empty).
//
//	empty  → kind weight only
//	typed  → name/basename quality + kind; full path only weak (avoids .config/* flood)
func scoreItem(q string, it item) int {
	if q == "" {
		return kindScore(it.kind)
	}
	best := -1
	// Primary: session name (hyphen segments count)
	if s := scoreName(q, strings.ToLower(it.name)); s > best {
		best = s
	}
	// Basename of path (zoxide folder name)
	base := strings.ToLower(filepath.Base(it.path))
	if base != "" && base != strings.ToLower(it.name) {
		if s := scoreName(q, base); s > best {
			best = s
		}
	}
	// Full path: weak only — enough to keep a hit, not to outrank names
	if p := strings.ToLower(it.path); p != "" {
		if s := scorePathWeak(q, p); s > best {
			best = s
		}
	}
	if best < 0 {
		return -1
	}
	return best + kindScore(it.kind)
}

// kindScore: idle ranking + typed tie-break. Below one match tier step (~10k).
func kindScore(k kind) int {
	switch k {
	case kindCreate:
		return 8_000
	case kindActive:
		return 6_000
	case kindPreset:
		return 4_000
	default:
		return 0
	}
}

// scoreName: match against a label (session name or folder basename).
// Takes max(whole label, hyphen/underscore segments) so "confi" scores the
// "config" segment inside "dotfiles-config", not a weak mid-string hit on the whole string.
func scoreName(q, name string) int {
	if name == "" {
		return -1
	}
	best := -1
	if s := scoreMatch(q, name); s >= 0 {
		best = s + densityBonus(q, name)
	}
	for _, seg := range strings.FieldsFunc(name, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == ' '
	}) {
		if seg == "" || seg == name {
			continue
		}
		s := scoreMatch(q, seg)
		if s < 0 {
			continue
		}
		// segment hit: full tier on the segment + density on segment length
		total := s + densityBonus(q, seg)
		if total > best {
			best = total
		}
	}
	return best
}

// scorePathWeak: path hit only if a path segment matches; capped low.
func scorePathWeak(q, path string) int {
	best := -1
	for _, seg := range strings.Split(path, string(os.PathSeparator)) {
		if seg == "" {
			continue
		}
		s := scoreMatch(q, strings.ToLower(seg))
		if s < 0 {
			continue
		}
		// cap so path never beats a real name prefix/exact
		if s > 25_000 {
			s = 25_000
		}
		s += densityBonus(q, seg) / 2
		if s > best {
			best = s
		}
	}
	if best < 0 {
		return -1
	}
	return best
}

func densityBonus(q, target string) int {
	if len(target) == 0 {
		return 0
	}
	// 0..5000 — "confi"/"config" ≈ 4166, "confi"/"dotfiles-config" whole is lower path
	return 5000 * len(q) / len(target)
}

// scoreMatch: higher = better. -1 = no match.
// exact > segment-boundary prefix > prefix > substring > fuzzy.
func scoreMatch(query, text string) int {
	if query == "" {
		return 0
	}
	if text == query {
		return 100_000
	}
	if strings.HasPrefix(text, query) {
		rest := text[len(query):]
		if rest == "" {
			return 100_000
		}
		// longer completion after a clean prefix is fine; prefer denser via densityBonus
		return 80_000 - (len(text) - len(query))
	}
	if i := strings.Index(text, query); i >= 0 {
		// mid-string substring (not a leading prefix) — weaker
		return 40_000 - i*20 - len(text)
	}
	return scoreFuzzy(query, text)
}

// scoreFuzzy: rune subsequence with consecutive-run bonus. -1 if no match.
func scoreFuzzy(query, text string) int {
	qr, tr := []rune(query), []rune(text)
	if len(qr) == 0 {
		return 0
	}
	if len(qr) > len(tr) {
		return -1
	}
	ti := 0
	score := 0
	prev := -2 // last match index
	first := -1
	for _, q := range qr {
		found := false
		for ; ti < len(tr); ti++ {
			if tr[ti] != q {
				continue
			}
			if first < 0 {
				first = ti
			}
			// consecutive run
			if ti == prev+1 {
				score += 50
			} else {
				score += 10
				// gap penalty
				if prev >= 0 {
					gap := ti - prev - 1
					if gap > 0 {
						score -= gap
					}
				}
			}
			// word-boundary / start bonus
			if ti == 0 || tr[ti-1] == '/' || tr[ti-1] == '-' || tr[ti-1] == '_' || tr[ti-1] == ' ' {
				score += 20
			}
			prev = ti
			ti++
			found = true
			break
		}
		if !found {
			return -1
		}
	}
	// earlier first match, shorter text → better
	score += 1000 - first*2
	score -= len(tr)
	if score < 0 {
		score = 0
	}
	return score
}

// fuzzyMatch kept for pick.go / tests — true if any match.
func fuzzyMatch(query, text string) bool {
	if query == "" {
		return true
	}
	return scoreMatch(query, text) >= 0
}
