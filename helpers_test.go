package main

import "testing"

func TestFuzzyUTF8(t *testing.T) {
	if !fuzzyMatch("thư", "thư mục") {
		t.Fatal("utf8 miss")
	}
	if fuzzyMatch("xyz", "abc") {
		t.Fatal("false positive")
	}
}

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

func TestScoreMatchOrder(t *testing.T) {
	// exact > prefix > substring > fuzzy
	e := scoreMatch("foo", "foo")
	p := scoreMatch("foo", "foobar")
	s := scoreMatch("foo", "xfoobar")
	f := scoreMatch("fo", "x-f-o-y") // still matches fuzzy-ish "f" then "o" in path-like
	if !(e > p && p > s) {
		t.Fatalf("exact %d prefix %d substr %d", e, p, s)
	}
	if f < 0 {
		// "fo" vs "x-f-o-y" should match subsequence
		t.Fatal("fuzzy miss")
	}
	if scoreMatch("zzz", "abc") >= 0 {
		t.Fatal("false positive")
	}
}

func TestScoreItemPrefersNameAndActive(t *testing.T) {
	q := "proj"
	active := item{kind: kindActive, name: "proj", path: "/a/proj", title: "[Active] proj"}
	zox := item{kind: kindZoxide, name: "other", path: "/z/proj-extra", title: "[Zoxide] other"}
	// name exact-ish prefix on active should beat path hit on zoxide
	sa, sz := scoreItem(q, active), scoreItem(q, zox)
	if sa < 0 || sz < 0 {
		t.Fatalf("scores %d %d", sa, sz)
	}
	if sa <= sz {
		t.Fatalf("active name should win: active=%d zox=%d", sa, sz)
	}
	// better name match beats kind: zoxide named "proj" vs active weakly matching
	zoxName := item{kind: kindZoxide, name: "proj", path: "/z/proj", title: "[Zoxide] proj"}
	activeWeak := item{kind: kindActive, name: "zzz", path: "/a/something-proj-x", title: "[Active] zzz"}
	if scoreItem(q, zoxName) <= scoreItem(q, activeWeak) {
		t.Fatalf("strong name on zox should beat weak path on active: %d vs %d",
			scoreItem(q, zoxName), scoreItem(q, activeWeak))
	}
}

func TestKindScoreIdleOrder(t *testing.T) {
	c := scoreItem("", item{kind: kindCreate, name: "x"})
	a := scoreItem("", item{kind: kindActive, name: "x"})
	p := scoreItem("", item{kind: kindPreset, name: "x"})
	z := scoreItem("", item{kind: kindZoxide, name: "x"})
	if !(c > a && a > p && p > z) {
		t.Fatalf("idle order C=%d A=%d P=%d Z=%d", c, a, p, z)
	}
}

func TestScoreKhoPrefersActive(t *testing.T) {
	// typing "kho" with live kho-cong should beat zoxide dir literally named "kho"
	q := "kho"
	active := item{kind: kindActive, name: "kho-cong", path: "/home/fm39hz/Workspace/Tecapro/kho-cong", title: "[Active] kho-cong"}
	zox := item{kind: kindZoxide, name: "kho", path: "/home/fm39hz/Workspace/Tecapro/kho-cong/workspace/deploy/kho", title: "[Zoxide] kho"}
	sa, sz := scoreItem(q, active), scoreItem(q, zox)
	if sa <= sz {
		t.Fatalf("want active kho-cong > zox kho: active=%d zox=%d", sa, sz)
	}
}
