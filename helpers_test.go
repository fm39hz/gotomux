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
	if layoutForStore("ab12,40x20,0,0{20x20,0,0,1,20x20,20,0,2}", 2) != "" {
		t.Fatal("drop absolute dump")
	}
	if layoutForBake("", 2) != "even-horizontal" {
		t.Fatal("default bake")
	}
	if layoutForBake("", 1) != "" {
		t.Fatal("single pane no layout")
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

func TestJSONRejectAbsoluteLayout(t *testing.T) {
	_, err := parsePreset(`{"name":"x","windows":[{"layout":"1x2,0,0","panes":[{"cwd":"/"}]}]}`)
	if err == nil {
		t.Fatal("want reject absolute layout")
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
