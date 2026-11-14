package tmuxlayout

import "testing"

func TestTopologyIgnoresGeometryAndPaneIDsButPreservesSplitTree(t *testing.T) {
	a := "aaaa,100x30,0,0{49x30,0,0,1,50x30,50,0{24x30,50,0,2,25x30,75,0,3}}"
	b := "bbbb,140x40,0,0{69x40,0,0,7,70x40,70,0{34x40,70,0,8,35x40,105,0,9}}"
	c := "cccc,100x30,0,0{49x30,0,0{24x30,0,0,1,25x30,25,0,2},50x30,50,0,3}"
	if Topology(a) != Topology(b) {
		t.Fatalf("equivalent layout topology differs: %q vs %q", Topology(a), Topology(b))
	}
	if Topology(a) == Topology(c) {
		t.Fatalf("different split tree collapsed to %q", Topology(a))
	}
}

func TestEquivalentUsesTopologyForDumpsAndClassForShapes(t *testing.T) {
	a := "aaaa,100x30,0,0{49x30,0,0,1,50x30,50,0,2}"
	b := "bbbb,140x40,0,0{69x40,0,0,8,70x40,70,0,9}"
	if !Equivalent(a, 2, b, 2) {
		t.Fatal("geometry or pane IDs made equivalent split trees differ")
	}
	if Equivalent("even-horizontal", 2, "even-vertical", 2) {
		t.Fatal("different named layouts considered equivalent")
	}
	if !Equivalent("", 1, "", 1) {
		t.Fatal("single-pane layouts should be equivalent")
	}
}
