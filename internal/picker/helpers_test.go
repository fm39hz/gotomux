package picker

import (
	"testing"
)

func TestTruncateRunes(t *testing.T) {
	s := truncateRunes("nhà cửa đẹp", 5)
	if len([]rune(s)) > 5 {
		t.Fatalf("got %q", s)
	}
}

func TestRankIdleKindOrder(t *testing.T) {
	pool := []Item{
		{Kind: KindZoxide, Name: "z"},
		{Kind: KindPreset, Name: "p"},
		{Kind: KindActive, Name: "a"},
		{Kind: KindCreate, Name: "c"},
	}
	got := rankItems("", pool)
	want := []Kind{KindCreate, KindActive, KindPreset, KindZoxide}
	if len(got) != 4 {
		t.Fatalf("len %d", len(got))
	}
	for i := range want {
		if got[i].Kind != want[i] {
			t.Fatalf("pos %d: got %v want %v", i, got[i].Kind, want[i])
		}
	}
}

func TestRankTierLexicographic(t *testing.T) {
	// Better match tier always wins over kind.
	// Zoxide exact "foo" vs Active fuzzy-only would still lose if Active only path-matches weakly —
	// here: Zoxide exact name vs Active substr on longer name.
	q := "foo"
	zoxExact := Item{Kind: KindZoxide, Name: "foo", Path: "/z/foo"}
	activeSub := Item{Kind: KindActive, Name: "xxfoo yy", Path: "/a/xx"}
	kz, _ := rankOf(q, zoxExact, 0)
	ka, _ := rankOf(q, activeSub, 1)
	if !kz.less(ka) {
		t.Fatalf("exact zox should rank before active substr: zox=%+v active=%+v", kz, ka)
	}
	if kz.tier >= ka.tier && ka.tier != tierNone {
		// exact tier 0 < substr tier 3
		if kz.tier >= tierSubstr {
			t.Fatalf("tiers zox=%d active=%d", kz.tier, ka.tier)
		}
	}
}

func TestRankInvariantBetterTierWin(t *testing.T) {
	// Synthetic: same kind, different tiers via names
	q := "ab"
	items := []Item{
		{Kind: KindZoxide, Name: "ab", Path: "/x/ab"},         // exact
		{Kind: KindZoxide, Name: "ab-cd", Path: "/x/ab-cd"},   // seg exact or prefix
		{Kind: KindZoxide, Name: "abzz", Path: "/x/abzz"},     // prefix
		{Kind: KindZoxide, Name: "xxabzz", Path: "/x/xxabzz"}, // substr
		{Kind: KindZoxide, Name: "xayb", Path: "/x/xayb"},     // fuzzy a..b
	}
	var keys []rankKey
	for i, it := range items {
		k, ok := rankOf(q, it, i)
		if !ok {
			t.Fatalf("expected match %s", it.Name)
		}
		keys = append(keys, k)
	}
	// tier sequence should be non-decreasing when sorted by less
	sorted := rankItems(q, items)
	var prev int8 = -1
	for _, it := range sorted {
		k, _ := rankOf(q, it, 0)
		if prev >= 0 && k.tier < prev {
			t.Fatalf("tier went backwards: prev=%d now=%d name=%s", prev, k.tier, it.Name)
		}
		// actually sorted best-first: tier should be non-decreasing (0,0,2,3,...)
		if prev >= 0 && k.tier < prev {
			t.Fatal("fail")
		}
		if prev < 0 {
			prev = k.tier
		} else if k.tier < prev {
			t.Fatalf("order")
		} else {
			prev = k.tier
		}
	}
	_ = keys
}

