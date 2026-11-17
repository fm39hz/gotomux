package model

import "fmt"

// RestoreRecipeIdentity links persisted recipe documents only when their full
// replay content agrees. It never accepts a live observation as ownership
// evidence. The caller persists the returned recipe in its transaction.
func RestoreRecipeIdentity(recipe, baseline Recipe) (Recipe, error) {
	if baseline.LogicalID == "" {
		return Recipe{}, fmt.Errorf("baseline has no logical identity")
	}
	if recipe.LogicalID != "" && recipe.LogicalID != baseline.LogicalID {
		return Recipe{}, fmt.Errorf("recipe and baseline logical identities conflict")
	}
	if !recipe.SameReplay(baseline) {
		return Recipe{}, fmt.Errorf("recipe replay does not match baseline")
	}
	result := RecipeFromSession(recipe.Session())
	result.LogicalID = baseline.LogicalID
	for wi := range result.Windows {
		window := &result.Windows[wi]
		source := baseline.Windows[wi]
		if window.SlotID != "" && window.SlotID != source.SlotID {
			return Recipe{}, fmt.Errorf("window %d identity conflicts with baseline", wi)
		}
		window.SlotID = source.SlotID
		for pi := range window.Panes {
			pane := &window.Panes[pi]
			if pane.SlotID != "" && pane.SlotID != source.Panes[pi].SlotID {
				return Recipe{}, fmt.Errorf("window %d pane %d identity conflicts with baseline", wi, pi)
			}
			pane.SlotID = source.Panes[pi].SlotID
		}
	}
	return result, nil
}
