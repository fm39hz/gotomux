package tmux

import "testing"

func TestCmdArgs(t *testing.T) {
	got := cmdArgs("nvim foo")
	if len(got) != 2 || got[0] != "nvim" {
		t.Fatalf("%v", got)
	}
}

func TestLayoutNamedOnly(t *testing.T) {
	if LayoutForStore("even-horizontal", 2) != "even-horizontal" {
		t.Fatal("keep named")
	}
	dump := "ad85,158x35,0,0{40x35,0,0,37,39x35,41,0,38}"
	if LayoutForStore(dump, 4) != dump {
		t.Fatal("keep layout dump for multi-pane")
	}
	if LayoutForStore(dump, 1) != "" {
		t.Fatal("single pane drops layout")
	}
	if LayoutForBake("", 2) != "even-horizontal" {
		t.Fatal("default bake")
	}
	if LayoutForBake(dump, 4) != dump {
		t.Fatal("bake uses dump")
	}
	if LayoutForBake("", 1) != "" {
		t.Fatal("single pane no layout")
	}
}
