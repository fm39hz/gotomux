package model

import "testing"

func TestRestoreRecipeIdentityRequiresReplayAndDoesNotMutateInput(t *testing.T) {
	recipe := Recipe{Name: "work", Cwd: "/work", Windows: []WindowSpec{{Idx: 1, Name: "shell", Panes: []PaneSpec{{Idx: 0, Cwd: "/work", StartCmd: "bash"}}}}}
	baseline := RecipeFromSession(recipe.Session())
	baseline.LogicalID = "logical"
	baseline.Windows[0].SlotID = "window"
	baseline.Windows[0].Panes[0].SlotID = "pane"
	got, err := RestoreRecipeIdentity(recipe, baseline)
	if err != nil {
		t.Fatal(err)
	}
	if got.LogicalID != "logical" || got.Windows[0].SlotID != "window" || got.Windows[0].Panes[0].SlotID != "pane" {
		t.Fatal("restored identity incomplete")
	}
	if recipe.LogicalID != "" || recipe.Windows[0].SlotID != "" || recipe.Windows[0].Panes[0].SlotID != "" {
		t.Fatal("restoration mutated input")
	}
	changed := RecipeFromSession(recipe.Session())
	changed.Windows[0].Panes[0].StartCmd = "different"
	if _, err := RestoreRecipeIdentity(changed, baseline); err == nil {
		t.Fatal("different replay was granted baseline ownership")
	}
	changed = RecipeFromSession(recipe.Session())
	changed.Windows[0].Panes[0].SlotID = "foreign"
	if _, err := RestoreRecipeIdentity(changed, baseline); err == nil {
		t.Fatal("foreign pane identity was overwritten")
	}
}
