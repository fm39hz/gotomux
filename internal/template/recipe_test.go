package template

import (
	"github.com/fm39hz/gotomux/internal/model"
	"testing"
)

func TestRecipeEditKeepsInstanceAndUnchangedSlotsAcrossRename(t *testing.T) {
	old := model.Recipe{Name: "old", LogicalID: "00000000-0000-4000-8000-000000000001", Windows: []model.WindowSpec{{Name: "shell", SlotID: "00000000-0000-4000-8000-000000000002", Panes: []model.PaneSpec{{SlotID: "00000000-0000-4000-8000-000000000003"}}}}}
	edit := model.RecipeFromSession(old.Session())
	edit.Name, edit.LogicalID = "new", ""
	edit.Windows[0].SlotID, edit.Windows[0].Panes[0].SlotID = "", ""
	result, err := reviseRecipe(old, edit)
	if err != nil {
		t.Fatal(err)
	}
	if result.LogicalID != old.LogicalID || result.Windows[0].SlotID != old.Windows[0].SlotID || result.Windows[0].Panes[0].SlotID != old.Windows[0].Panes[0].SlotID {
		t.Fatal("unchanged recipe lost lineage")
	}
	edit.Windows[0].Panes[0].Cmd = "nvim"
	result, err = reviseRecipe(old, edit)
	if err != nil {
		t.Fatal(err)
	}
	if result.LogicalID != old.LogicalID || result.Windows[0].SlotID == old.Windows[0].SlotID {
		t.Fatal("changed recipe did not retain instance with a fresh slot")
	}
	if old.Windows[0].Panes[0].Cmd != "" {
		t.Fatal("edit mutated old recipe")
	}
}
