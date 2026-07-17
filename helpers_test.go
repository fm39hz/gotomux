package main

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestRankMultiTokenAND(t *testing.T) {
	// "kho cong" matches kho-cong (segments); pure "kho" leaf without cong ranks worse or fails cong token
	q := "kho cong"
	target := item{kind: kindActive, name: "kho-cong", path: "/w/Tecapro/kho-cong"}
	other := item{kind: kindZoxide, name: "kho", path: "/w/other/deploy/kho"}
	got := rankItems(q, []item{other, target})
	if len(got) == 0 {
		t.Fatal("expected kho-cong to match both tokens")
	}
	if got[0].name != "kho-cong" {
		t.Fatalf("want kho-cong first, got %s", got[0].name)
	}
	// other may be filtered (no "cong")
	for _, it := range got {
		if it.name == "kho" {
			t.Fatal("leaf kho should not match token cong")
		}
	}
}

func TestRankCamelCaseSegment(t *testing.T) {
	q := "config"
	// API.Configuration → configuration segment via . and camel
	it := item{kind: kindZoxide, name: "api-configuration", path: "/x/NKT.APIs/API.Configuration"}
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
	// same name match tier+kind: higher recency wins
	q := "demo"
	newer := item{kind: kindPreset, name: "demo", path: "/a", recency: 200}
	older := item{kind: kindPreset, name: "demo-old", path: "/b", recency: 100}
	// demo exact vs demo-old prefix — different tiers. Use two exact-ish presets via segment.
	// Better: two zoxide same tier prefix with different recency
	a := item{kind: kindZoxide, name: "demoapp", path: "/z/demoapp", recency: 10}
	b := item{kind: kindZoxide, name: "demokit", path: "/z/demokit", recency: 50}
	got := rankItems(q, []item{a, b})
	if len(got) < 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	// both prefix tier; higher recency first
	if got[0].name != "demokit" {
		t.Fatalf("want demokit (recency 50) first, got %s keys %v %v",
			got[0].name, mustKey(q, a), mustKey(q, b))
	}
	_ = newer
	_ = older
}

func TestRankRecencyPresetLastUsed(t *testing.T) {
	// idle list: among presets kind equal — higher last_used first when same kind block
	// idle uses kind first so Create/Active still on top; among two presets:
	pool := []item{
		{kind: kindPreset, name: "old", recency: 1},
		{kind: kindPreset, name: "new", recency: 99},
	}
	got := rankItems("", pool)
	if got[0].name != "new" {
		t.Fatalf("idle recency: want new first, got %s", got[0].name)
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
	// same tier/kind/detail: higher item.recency (frecency) wins
	q := "demo"
	a := item{kind: kindZoxide, name: "demoapp", path: "/z/demoapp", recency: 10}
	b := item{kind: kindZoxide, name: "demokit", path: "/z/demokit", recency: 500}
	got := rankItems(q, []item{a, b})
	if got[0].name != "demokit" {
		t.Fatalf("want demokit first, got %s", got[0].name)
	}
}

func TestFindProjectRootNearest(t *testing.T) {
	dir := t.TempDir()
	// repo/
	//   go.mod
	//   pkg/x/
	repo := filepath.Join(dir, "repo")
	sub := filepath.Join(repo, "pkg", "x")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := findProjectRoot(sub)
	if got != repo {
		t.Fatalf("got %q want %q", got, repo)
	}
}

func TestFindProjectRootDotnet(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "MyApp")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "MyApp.csproj"), []byte("<Project/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := findProjectRoot(app)
	if got != app {
		t.Fatalf("got %q want %q", got, app)
	}
}

func TestSessionNameGenericPrefix(t *testing.T) {
	// .../Foo/web → foo-web
	name := sessionName("/work/Foo/web")
	if name != "foo-web" {
		t.Fatalf("got %q", name)
	}
	// underscore → hyphen
	if n := sessionName("/work/my_app"); n != "my-app" {
		t.Fatalf("got %q", n)
	}
}

func TestProjectSessionSubdir(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "grimoire")
	sub := filepath.Join(repo, "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "package.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	name, root := projectSession(sub)
	if root != repo {
		t.Fatalf("root %q want %q", root, repo)
	}
	if name != "grimoire" {
		t.Fatalf("name %q", name)
	}
}

func TestRankCooccurBreaksRecencyTie(t *testing.T) {
	// same tier/kind/detail/recency: higher cooccur wins
	q := "svc"
	a := item{kind: kindZoxide, name: "svc-a", path: "/z/svc-a", recency: 10, cooccur: 5}
	b := item{kind: kindZoxide, name: "svc-b", path: "/z/svc-b", recency: 10, cooccur: 50}
	got := rankItems(q, []item{a, b})
	if got[0].name != "svc-b" {
		t.Fatalf("want svc-b (cooccur) first, got %s", got[0].name)
	}
}

func TestRankCooccurBelowRecency(t *testing.T) {
	// same name shape → equal tier/detail/kind; recency must beat cooccur
	q := "svcxx"
	hot := item{kind: kindZoxide, name: "svcxx-a", path: "/z/svcxx-a", recency: 500, cooccur: 0}
	paired := item{kind: kindZoxide, name: "svcxx-b", path: "/z/svcxx-b", recency: 10, cooccur: 999}
	// ensure equal detail: identical prefix structure
	got := rankItems(q, []item{paired, hot})
	if mustKey(q, hot).detail != mustKey(q, paired).detail {
		t.Fatalf("test setup detail mismatch hot=%d pair=%d", mustKey(q, hot).detail, mustKey(q, paired).detail)
	}
	if got[0].name != "svcxx-a" {
		t.Fatalf("recency should beat cooccur: got %s keys hot=%+v pair=%+v",
			got[0].name, mustKey(q, hot), mustKey(q, paired))
	}
}

func TestPairCanonical(t *testing.T) {
	// unit-level: RecordPair order independence via store if possible — skip if no db
	// just ensure applyCooccur maps names
	items := []item{{name: "b"}, {name: "c"}}
	applyCooccur(items, map[string]int64{"b": 7, "x": 1})
	if items[0].cooccur != 7 || items[1].cooccur != 0 {
		t.Fatalf("%+v", items)
	}
}
