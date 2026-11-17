package model

import "testing"

func TestEntityIdentityComparisonUsesLogicalOrServerScopedRuntimeIdentity(t *testing.T) {
	tests := []struct {
		name string
		a, b EntityIdentity
		want bool
	}{
		{"logical identity survives rename and path change", EntityIdentity{LogicalID: "l1", RuntimeID: "$1", ServerKey: "s1"}, EntityIdentity{LogicalID: "l1"}, true},
		{"runtime identity requires same server", EntityIdentity{RuntimeID: "$1", ServerKey: "s1"}, EntityIdentity{RuntimeID: "$1", ServerKey: "s2"}, false},
		{"no identity evidence is not equality", EntityIdentity{}, EntityIdentity{}, false},
		{"same runtime within server", EntityIdentity{RuntimeID: "$1", ServerKey: "s1"}, EntityIdentity{RuntimeID: "$1", ServerKey: "s1"}, true},
		{"same project path", EntityIdentity{ProjectPath: "/work/../work/repo"}, EntityIdentity{ProjectPath: "/work/repo"}, true},
		{"conflicting session identity outranks shared project", EntityIdentity{LogicalID: "one", ProjectPath: "/work/repo"}, EntityIdentity{LogicalID: "two", ProjectPath: "/work/repo"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.SameEntity(tt.b); got != tt.want {
				t.Fatalf("SameEntity()=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestRecipeDropsRuntimeAndCopiesTree(t *testing.T) {
	source := Session{Name: "work", LogicalID: "instance", TmuxID: "$1", ServerKey: "server", Windows: []Window{{SlotID: "window", TmuxID: "@1", Panes: []Pane{{SlotID: "pane", TmuxID: "%1", Cmd: "nvim"}}}}}
	recipe := RecipeFromSession(source)
	source.Windows[0].Panes[0].Cmd = "other"
	projected := recipe.Session()
	if projected.TmuxID != "" || projected.ServerKey != "" || projected.Windows[0].TmuxID != "" || projected.Windows[0].Panes[0].TmuxID != "" {
		t.Fatal("recipe leaked runtime handles")
	}
	if projected.Windows[0].Panes[0].Cmd != "nvim" {
		t.Fatal("recipe aliases source")
	}
}

func TestBaselineSeparatesIncarnationFromDesiredState(t *testing.T) {
	source := Session{Name: "work", LogicalID: "instance", TmuxID: "$1", ServerKey: "server", Windows: []Window{{SlotID: "window", TmuxID: "@1", Panes: []Pane{{SlotID: "pane", TmuxID: "%1", Cmd: "nvim"}}}}}
	baseline := BaselineFromSession(source)
	baseline.Bindings.SessionID = "$9"
	baseline.Bindings.Windows["window"] = "@9"
	baseline.Bindings.Panes["pane"] = "%9"
	result := baseline.Session()
	if result.TmuxID != "$9" || result.Windows[0].TmuxID != "@9" || result.Windows[0].Panes[0].TmuxID != "%9" {
		t.Fatal("bindings were not projected by stable slot")
	}
	result.Windows[0].Panes[0].Cmd = "other"
	if baseline.Desired.Windows[0].Panes[0].Cmd != "nvim" || source.TmuxID != "$1" {
		t.Fatal("runtime refresh mutated desired/source")
	}
}

func TestBaselineRecipeRevisionDoesNotRewriteFrozenDesired(t *testing.T) {
	session := Session{LogicalID: "instance", Windows: []Window{{SlotID: "window", Panes: []Pane{{SlotID: "pane", Cmd: "old"}}}}}
	frozen := BaselineFromSession(session)
	revision := RecipeFromSession(session)
	revision.Windows[0].Panes[0].Cmd = "new"
	if frozen.Desired.Windows[0].Panes[0].Cmd != "old" || revision.LogicalID != frozen.Desired.LogicalID {
		t.Fatal("recipe revision changed frozen state or logical lineage")
	}
}
