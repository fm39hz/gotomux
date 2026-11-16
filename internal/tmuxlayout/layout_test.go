package tmuxlayout

import (
	"encoding/json"
	"strings"
	"testing"
)

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

func TestTopologyParsesTmuxJSONLayoutTree(t *testing.T) {
	a := `{"V":2,"L":{"t":"v","w":80,"h":24,"x":0,"y":0,"c":[{"t":"h","w":80,"h":11,"x":0,"y":0,"c":[{"t":"p","w":39,"h":11,"x":0,"y":0,"i":1,"I":"%0"},{"t":"p","w":40,"h":11,"x":40,"y":0,"i":2,"I":"%1"}]},{"t":"h","w":80,"h":12,"x":0,"y":12,"c":[{"t":"p","w":39,"h":12,"x":0,"y":12,"i":3,"I":"%2"},{"t":"p","w":40,"h":12,"x":40,"y":12,"i":4,"I":"%3"}]}]}}`
	b := `{"L":{"c":[{"c":[{"I":"%8","i":4,"t":"p","w":20,"h":6,"x":0,"y":0},{"I":"%9","i":3,"t":"p","w":21,"h":6,"x":21,"y":0}],"t":"h","w":42,"h":6,"x":0,"y":0},{"c":[{"I":"%10","i":2,"t":"p","w":20,"h":7,"x":0,"y":7},{"I":"%11","i":1,"t":"p","w":21,"h":7,"x":21,"y":7}],"t":"h","w":42,"h":7,"x":0,"y":7}],"t":"v","w":42,"h":14,"x":0,"y":0},"V":2}`
	c := `{"V":2,"L":{"t":"h","w":80,"h":24,"x":0,"y":0,"c":[{"t":"v","w":39,"h":24,"x":0,"y":0,"c":[{"t":"p","i":1,"I":"%0"},{"t":"p","i":2,"I":"%1"}]},{"t":"v","w":40,"h":24,"x":40,"y":0,"c":[{"t":"p","i":3,"I":"%2"},{"t":"p","i":4,"I":"%3"}]}]}}`
	if Topology(a) != Topology(b) {
		t.Fatalf("equivalent JSON split tree differs by geometry, keys, or pane IDs: %q vs %q", Topology(a), Topology(b))
	}
	if Topology(a) == Topology(c) {
		t.Fatalf("different JSON split tree collapsed to %q", Topology(a))
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

func TestRemapPaneIDsFollowsFrozenLayoutLeaves(t *testing.T) {
	layout := `{"V":2,"L":{"t":"v","c":[{"t":"h","c":[{"t":"p","I":"%0"},{"t":"p","I":"%1"}]},{"t":"p","I":"%2"}]}}`
	got, err := RemapPaneIDs(layout, map[string]string{"%0": "%8", "%1": "%9", "%2": "%10"})
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		L json.RawMessage `json:"L"`
	}
	if err := json.Unmarshal([]byte(got), &root); err != nil {
		t.Fatalf("remapped layout is invalid JSON %q: %v", got, err)
	}
	var ids []string
	var visit func(json.RawMessage)
	visit = func(raw json.RawMessage) {
		var node struct {
			Type     string            `json:"t"`
			ID       string            `json:"I"`
			Children []json.RawMessage `json:"c"`
		}
		if err := json.Unmarshal(raw, &node); err != nil {
			t.Fatalf("decode layout node: %v", err)
		}
		if node.Type == "p" {
			ids = append(ids, node.ID)
		}
		for _, child := range node.Children {
			visit(child)
		}
	}
	visit(root.L)
	if strings.Join(ids, ",") != "%8,%9,%10" {
		t.Fatalf("pane IDs in layout leaf order = %v", ids)
	}
}

func TestRemapPaneIDsRejectsAnUnmappedJSONLeaf(t *testing.T) {
	layout := `{"V":2,"L":{"t":"p","I":"%4"}}`
	if _, err := RemapPaneIDs(layout, map[string]string{"%3": "%9"}); err == nil {
		t.Fatal("layout with an unmapped pane leaf was accepted")
	}
}

func TestRemapPaneIDsLeavesLegacyLayoutStringsUnchanged(t *testing.T) {
	layout := "aaaa,80x24,0,0[39x24,0,0,0,40x24,40,0,1]"
	got, err := RemapPaneIDs(layout, map[string]string{"%0": "%8"})
	if err != nil || got != layout {
		t.Fatalf("legacy layout = %q, %v; want unchanged %q", got, err, layout)
	}
}

func TestPaneIDsReturnsJSONLayoutLeafOrder(t *testing.T) {
	layout := `{"V":2,"L":{"t":"v","c":[{"t":"h","c":[{"t":"p","I":"%8"},{"t":"p","I":"%9"}]},{"t":"p","I":"%10"}]}}`
	ids, err := PaneIDs(layout)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "%8,%9,%10" {
		t.Fatalf("pane IDs in layout leaf order = %v", ids)
	}
	if ids, err := PaneIDs("tiled"); err != nil || ids != nil {
		t.Fatalf("named layout pane IDs = %v, %v; want nil IDs", ids, err)
	}
}

func TestRemapPaneIndexesFollowsFrozenPaneSlots(t *testing.T) {
	layout := `{"V":2,"L":{"t":"h","c":[{"t":"p","i":1,"I":"%0"},{"t":"p","i":2,"I":"%1"}]}}`
	got, err := RemapPaneIndexes(layout, map[int]int{1: 3, 2: 4})
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		L json.RawMessage `json:"L"`
	}
	if err := json.Unmarshal([]byte(got), &root); err != nil {
		t.Fatal(err)
	}
	var indexes []int
	var visit func(json.RawMessage)
	visit = func(raw json.RawMessage) {
		var node struct {
			Type     string            `json:"t"`
			Index    int               `json:"i"`
			ID       string            `json:"I"`
			Children []json.RawMessage `json:"c"`
		}
		if err := json.Unmarshal(raw, &node); err != nil {
			t.Fatal(err)
		}
		if node.Type == "p" {
			indexes = append(indexes, node.Index)
		}
		for _, child := range node.Children {
			visit(child)
		}
	}
	visit(root.L)
	if len(indexes) != 2 || indexes[0] != 3 || indexes[1] != 4 {
		t.Fatalf("pane indexes in layout leaf order = %v", indexes)
	}
}
