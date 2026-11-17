package reconcile

import (
	"github.com/fm39hz/gotomux/internal/classify"
	"github.com/fm39hz/gotomux/internal/model"
)

// Semantic roles are desired attributes, never ownership evidence.
func semanticRecipeWindowRole(classifierVersion int, window model.WindowSpec) string {
	if window.Role != "" && classifierVersion == classify.WindowRoleVersion {
		return window.Role
	}
	return classify.WindowRole(window.SessionDTO())
}

func semanticObservedWindowRole(session model.ObservedSession, window model.Window) string {
	if window.Role != "" && session.ClassifierVersion == classify.WindowRoleVersion {
		return window.Role
	}
	return classify.WindowRole(window)
}