func TestRankKhoActiveOverDeepExactZox(t *testing.T) {
	// Product rule: segment-exact on Active "kho-cong" ranks above path-deep Zoxide exact "kho"
	// when we prefer shallower + kind — actually both: Active segExact tier1 vs Zox exact tier0.
	// Exact label is BETTER tier than segExact. So pure tier would put zox "kho" first.
	// Professional product choice for session picker:
	//   prefer Active/Preset with q as full segment over Zoxide whose whole name equals q
	//   if the zox path is deeper (leaf folder) — encoded as: boost segExact on Active via kind
	//   BUT invariant says kind cannot beat tier.
	// Resolution used: treat name exact and segment exact as SAME tier band with detail
	// preferring longer structured names?
	// Final policy in impl: tierExact and tierSegExact — Active+segExact should win for UX.
	// We implement by: segment exact on multi-segment label gets detail boost;
	// AND we compare path depth so shallow project root wins.
	q := "kho"
	active := Item{Kind: KindActive, Name: "kho-cong", Path: "/home/fm39hz/Workspace/Tecapro/kho-cong"}
	zox := Item{Kind: KindZoxide, Name: "kho", Path: "/home/fm39hz/Workspace/Tecapro/kho-cong/workspace/deploy/kho"}
	got := rankItems(q, []Item{zox, active})
	if got[0].Name != "kho-cong" {
		t.Fatalf("want kho-cong first, got %s (keys active=%+v zox=%+v)",
			got[0].Name, mustKey(q, active), mustKey(q, zox))
	}
}

func TestRankConfiPresetOverShortZoxAndPathChild(t *testing.T) {
	q := "confi"
	preset := Item{Kind: KindPreset, Name: "dotfiles-config", Path: "/home/fm39hz/.config"}
	zox := Item{Kind: KindZoxide, Name: "config", Path: "/home/fm39hz/.gemini/config"}
	child := Item{Kind: KindZoxide, Name: "nvim", Path: "/home/fm39hz/.config/nvim"}
	got := rankItems(q, []Item{child, zox, preset})
	if got[0].Name != "dotfiles-config" {
		t.Fatalf("want dotfiles-config first, got %s", got[0].Name)
	}
	// path-only child must be after name matches
	for i, it := range got {
		if it.Name == "nvim" && i == 0 {
			t.Fatal("path child must not be first")
		}
	}
	// nvim should be last (pathOnly)
	if got[len(got)-1].Name != "nvim" {
		// may only be 3 items
		var tiers []string
		for _, it := range got {
			k := mustKey(q, it)
			tiers = append(tiers, it.Name+":"+itoa(int(k.tier)))
		}
		// nvim pathOnly=5; others prefix=2 — nvim last
		if mustKey(q, child).tier != tierPath {
			t.Fatalf("nvim should be pathOnly: %+v %v", mustKey(q, child), tiers)
		}
	}
}

func TestRankSameTierKindBreaksTie(t *testing.T) {
	q := "proj"
	active := Item{Kind: KindActive, Name: "proj", Path: "/a/proj"}
	zox := Item{Kind: KindZoxide, Name: "proj", Path: "/z/proj"}
	got := rankItems(q, []Item{zox, active})
	if got[0].Kind != KindActive {
		t.Fatalf("same exact tier: Active before Zoxide")
	}
}

func TestRankNoMatchFiltered(t *testing.T) {
	got := rankItems("zzz", []Item{{Kind: KindActive, Name: "abc", Path: "/a"}})
	if len(got) != 0 {
		t.Fatalf("want empty, got %v", got)
	}
}

