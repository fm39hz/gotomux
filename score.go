package main

import (
	"path/filepath"
	"strings"
	"unicode"
)

// Ranking: lexicographic rankKey — not a single ad-hoc sum.
//
//	tier   — match quality band only (lower = better). Kind never outranks a better tier.
//	kind   — domain preference within the same tier (higher = better).
//	detail — within-tier match quality (higher = better).
//	pathQ  — prefer shallower paths (higher = better): -depth.
//	idx    — stable input order.
//
// Idle (empty q): tier=0 for all; sort by kind, then pathQ, then idx.
//
// Typed tiers:
//
//	0 token   — full name/basename == q, OR a hyphen segment == q
//	1 prefix  — name/basename/segment HasPrefix(q)
//	2 substr  — contiguous mid-string on those labels
//	3 fuzzy   — rune subsequence on name/basename
//	4 path    — match only via path segments (weakest)
//
// Product: Active "kho-cong" (token via segment) ranks above deep Zoxide "kho"
// (token via full name) because same tier and kind(Active) > kind(Zoxide).

const (
	tierToken  int8 = 0
	tierPrefix int8 = 1
	tierSubstr int8 = 2
	tierFuzzy  int8 = 3
	tierPath   int8 = 4
	tierNone   int8 = 127
)

const (
	detailBase    int32 = 1_000_000
	detailDensity int32 = 10_000
	detailPosUnit int32 = 100
	detailLenUnit int32 = 1
	// Within token tier: full-label exact slightly above segment exact.
	detailFullExact int32 = 5_000
	detailSegExact  int32 = 2_000
	detailFuzzyRun  int32 = 50
	detailFuzzyHit  int32 = 10
	detailFuzzyBnd  int32 = 20
)

type rankKey struct {
	tier   int8
	kind   int8
	detail int32
	pathQ  int8
	idx    int
}

// less reports whether a should sort before b (best first).
func (a rankKey) less(b rankKey) bool {
	if a.tier != b.tier {
		return a.tier < b.tier
	}
	if a.kind != b.kind {
		return a.kind > b.kind
	}
	if a.detail != b.detail {
		return a.detail > b.detail
	}
	if a.pathQ != b.pathQ {
		return a.pathQ > b.pathQ
	}
	return a.idx < b.idx
}

func kindRank(k kind) int8 {
	switch k {
	case kindCreate:
		return 4
	case kindActive:
		return 3
	case kindPreset:
		return 2
	default:
		return 1
	}
}

func pathQuality(p string) int8 {
	if p == "" {
		return 0
	}
	p = filepath.Clean(p)
	d := 0
	for _, r := range p {
		if r == filepath.Separator {
			d++
		}
	}
	if d > 127 {
		d = 127
	}
	return -int8(d)
}

type fieldHit struct {
	tier   int8
	detail int32
}

func betterHit(a, b fieldHit) fieldHit {
	if a.tier != b.tier {
		if a.tier < b.tier {
			return a
		}
		return b
	}
	if a.detail >= b.detail {
		return a
	}
	return b
}

func densityDetail(q, target string) int32 {
	if len(target) == 0 {
		return 0
	}
	return detailDensity * int32(len(q)) / int32(len(target))
}

func labelParts(label string) (whole string, segs []string) {
	whole = strings.ToLower(strings.TrimSpace(label))
	if whole == "" {
		return "", nil
	}
	for _, seg := range strings.FieldsFunc(whole, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == ' '
	}) {
		if seg != "" && seg != whole {
			segs = append(segs, seg)
		}
	}
	return whole, segs
}

// matchOnLabel: best hit on session name or folder basename (never path-only tier).
func matchOnLabel(q, label string) (fieldHit, bool) {
	whole, segs := labelParts(label)
	if whole == "" {
		return fieldHit{tierNone, 0}, false
	}
	best := fieldHit{tierNone, 0}
	ok := false

	hit := func(text string, segment bool) {
		if text == "" {
			return
		}
		if text == q {
			d := detailBase + densityDetail(q, text)
			if segment {
				d += detailSegExact
				// slight preference for longer structured parent labels
				d += int32(len(whole))
			} else {
				d += detailFullExact
			}
			best = betterHit(best, fieldHit{tierToken, d})
			ok = true
			return
		}
		if strings.HasPrefix(text, q) {
			rest := len(text) - len(q)
			d := detailBase + densityDetail(q, text) - int32(rest)*detailLenUnit
			best = betterHit(best, fieldHit{tierPrefix, d})
			ok = true
			return
		}
		if i := strings.Index(text, q); i >= 0 {
			d := detailBase + densityDetail(q, text) - int32(i)*detailPosUnit - int32(len(text))*detailLenUnit
			best = betterHit(best, fieldHit{tierSubstr, d})
			ok = true
			return
		}
		if d, yes := fuzzyDetail(q, text); yes {
			best = betterHit(best, fieldHit{tierFuzzy, d})
			ok = true
		}
	}

	hit(whole, false)
	for _, seg := range segs {
		hit(seg, true)
	}
	return best, ok
}

