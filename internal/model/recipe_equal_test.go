package model

import "testing"

func TestReplayEquivalenceExcludesOwnershipButPreservesLaunchContract(t *testing.T) {
	original := Session{Name: "work", Cwd: "/work", LogicalID: "one", TmuxID: "$1", Windows: []Window{{Idx: 1, Name: "editor", SlotID: "window", TmuxID: "@1", Panes: []Pane{{Idx: 0, Cwd: "/work", Cmd: "nvim", StartCmd: "nvim file.txt", SlotID: "pane", TmuxID: "%1"}}}}}
	a := RecipeFromSession(original)
	b := RecipeFromSession(original)
	b.LogicalID = "other"
	b.Windows[0].SlotID = "other-window"
	b.Windows[0].Panes[0].SlotID = "other-pane"
	if !a.SameReplay(b) {
		t.Fatal("ownership metadata changed replay equivalence")
	}
	b.Windows[0].Panes[0].StartCmd = "nvim another.txt"
	if a.SameReplay(b) {
		t.Fatal("launch arguments were ignored")
	}
	b = RecipeFromSession(original)
	b.Windows[0].Panes[0].Idx++
	if a.SameReplay(b) {
		t.Fatal("pane coordinate change was ignored")
	}
}