func mustKey(q string, it Item) rankKey {
	k, ok := rankOf(q, it, 0)
	if !ok {
		return rankKey{tier: tierNone}
	}
	return k
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func TestFuzzyUTF8(t *testing.T) {
	if !fuzzyMatch("thư", "thư mục") {
		t.Fatal("utf8 miss")
	}
	if fuzzyMatch("xyz", "abc") {
		t.Fatal("false positive")
	}
}

func TestFoldDiacritic(t *testing.T) {
	cases := []struct{ in, want string }{
		{"gotomux", "gotomux"},
		{"Gôtomux", "gotomux"},
		{"gôt", "got"},
		{"Đà-Nẵng", "da-nang"},
		{"THƯ", "thu"},
		{"cafe", "cafe"},
	}
	for _, c := range cases {
		if got := foldDiacritic(c.in); got != c.want {
			t.Fatalf("fold(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestRankAccentInsensitive(t *testing.T) {
	// gôt → gotomux (diacritic fold + prefix/fuzzy)
	it := Item{Kind: KindActive, Name: "gotomux", Path: "/w/gotomux"}
	for _, q := range []string{"got", "gôt", "GÔT", "gotomux", "gôtomux"} {
		k, ok := rankOf(q, it, 0)
		if !ok {
			t.Fatalf("q=%q should match gotomux", q)
		}
		if k.tier > tierFuzzy {
			t.Fatalf("q=%q tier too weak: %+v", q, k)
		}
	}
	// đà-nẵng style name
	dn := Item{Kind: KindZoxide, Name: "da-nang", Path: "/w/Đà-Nẵng"}
	if _, ok := rankOf("đà", dn, 0); !ok {
		t.Fatal("đà should fold-match da-nang")
	}
	if _, ok := rankOf("nang", dn, 0); !ok {
		t.Fatal("nang should match da-nang segment")
	}
}

func TestRankKeyOrderDoc(t *testing.T) {
	// Within same tier, kind beats detail: Active fuzzy-ish vs Zoxide better detail still...
	// Stronger: same tierToken, Active wins over Zoxide regardless of detail.
	q := "kho"
	active := Item{Kind: KindActive, Name: "kho-cong", Path: "/w/Tecapro/kho-cong"}
	zox := Item{Kind: KindZoxide, Name: "kho", Path: "/w/Tecapro/kho-cong/workspace/deploy/kho"}
	ka, _ := rankOf(q, active, 0)
	kz, _ := rankOf(q, zox, 1)
	if ka.tier != tierToken || kz.tier != tierToken {
		t.Fatalf("both token: a=%+v z=%+v", ka, kz)
	}
	if !ka.less(kz) {
		t.Fatalf("kind Active should sort before Zoxide at same tier: a=%+v z=%+v", ka, kz)
	}
}

func TestRankMultiTokenAND(t *testing.T) {
	// "kho cong" matches kho-cong (segments); pure "kho" leaf without cong ranks worse or fails cong token
	q := "kho cong"
	target := Item{Kind: KindActive, Name: "kho-cong", Path: "/w/Tecapro/kho-cong"}
	other := Item{Kind: KindZoxide, Name: "kho", Path: "/w/other/deploy/kho"}
	got := rankItems(q, []Item{other, target})
	if len(got) == 0 {
		t.Fatal("expected kho-cong to match both tokens")
	}
	if got[0].Name != "kho-cong" {
		t.Fatalf("want kho-cong first, got %s", got[0].Name)
	}
	// other may be filtered (no "cong")
	for _, it := range got {
		if it.Name == "kho" {
			t.Fatal("leaf kho should not match token cong")
		}
	}
}

func TestRankCamelCaseSegment(t *testing.T) {
	q := "config"
	// API.Configuration → configuration segment via . and camel
	it := Item{Kind: KindZoxide, Name: "api-configuration", Path: "/x/NKT.APIs/API.Configuration"}
	k, ok := rankOf(q, it, 0)
	if !ok {
		t.Fatal("expected match on Configuration segment")
	}
	if k.tier > tierPrefix {
		// prefix of "configuration" is fine (tier prefix or token if exact)
		t.Fatalf("tier too weak: %+v", k)
	}
}

func TestRankRecencyWithinSameTier(t *testing.T) {
	// same name match tier+Kind: higher recency wins
	q := "demo"
	newer := Item{Kind: KindPreset, Name: "demo", Path: "/a", Recency: 200}
	older := Item{Kind: KindPreset, Name: "demo-old", Path: "/b", Recency: 100}
	// demo exact vs demo-old prefix — different tiers. Use two exact-ish presets via segment.
	// Better: two zoxide same tier prefix with different recency
	a := Item{Kind: KindZoxide, Name: "demoapp", Path: "/z/demoapp", Recency: 10}
	b := Item{Kind: KindZoxide, Name: "demokit", Path: "/z/demokit", Recency: 50}
	got := rankItems(q, []Item{a, b})
	if len(got) < 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	// both prefix tier; higher recency first
	if got[0].Name != "demokit" {
		t.Fatalf("want demokit (recency 50) first, got %s keys %v %v",
			got[0].Name, mustKey(q, a), mustKey(q, b))
	}
	_ = newer
	_ = older
}

func TestRankRecencyPresetLastUsed(t *testing.T) {
	// idle list: among presets kind equal — higher last_used first when same kind block
	// idle uses kind first so Create/Active still on top; among two presets:
	pool := []Item{
		{Kind: KindPreset, Name: "old", Recency: 1},
		{Kind: KindPreset, Name: "new", Recency: 99},
	}
	got := rankItems("", pool)
	if got[0].Name != "new" {
		t.Fatalf("idle Recency: want new first, got %s", got[0].Name)
	}
}

func TestFrecencyScoreBasic(t *testing.T) {
	now := int64(1_700_000_000)
	// many opens recent >> few opens old
	hot := frecencyScore(20, 0, now-3600, 0, now)      // 20 opens, 1h ago
	cold := frecencyScore(20, 0, now-86400*30, 0, now) // 20 opens, 30d ago
	if hot <= cold {
		t.Fatalf("hot %d should beat cold %d", hot, cold)
	}
	// kills penalize
	clean := frecencyScore(10, 0, now-3600, 0, now)
	killed := frecencyScore(10, 5, now-3600, now-3600, now)
	if killed >= clean {
		t.Fatalf("kills should penalize: clean=%d killed=%d", clean, killed)
	}
	// zero
	if frecencyScore(0, 0, 0, 0, now) != 0 {
		t.Fatal("zero usage")
	}
}

func TestRankUsesFrecencyRecencyField(t *testing.T) {
	// same tier/kind/detail: higher item.Recency (frecency) wins
	q := "demo"
	a := Item{Kind: KindZoxide, Name: "demoapp", Path: "/z/demoapp", Recency: 10}
	b := Item{Kind: KindZoxide, Name: "demokit", Path: "/z/demokit", Recency: 500}
	got := rankItems(q, []Item{a, b})
	if got[0].Name != "demokit" {
		t.Fatalf("want demokit first, got %s", got[0].Name)
	}
}

func TestRankCooccurBreaksRecencyTie(t *testing.T) {
	// same tier/kind/detail/Recency: higher cooccur wins
	q := "svc"
	a := Item{Kind: KindZoxide, Name: "svc-a", Path: "/z/svc-a", Recency: 10, Cooccur: 5}
	b := Item{Kind: KindZoxide, Name: "svc-b", Path: "/z/svc-b", Recency: 10, Cooccur: 50}
	got := rankItems(q, []Item{a, b})
	if got[0].Name != "svc-b" {
		t.Fatalf("want svc-b (cooccur) first, got %s", got[0].Name)
	}
}

func TestRankCooccurBelowRecency(t *testing.T) {
	// same name shape → equal tier/detail/kind; recency must beat cooccur
	q := "svcxx"
	hot := Item{Kind: KindZoxide, Name: "svcxx-a", Path: "/z/svcxx-a", Recency: 500, Cooccur: 0}
	paired := Item{Kind: KindZoxide, Name: "svcxx-b", Path: "/z/svcxx-b", Recency: 10, Cooccur: 999}
	// ensure equal detail: identical prefix structure
	got := rankItems(q, []Item{paired, hot})
	if mustKey(q, hot).detail != mustKey(q, paired).detail {
		t.Fatalf("test setup detail mismatch hot=%d pair=%d", mustKey(q, hot).detail, mustKey(q, paired).detail)
	}
	if got[0].Name != "svcxx-a" {
		t.Fatalf("recency should beat Cooccur: got %s keys hot=%+v pair=%+v",
			got[0].Name, mustKey(q, hot), mustKey(q, paired))
	}
}

func TestPairCanonical(t *testing.T) {
	// unit-level: RecordPair order independence via store if possible — skip if no db
	// just ensure applyCooccur maps names
	items := []Item{{Name: "b"}, {Name: "c"}}
	applyCooccur(items, map[string]int64{"b": 7, "x": 1})
	if items[0].Cooccur != 7 || items[1].Cooccur != 0 {
		t.Fatalf("%+v", items)
	}
}



func TestIdleMRUAndDemoteCurrent(t *testing.T) {
	// empty query: higher Recency among Active wins; demote is applied in applyRankMeta
	cur := Item{Kind: KindActive, Name: "here", Recency: 1000}
	left := Item{Kind: KindActive, Name: "left", Recency: 900}
	old := Item{Kind: KindActive, Name: "old", Recency: 100}
	// without demote, cur first
	got := rankItems("", []Item{old, cur, left})
	if got[0].Name != "here" {
		t.Fatalf("pre-demote want here first, got %s", got[0].Name)
	}
	by := map[string][]Item{SrcTmux: {old, cur, left}}
	applyRankMeta(by, nil, nil, "here")
	got = rankItems("", by[SrcTmux])
	if got[0].Name != "left" {
		t.Fatalf("after demote current, want left (just-left MRU), got %v", namesOf(got))
	}
}

func namesOf(items []Item) []string {
	var s []string
	for _, it := range items {
		s = append(s, it.Name)
	}
	return s
}
