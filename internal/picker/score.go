package picker

import (
	"path/filepath"
	"strings"
	"unicode"

	"github.com/fm39hz/gotomux/internal/store"
	"golang.org/x/text/unicode/norm"
)

// Ranking: lexicographic rankKey (not a single ad-hoc sum).
//
//	tier    — match quality band (lower = better). Kind never outranks a better tier.
//	kind    — domain preference within tier (higher = better).
//	detail  — within-tier match quality (higher = better).
//	recency — app frecency (opens/kills/time) or fallback preset/zoxide (higher = better).
//	cooccur — pair score with current session (higher = better); 0 if no context.
//	pathQ   — shallower path (higher = better): -depth.
//	idx     — stable input order.
//
// Idle (empty q): tier=0; sort kind → recency (tmux last_attached/activity, usage max) → cooccur → pathQ → idx.
// Inside tmux, current session recency is demoted so "just left" surfaces first.
//
// Frecency (usage table): opens with day-decay minus kill penalty — see frecencyScore.
//
// Typed tiers:
//
//	0 token  — full name/basename == q, OR a name segment == q
//	1 prefix — label/segment HasPrefix(q)
//	2 substr — mid-string contains q
//	3 fuzzy  — rune subsequence
//	4 path   — path segments only
//
// Multi-token query (whitespace): AND — every token must match; tier = worst token tier;
// detail = sum of per-token details.
//
// Segments: split on - _ . space, plus CamelCase / acronym boundaries.

const (
	tierToken  int8 = 0
	tierPrefix int8 = 1
	tierSubstr int8 = 2
	tierFuzzy  int8 = 3
	tierPath   int8 = 4
	tierNone   int8 = 127
)

const (
	detailBase      int32 = 1_000_000
	detailDensity   int32 = 10_000
	detailPosUnit   int32 = 100
	detailLenUnit   int32 = 1
	detailFullExact int32 = 5_000
	detailSegExact  int32 = 2_000
	detailFuzzyRun  int32 = 50
	detailFuzzyHit  int32 = 10
	detailFuzzyBnd  int32 = 20
)

type rankKey struct {
	tier    int8
	kind    int8
	detail  int32
	recency int64
	cooccur int64
	pathQ   int8
	idx     int
}

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
	if a.recency != b.recency {
		return a.recency > b.recency
	}
	if a.cooccur != b.cooccur {
		return a.cooccur > b.cooccur
	}
	if a.pathQ != b.pathQ {
		return a.pathQ > b.pathQ
	}
	return a.idx < b.idx
}

