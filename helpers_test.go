package main

import "testing"

func TestTruncateRunes(t *testing.T) {
	s := truncateRunes("nhà cửa đẹp", 5)
	if len([]rune(s)) > 5 {
		t.Fatalf("got %q", s)
	}
}

func TestValidSessionName(t *testing.T) {
	if validSessionName("") || validSessionName("a:b") || !validSessionName("foo-bar") {
		t.Fatal("validSessionName")
	}
}

func TestCmdArgs(t *testing.T) {
	got := cmdArgs("nvim foo")
	if len(got) != 2 || got[0] != "nvim" {
		t.Fatalf("%v", got)
	}
}

func TestLayoutNamedOnly(t *testing.T) {
	if layoutForStore("even-horizontal", 2) != "even-horizontal" {
		t.Fatal("keep named")
	}
	dump := "ad85,158x35,0,0{40x35,0,0,37,39x35,41,0,38}"
	if layoutForStore(dump, 4) != dump {
		t.Fatal("keep layout dump for multi-pane")
	}
	if layoutForStore(dump, 1) != "" {
		t.Fatal("single pane drops layout")
	}
	if layoutForBake("", 2) != "even-horizontal" {
		t.Fatal("default bake")
	}
	if layoutForBake(dump, 4) != dump {
		t.Fatal("bake uses dump")
	}
	if layoutForBake("", 1) != "" {
		t.Fatal("single pane no layout")
	}
}

