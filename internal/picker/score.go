package picker

import (
	"path/filepath"
	"strings"
	"unicode"

	"github.com/fm39hz/gotomux/internal/store"
	"github.com/junegunn/fzf/src/algo"
	"github.com/junegunn/fzf/src/util"
	"golang.org/x/text/unicode/norm"
)

var fzfSlab = util.MakeSlab(128*1024, 64*1024)

// Ranking: tiered tuple sort (not a single ad-hoc sum).
//
//	tier    - match quality band (lower = better). Nothing outranks tier.
//	recency - app frecency (opens/kills/time) or fallback preset/zoxide (higher = better).
//	cooccur - pair score with current session (higher = better); 0 if no context.
//	kind    - domain preference (higher = better). Tiebreaker within same recency+cooccur.
//	detail  - within-tier match quality (higher = better).
//	busy    - 1 if session has a non-shell tool active (higher = better).
//	pathQ   - shallower path (higher = better): -depth.
//	idx     - stable input order.
//
// Idle (empty q): recency > cooccur > kind > detail > busy > pathQ > idx.
// When ctxSession is set (inside tmux), any item whose Name matches the
// current session is excluded (you're already there). Remaining items sort
// naturally — "just left" surfaces first via recency. Same algorithm
// regardless of environment; ctxSession only adds filter + cooccur signals.
//
// Frecency (usage table): opens with day-decay minus kill penalty - see frecencyScore.
//
// Typed tiers:
//
//	0 token  - full name/basename == q, OR a name segment == q
//	1 prefix - label/segment HasPrefix(q)
//	2 substr - mid-string contains q
//	3 fuzzy  - rune subsequence
//	4 path   - path segments only
//
// Multi-token query (whitespace): AND - every token must match; tier = worst token tier;
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
	detailFullExact int32 = 5_000
	detailSegExact  int32 = 2_000
)

type rankKey struct {
	tier    int8
	recency int64
	cooccur int64
	kind    int8
	detail  int32
	pathQ   int8
	idx     int
}

func (a rankKey) less(b rankKey) bool {
	if a.tier != b.tier {
		return a.tier < b.tier
	}
	if a.recency != b.recency {
		return a.recency > b.recency
	}
	if a.cooccur != b.cooccur {
		return a.cooccur > b.cooccur
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

// camelSplit breaks CamelCase and acronyms: "APIConfiguration" -> API, Configuration.
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
				// XMLParser -> break before P
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

// foldDiacritic: NFD -> strip marks -> lower; Vietnamese đ/Đ -> d.
// Match-time only - stored names stay as-is.
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
			continue // combining marks (acute, grave, horn, ...)
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

		chars := util.ToChars([]byte(text))
		qr := []rune(q)

		if result, _ := algo.PrefixMatch(false, false, true, &chars, qr, false, fzfSlab); result.Start >= 0 {
			best = betterHit(best, fieldHit{tierPrefix, int32(result.Score)})
			ok = true
			return
		}
		if result, _ := algo.ExactMatchNaive(false, false, true, &chars, qr, false, fzfSlab); result.Start >= 0 {
			best = betterHit(best, fieldHit{tierSubstr, int32(result.Score)})
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
	if query == "" {
		return detailBase, true
	}
	qr := []rune(query)
	if len(qr) == 0 {
		return detailBase, true
	}
	tr := []rune(text)
	if len(qr) > len(tr) {
		return 0, false
	}

	chars := util.ToChars([]byte(text))
	result, _ := algo.FuzzyMatchV2(
		false, false, true, &chars, qr, false, fzfSlab,
	)
	if result.Score <= 0 {
		return 0, false
	}
	return int32(result.Score), true
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

// usageRecency maps stored Usage -> rank recency key at "now".
func usageRecency(u store.Usage, now int64) int64 {
	return frecencyScore(u.Opens, u.Kills, u.LastOpen, u.LastKill, now)
}

// rankOf builds the sort key. ok=false -> drop.
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
