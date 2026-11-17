package template

import (
	"fmt"
	"reflect"

	"github.com/fm39hz/gotomux/internal/identity"
	"github.com/fm39hz/gotomux/internal/model"
)

// reviseRecipe preserves logical instance identity through an explicit editor
// operation. Unchanged uniquely identifiable recipe units keep their slots;
// changed/new units receive fresh slots. This is recipe authorship, not a
// claim about ownership of an observed tmux object.
func reviseRecipe(previous, edited model.Recipe) (model.Recipe, error) {
	if previous.LogicalID == "" {
		return edited, nil
	}
	if edited.LogicalID != "" && edited.LogicalID != previous.LogicalID {
		return model.Recipe{}, fmt.Errorf("edited recipe has a conflicting logical instance identity")
	}
	result := model.RecipeFromSession(edited.Session())
	result.LogicalID = previous.LogicalID
	used := map[int]bool{}
	for wi := range result.Windows {
		window := &result.Windows[wi]
		match := -1
		for oi, old := range previous.Windows {
			if used[oi] || !sameWindowSpec(old, *window) {
				continue
			}
			if match >= 0 {
				match = -2
				break
			}
			match = oi
		}
		if match >= 0 {
			old := previous.Windows[match]
			used[match] = true
			window.SlotID = old.SlotID
			for pi := range window.Panes {
				window.Panes[pi].SlotID = old.Panes[pi].SlotID
			}
			continue
		}
		var err error
		window.SlotID, err = identity.New()
		if err != nil {
			return model.Recipe{}, err
		}
		for pi := range window.Panes {
			window.Panes[pi].SlotID, err = identity.New()
			if err != nil {
				return model.Recipe{}, err
			}
		}
	}
	return result, nil
}

func sameWindowSpec(a, b model.WindowSpec) bool {
	a.Idx, b.Idx = 0, 0
	a.SlotID, b.SlotID = "", ""
	a.Role, b.Role = "", ""
	a.Panes = append([]model.PaneSpec(nil), a.Panes...)
	b.Panes = append([]model.PaneSpec(nil), b.Panes...)
	for i := range a.Panes {
		a.Panes[i].Idx = 0
		a.Panes[i].SlotID = ""
	}
	for i := range b.Panes {
		b.Panes[i].Idx = 0
		b.Panes[i].SlotID = ""
	}
	return reflect.DeepEqual(a, b)
}
