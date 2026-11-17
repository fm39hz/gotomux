package model

import "github.com/fm39hz/gotomux/internal/tmuxlayout"

// SameReplay compares persisted replay content. Identity, runtime receipts and
// derived semantic metadata are deliberately excluded. Equal replay content
// alone never proves that a live object belongs to this recipe.
func (recipe Recipe) SameReplay(other Recipe) bool {
	if recipe.Name != other.Name || recipe.Cwd != other.Cwd || len(recipe.Windows) != len(other.Windows) {
		return false
	}
	for i, window := range recipe.Windows {
		if !window.SameReplay(other.Windows[i]) {
			return false
		}
	}
	return true
}

func (window WindowSpec) SameReplay(other WindowSpec) bool {
	if window.Idx != other.Idx || window.Name != other.Name || window.Cwd != other.Cwd || len(window.Panes) != len(other.Panes) {
		return false
	}
	if window.Layout != other.Layout {
		equal, known := tmuxlayout.Compare(window.Layout, len(window.Panes), other.Layout, len(other.Panes))
		if !known || !equal {
			return false
		}
	}
	for i, pane := range window.Panes {
		if !pane.SameReplay(other.Panes[i]) {
			return false
		}
	}
	return true
}

func (pane PaneSpec) SameReplay(other PaneSpec) bool {
	return pane.Idx == other.Idx && pane.Cwd == other.Cwd && pane.Cmd == other.Cmd && pane.CmdPath == other.CmdPath && pane.StartCmd == other.StartCmd
}