func kindRank(k Kind) int8 {
	switch k {
	case KindCreate:
		return 4
	case KindActive:
		return 3
	case KindPreset:
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

func worseTier(a, b int8) int8 {
	if a > b {
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

// camelSplit breaks CamelCase and acronyms: "APIConfiguration" → API, Configuration.
func camelSplit(s string) []string {
	runes := []rune(s)
	if len(runes) == 0 {
		return nil
	}
	var segs []string
	start := 0
	for i := 1; i < len(runes); i++ {
		r, prev := runes[i], runes[i-1]
		br := false
		if unicode.IsUpper(r) {
			if unicode.IsLower(prev) {
				br = true
			} else if i+1 < len(runes) && unicode.IsLower(runes[i+1]) && unicode.IsUpper(prev) {
				// XMLParser → break before P
				br = true
			}
		}
		if br {
			if start < i {
				segs = append(segs, string(runes[start:i]))
			}
			start = i
		}
	}
	if start < len(runes) {
		segs = append(segs, string(runes[start:]))
	}
	return segs
}

// foldDiacritic: NFD → strip marks → lower; Vietnamese đ/Đ → d.
// Match-time only — stored names stay as-is.
func foldDiacritic(s string) string {
	if s == "" {
		return ""
	}
	// Fast path: pure ASCII letters/digits/- already lower.
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return strings.ToLower(s)
	}
	de := norm.NFD.String(s)
	var b strings.Builder
	b.Grow(len(de))
	for _, r := range de {
		if unicode.Is(unicode.Mn, r) {
			continue // combining marks (acute, grave, horn, …)
		}
		switch r {
		case 'đ', 'Đ':
			b.WriteByte('d')
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

// labelParts: whole folded label + delimiter segments + camel segments (all folded).
func labelParts(label string) (whole string, segs []string) {
	raw := strings.TrimSpace(label)
	whole = foldDiacritic(raw)
	if whole == "" {
		return "", nil
	}
	seen := map[string]bool{whole: true}
	add := func(s string) {
		s = foldDiacritic(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		segs = append(segs, s)
	}
	// punctuation / path-ish split (on original for camel before fold)
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == ' ' || r == '/'
	}) {
		if part == "" {
			continue
		}
		add(part)
		for _, c := range camelSplit(part) {
			add(c)
		}
	}
	// also camel-split whole raw if no delimiters
	if !strings.ContainsAny(raw, "-_. /") {
		for _, c := range camelSplit(raw) {
			add(c)
		}
	}
	return whole, segs
}

func matchOnLabel(q, label string) (fieldHit, bool) {
	q = foldDiacritic(q)
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
				d += detailSegExact + int32(len(whole))
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

// bestHit for a single token against name / basename / path.
func bestHitToken(token string, it Item) (fieldHit, bool) {
	best := fieldHit{tierNone, 0}
	any := false
	if h, ok := matchOnLabel(token, it.Name); ok {
		best, any = h, true
	}
	base := filepath.Base(it.Path)
	if base != "" && !strings.EqualFold(base, it.Name) {
		if h, ok := matchOnLabel(token, base); ok {
			if !any {
				best, any = h, true
			} else {
				best = betterHit(best, h)
			}
		}
	}
	if h, ok := matchOnPath(token, it.Path); ok {
		if !any {
			best, any = h, true
		}
		// name already matched: path does not change tier
		_ = h
	}
	return best, any
}

// frecencyScore combines open frequency, recency decay, and kill penalty.
// Higher = used more / more recently / killed less. Pure integer (no float).
//
//	opens: day-decay half-life ~7d via opens*1000/(1+ageDays)
//	kills: soft penalty kills*200/(1+killAgeDays)
//	plus small recency bump so a brand-new open beats a stale high-open ghost
func frecencyScore(opens, kills, lastOpen, lastKill, now int64) int64 {
	if opens <= 0 && kills <= 0 && lastOpen <= 0 {
		return 0
	}
	if now <= 0 {
		now = 0
	}
	ageOpen := int64(0)
	if lastOpen > 0 && now >= lastOpen {
		ageOpen = (now - lastOpen) / 86400
	}
	ageKill := int64(0)
	if lastKill > 0 && now >= lastKill {
		ageKill = (now - lastKill) / 86400
	}
	o := opens
	if o > 10_000 {
		o = 10_000
	}
	k := kills
	if k > 10_000 {
		k = 10_000
	}
	// frequency with day decay
	freq := o * 1000 / (1 + ageOpen)
	// recent activity bump (0..100)
	bump := int64(100) - ageOpen
	if bump < 0 {
		bump = 0
	}
	// kill penalty (recent kills hurt more)
	pen := k * 200 / (1 + ageKill)
	score := freq + bump - pen
	if score < 0 {
		return 0
	}
	return score
}

// usageRecency maps stored Usage → rank recency key at "now".
func usageRecency(u store.Usage, now int64) int64 {
	return frecencyScore(u.Opens, u.Kills, u.LastOpen, u.LastKill, now)
}

// rankOf builds the sort key. ok=false → drop.
func rankOf(q string, it Item, idx int) (rankKey, bool) {
	kr := kindRank(it.Kind)
	pq := pathQuality(it.Path)
	q = strings.TrimSpace(q)

	if q == "" {
		return rankKey{tier: 0, kind: kr, detail: 0, recency: it.Recency, cooccur: it.Cooccur, pathQ: pq, idx: idx}, true
	}

	tokens := strings.Fields(foldDiacritic(q))
	if len(tokens) == 0 {
		return rankKey{tier: 0, kind: kr, detail: 0, recency: it.Recency, cooccur: it.Cooccur, pathQ: pq, idx: idx}, true
	}

	// Multi-token AND: every token must match; tier = worst; detail = sum.
	var (
		worst  int8 = tierToken
		detail int32
	)
	for i, tok := range tokens {
		h, ok := bestHitToken(tok, it)
		if !ok || h.tier == tierNone {
			return rankKey{}, false
		}
		if i == 0 {
			worst = h.tier
		} else {
			worst = worseTier(worst, h.tier)
		}
		detail += h.detail
	}

	return rankKey{
		tier:    worst,
		kind:    kr,
		detail:  detail,
		recency: it.Recency,
		cooccur: it.Cooccur,
		pathQ:   pq,
		idx:     idx,
	}, true
}

// scoreItem: debug int (higher = better). Production sort uses rankKey.less.
func scoreItem(q string, it Item) int {
	k, ok := rankOf(q, it, 0)
	if !ok {
		return -1
	}
	// coarse encoding for tests comparing order loosely
	return int(127-k.tier)*100_000_000 +
		int(k.kind)*1_000_000 +
		int(k.detail%1_000_000) +
		int(k.recency%10_000)*10 +
		int(k.pathQ+127)
}

func fuzzyMatch(query, text string) bool {
	if query == "" {
		return true
	}
	// each token must match text as a label or path
	for _, tok := range strings.Fields(strings.ToLower(query)) {
		if _, ok := matchOnLabel(tok, text); ok {
			continue
		}
		if _, ok := matchOnPath(tok, text); ok {
			continue
		}
		return false
	}
	return true
}

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
