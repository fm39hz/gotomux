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
	if InferSplit("", 2) != "even-horizontal" {
		t.Fatal("default bake")
	}
	// dump -> portable class, not pixel restore
	if InferSplit(dump, 4) != "even-horizontal" {
		t.Fatalf("infer h-dump: %q", InferSplit(dump, 4))
	}
	if InferSplit("", 1) != "" {
		t.Fatal("single pane no layout")
	}
}

func TestSessionTargets(t *testing.T) {
	if sessionTarget("bat-dong-san-web") != "=bat-dong-san-web:" {
		t.Fatal(sessionTarget("bat-dong-san-web"))
	}
	if windowTarget("bat-dong-san-web", 2) != "=bat-dong-san-web:2" {
		t.Fatal(windowTarget("bat-dong-san-web", 2))
	}
	if safeWindowName("bat-dong-san-web", "bat-dong-san-web") != "" {
		t.Fatal("same-as-session name should be dropped")
	}
	if safeWindowName("nvim", "bat-dong-san-web") != "nvim" {
		t.Fatal(safeWindowName("nvim", "bat-dong-san-web"))
	}
	if safeWindowName("/home/x/cache", "s") != "" {
		t.Fatal("path name should be empty")
	}
}

func TestLayoutForShapeClassifiesDump(t *testing.T) {
	h := "ad85,158x35,0,0{40x35,0,0,37,39x35,41,0,38}"
	v := "4080,158x35,0,0[158x17,0,0,100,158x17,0,18,101]"
	nested := "7f98,158x35,0,0{80x35,0,0[80x17,0,0,21,80x17,0,18,22],77x35,81,0[77x17,81,0,23,77x17,81,18,24]}"
	if LayoutForShape(h, 2) != "even-horizontal" {
		t.Fatalf("h dump -> even-horizontal, got %q", LayoutForShape(h, 2))
	}
	if LayoutForShape(v, 2) != "even-vertical" {
		t.Fatalf("v dump -> even-vertical, got %q", LayoutForShape(v, 2))
	}
	if LayoutForShape(nested, 4) != "tiled" {
		t.Fatalf("nested -> tiled, got %q", LayoutForShape(nested, 4))
	}
	if LayoutForShape("even-vertical", 2) != "even-vertical" {
		t.Fatal("named kept")
	}
	if LayoutForShape(h, 1) != "" {
		t.Fatal("single pane")
	}
	if InferSplit("", 3) != "even-horizontal" {
		t.Fatal("bake empty -> even-h")
	}
	if InferSplit(v, 2) != "even-vertical" {
		t.Fatal("InferSplit classifies dump")
	}
}