// matchOnPath: segments of path only → tierPath.
func matchOnPath(q, path string) (fieldHit, bool) {
	if path == "" {
		return fieldHit{tierNone, 0}, false
	}
	best := fieldHit{tierNone, 0}
	ok := false
	for _, seg := range strings.Split(filepath.ToSlash(filepath.Clean(path)), "/") {
		if seg == "" {
			continue
		}
		h, yes := matchOnLabel(q, seg)
		if !yes {
			continue
		}
		// collapse whatever label tier into path-only band; keep detail
		best = betterHit(best, fieldHit{tierPath, h.detail})
		ok = true
	}
	return best, ok
}

func fuzzyDetail(query, text string) (int32, bool) {
	qr, tr := []rune(query), []rune(text)
	if len(qr) == 0 {
		return detailBase, true
	}
	if len(qr) > len(tr) {
		return 0, false
	}
	ti := 0
	var score int32
	prev := -2
	first := -1
	for _, ch := range qr {
		found := false
		for ; ti < len(tr); ti++ {
			if tr[ti] != ch {
				continue
			}
			if first < 0 {
				first = ti
			}
			if ti == prev+1 {
				score += detailFuzzyRun
			} else {
				score += detailFuzzyHit
				if prev >= 0 {
					score -= int32(ti - prev - 1)
				}
			}
			if ti == 0 || isBoundary(tr[ti-1]) {
				score += detailFuzzyBnd
			}
			prev = ti
			ti++
			found = true
			break
		}
		if !found {
			return 0, false
		}
	}
	score += detailBase/100 - int32(first)*2 - int32(len(tr))
	if score < 0 {
		score = 0
	}
	return score, true
}

func isBoundary(r rune) bool {
	return r == '/' || r == '-' || r == '_' || r == '.' || r == ' ' || unicode.IsSpace(r)
}

// rankOf builds the sort key. ok=false → drop from results.
func rankOf(q string, it item, idx int) (rankKey, bool) {
	kr := kindRank(it.kind)
	pq := pathQuality(it.path)
	q = strings.ToLower(strings.TrimSpace(q))

	if q == "" {
		return rankKey{tier: 0, kind: kr, detail: 0, pathQ: pq, idx: idx}, true
	}

	best := fieldHit{tierNone, 0}
	any := false

	if h, ok := matchOnLabel(q, it.name); ok {
		best, any = h, true
	}
	base := filepath.Base(it.path)
	if base != "" && !strings.EqualFold(base, it.name) {
		if h, ok := matchOnLabel(q, base); ok {
			if !any {
				best, any = h, true
			} else {
				best = betterHit(best, h)
			}
		}
	}

	if h, ok := matchOnPath(q, it.path); ok {
		if !any {
			// pure path hit
			best, any = h, true
		}
		// if name already matched, path only affects pathQ — do not worsen/improve tier
		_ = h
	}

	if !any || best.tier == tierNone {
		return rankKey{}, false
	}
	return rankKey{
		tier:   best.tier,
		kind:   kr,
		detail: best.detail,
		pathQ:  pq,
		idx:    idx,
	}, true
}

// scoreItem: debug/total-order int (higher = better). Sorting uses rankKey.less.
func scoreItem(q string, it item) int {
	k, ok := rankOf(q, it, 0)
	if !ok {
		return -1
	}
	return int(127-k.tier)*100_000_000 +
		int(k.kind)*1_000_000 +
		int(k.detail) +
		int(k.pathQ+127)
}

// fuzzyMatch: any match (pick.go).
func fuzzyMatch(query, text string) bool {
	if query == "" {
		return true
	}
	query = strings.ToLower(query)
	text = strings.ToLower(text)
	if _, ok := matchOnLabel(query, text); ok {
		return true
	}
	if _, ok := matchOnPath(query, text); ok {
		return true
	}
	return false
}

// scoreMatch: legacy helper for tests — higher = better match on a single label.
func scoreMatch(query, text string) int {
	if query == "" {
		return 0
	}
	h, ok := matchOnLabel(query, text)
	if !ok {
		return -1
	}
	return int(127-h.tier)*100_000 + int(h.detail)
}