func TestJSONAllowsLayoutDump(t *testing.T) {
	dump := "7efd,158x35,0,0[158x17,0,0,63,158x17,0,18,64]"
	raw := `{"name":"x","windows":[{"name":"w","layout":"` + dump + `","panes":[{"cwd":"/a"},{"cwd":"/b"}]}]}`
	p, err := parsePreset(raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.Windows[0].Layout != dump {
		t.Fatalf("layout %q", p.Windows[0].Layout)
	}
}

func TestJSONPresetRoundtrip(t *testing.T) {
	raw := `{
  "name": "demo",
  "cwd": "/tmp",
  "windows": [
    {
      "name": "editor",
      "layout": "even-horizontal",
      "panes": [
        {"cwd": "/tmp", "cmd": "nvim"},
        {"cwd": "/tmp/test"}
      ]
    }
  ]
}`
	p, err := parsePreset(raw)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "demo" || len(p.Windows) != 1 || len(p.Windows[0].Panes) != 2 {
		t.Fatalf("%+v", p)
	}
	if p.Windows[0].Panes[0].Cmd != "nvim" {
		t.Fatal("cmd")
	}
	out := formatPreset(p)
	p2, err := parsePreset(out)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Windows[0].Layout != "even-horizontal" {
		t.Fatalf("layout lost: %q", p2.Windows[0].Layout)
	}
}

func TestApplyTemplate(t *testing.T) {
	tmpl := &Preset{
		Name: "default",
		Windows: []PresetWindow{
			{Name: "editor", Panes: []PresetPane{{Cmd: "nvim"}}},
			{Name: "test", Panes: []PresetPane{{Cwd: "test"}, {Cwd: ""}}},
		},
	}
	p := applyTemplate(tmpl, "myproj", "/work/myproj")
	if p.Name != "myproj" || p.Cwd != "/work/myproj" {
		t.Fatalf("root: %+v", p)
	}
	if p.Windows[0].Panes[0].Cwd != "/work/myproj" || p.Windows[0].Panes[0].Cmd != "nvim" {
		t.Fatalf("editor: %+v", p.Windows[0].Panes[0])
	}
	if p.Windows[1].Panes[0].Cwd != "/work/myproj/test" {
		t.Fatalf("rel: %q", p.Windows[1].Panes[0].Cwd)
	}
	if p.Windows[1].Panes[1].Cwd != "/work/myproj" {
		t.Fatalf("empty: %q", p.Windows[1].Panes[1].Cwd)
	}
}

func TestResolveCwd(t *testing.T) {
	if resolveCwd("/a", "") != "/a" {
		t.Fatal("empty")
	}
	if resolveCwd("/a", "b") != "/a/b" {
		t.Fatal("rel")
	}
	if resolveCwd("/a", "/abs") != "/abs" {
		t.Fatal("abs")
	}
}

func TestPresetToTemplate(t *testing.T) {
	p := &Preset{
		Name: "fantasia",
		Cwd:  "/work/Fantasia",
		Windows: []PresetWindow{
			{Name: "editor", Cwd: "/work/Fantasia", Panes: []PresetPane{{Cwd: "/work/Fantasia", Cmd: "nvim"}}},
			{Name: "test", Panes: []PresetPane{{Cwd: "/work/Fantasia/test"}, {Cwd: "/work/Fantasia"}}},
		},
	}
	tmpl := presetToTemplate(p)
	if tmpl.Name != "fantasia" {
		t.Fatal(tmpl.Name)
	}
	if tmpl.Windows[0].Panes[0].Cwd != "" || tmpl.Windows[0].Panes[0].Cmd != "nvim" {
		t.Fatalf("editor pane: %+v", tmpl.Windows[0].Panes[0])
	}
	if tmpl.Windows[1].Panes[0].Cwd != "test" {
		t.Fatalf("rel: %q", tmpl.Windows[1].Panes[0].Cwd)
	}
	if tmpl.Windows[1].Panes[1].Cwd != "" {
		t.Fatalf("root pane: %q", tmpl.Windows[1].Panes[1].Cwd)
	}
	// bake
	got := applyTemplate(tmpl, "other", "/proj/other")
	if got.Windows[0].Panes[0].Cwd != "/proj/other" || got.Windows[0].Panes[0].Cmd != "nvim" {
		t.Fatalf("bake editor: %+v", got.Windows[0].Panes[0])
	}
	if got.Windows[1].Panes[0].Cwd != "/proj/other/test" {
		t.Fatalf("bake test: %q", got.Windows[1].Panes[0].Cwd)
	}
}

func TestRelativizeCwd(t *testing.T) {
	if relativizeCwd("/a", "/a/b") != "b" {
		t.Fatal("rel")
	}
	if relativizeCwd("/a", "/a") != "" {
		t.Fatal("root")
	}
	if relativizeCwd("/a", "/other") != "" {
		t.Fatal("outside")
	}
}

// --- ranking (lexicographic rankKey) ---

func TestRankIdleKindOrder(t *testing.T) {
	pool := []item{
		{kind: kindZoxide, name: "z"},
		{kind: kindPreset, name: "p"},
		{kind: kindActive, name: "a"},
		{kind: kindCreate, name: "c"},
	}
	got := rankItems("", pool)
	want := []kind{kindCreate, kindActive, kindPreset, kindZoxide}
	if len(got) != 4 {
		t.Fatalf("len %d", len(got))
	}
	for i := range want {
		if got[i].kind != want[i] {
			t.Fatalf("pos %d: got %v want %v", i, got[i].kind, want[i])
		}
	}
}

func TestRankTierLexicographic(t *testing.T) {
	// Better match tier always wins over kind.
	// Zoxide exact "foo" vs Active fuzzy-only would still lose if Active only path-matches weakly —
	// here: Zoxide exact name vs Active substr on longer name.
	q := "foo"
	zoxExact := item{kind: kindZoxide, name: "foo", path: "/z/foo"}
	activeSub := item{kind: kindActive, name: "xxfoo yy", path: "/a/xx"}
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
	items := []item{
		{kind: kindZoxide, name: "ab", path: "/x/ab"},         // exact
		{kind: kindZoxide, name: "ab-cd", path: "/x/ab-cd"},   // seg exact or prefix
		{kind: kindZoxide, name: "abzz", path: "/x/abzz"},     // prefix
		{kind: kindZoxide, name: "xxabzz", path: "/x/xxabzz"}, // substr
		{kind: kindZoxide, name: "xayb", path: "/x/xayb"},     // fuzzy a..b
	}
	var keys []rankKey
	for i, it := range items {
		k, ok := rankOf(q, it, i)
		if !ok {
			t.Fatalf("expected match %s", it.name)
		}
		keys = append(keys, k)
	}
	// tier sequence should be non-decreasing when sorted by less
	sorted := rankItems(q, items)
	var prev int8 = -1
	for _, it := range sorted {
		k, _ := rankOf(q, it, 0)
		if prev >= 0 && k.tier < prev {
			t.Fatalf("tier went backwards: prev=%d now=%d name=%s", prev, k.tier, it.name)
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
	active := item{kind: kindActive, name: "kho-cong", path: "/home/fm39hz/Workspace/Tecapro/kho-cong"}
	zox := item{kind: kindZoxide, name: "kho", path: "/home/fm39hz/Workspace/Tecapro/kho-cong/workspace/deploy/kho"}
	got := rankItems(q, []item{zox, active})
	if got[0].name != "kho-cong" {
		t.Fatalf("want kho-cong first, got %s (keys active=%+v zox=%+v)",
			got[0].name, mustKey(q, active), mustKey(q, zox))
	}
}

func TestRankConfiPresetOverShortZoxAndPathChild(t *testing.T) {
	q := "confi"
	preset := item{kind: kindPreset, name: "dotfiles-config", path: "/home/fm39hz/.config"}
	zox := item{kind: kindZoxide, name: "config", path: "/home/fm39hz/.gemini/config"}
	child := item{kind: kindZoxide, name: "nvim", path: "/home/fm39hz/.config/nvim"}
	got := rankItems(q, []item{child, zox, preset})
	if got[0].name != "dotfiles-config" {
		t.Fatalf("want dotfiles-config first, got %s", got[0].name)
	}
	// path-only child must be after name matches
	for i, it := range got {
		if it.name == "nvim" && i == 0 {
			t.Fatal("path child must not be first")
		}
	}
	// nvim should be last (pathOnly)
	if got[len(got)-1].name != "nvim" {
		// may only be 3 items
		var tiers []string
		for _, it := range got {
			k := mustKey(q, it)
			tiers = append(tiers, it.name+":"+itoa(int(k.tier)))
		}
		// nvim pathOnly=5; others prefix=2 — nvim last
		if mustKey(q, child).tier != tierPath {
			t.Fatalf("nvim should be pathOnly: %+v %v", mustKey(q, child), tiers)
		}
	}
}

func TestRankSameTierKindBreaksTie(t *testing.T) {
	q := "proj"
	active := item{kind: kindActive, name: "proj", path: "/a/proj"}
	zox := item{kind: kindZoxide, name: "proj", path: "/z/proj"}
	got := rankItems(q, []item{zox, active})
	if got[0].kind != kindActive {
		t.Fatalf("same exact tier: Active before Zoxide")
	}
}

func TestRankNoMatchFiltered(t *testing.T) {
	got := rankItems("zzz", []item{{kind: kindActive, name: "abc", path: "/a"}})
	if len(got) != 0 {
		t.Fatalf("want empty, got %v", got)
	}
}

func mustKey(q string, it item) rankKey {
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

func TestRankKeyOrderDoc(t *testing.T) {
	// Within same tier, kind beats detail: Active fuzzy-ish vs Zoxide better detail still...
	// Stronger: same tierToken, Active wins over Zoxide regardless of detail.
	q := "kho"
	active := item{kind: kindActive, name: "kho-cong", path: "/w/Tecapro/kho-cong"}
	zox := item{kind: kindZoxide, name: "kho", path: "/w/Tecapro/kho-cong/workspace/deploy/kho"}
	ka, _ := rankOf(q, active, 0)
	kz, _ := rankOf(q, zox, 1)
	if ka.tier != tierToken || kz.tier != tierToken {
		t.Fatalf("both token: a=%+v z=%+v", ka, kz)
	}
	if !ka.less(kz) {
		t.Fatalf("kind Active should sort before Zoxide at same tier: a=%+v z=%+v", ka, kz)
	}
}
