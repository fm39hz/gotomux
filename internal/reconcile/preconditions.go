package reconcile

import (
	"context"
	"fmt"

	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/tmux"
)

func observe(ctx context.Context, ops Executor, session, serverKey string) (*model.Session, error) {
	state, err := ops.Freeze(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("observe session before operation: %w", err)
	}
	if serverKey == "" || state.ServerKey != serverKey {
		return nil, fmt.Errorf("tmux server generation changed during reconcile; rerun reconciliation")
	}
	return state, nil
}

func findWindow(state *model.Session, windowID string, index int) (model.Window, bool) {
	for _, w := range state.Windows {
		if windowID != "" && w.TmuxID == windowID {
			return w, true
		}
		if windowID == "" && w.Idx == index {
			return w, true
		}
	}
	return model.Window{}, false
}

func hasPaneIDs(w model.Window, ids []string) bool {
	for _, id := range ids {
		if id == "" {
			continue
		}
		found := false
		for _, pane := range w.Panes {
			if pane.TmuxID == id {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func paneIDs(w model.Window) []string {
	ids := make([]string, len(w.Panes))
	for i := range w.Panes {
		ids[i] = w.Panes[i].TmuxID
	}
	return ids
}

func indexOccupied(state *model.Session, idx int, exceptID string) bool {
	for _, w := range state.Windows {
		if w.Idx == idx && (exceptID == "" || w.TmuxID != exceptID) {
			return true
		}
	}
	return false
}

func verifyWindowSnapshot(actual, expected model.Window, expectedIndex int) error {
	if actual.Idx != expectedIndex {
		return fmt.Errorf("window moved from planned index %d to %d", expectedIndex, actual.Idx)
	}
	if expected.Name != actual.Name {
		return fmt.Errorf("window name changed from %q to %q", expected.Name, actual.Name)
	}
	if len(actual.Panes) != len(expected.Panes) {
		return fmt.Errorf("window pane count changed from %d to %d", len(expected.Panes), len(actual.Panes))
	}
	if !hasPaneIDs(actual, paneIDs(expected)) {
		return fmt.Errorf("window pane identities changed")
	}
	if !layoutEquivalent(expected.Layout, len(expected.Panes), actual.Layout, len(actual.Panes)) {
		return fmt.Errorf("window split topology changed")
	}
	return nil
}

func expectedWindowAt(state *model.Session, idx int) error {
	if indexOccupied(state, idx, "") {
		return fmt.Errorf("target window index %d is no longer free", idx)
	}
	return nil
}

func verifyCreatedWindow(state *model.Session, idx int, expected model.Window, session string) error {
	w, ok := findWindow(state, "", idx)
	if !ok {
		return fmt.Errorf("created window at index %d is missing", idx)
	}
	wantPanes := len(expected.Panes)
	if wantPanes == 0 {
		wantPanes = 1
	}
	if len(w.Panes) != wantPanes {
		return fmt.Errorf("created window %d has %d panes; wanted %d", idx, len(w.Panes), wantPanes)
	}
	for i, pane := range w.Panes {
		if pane.TmuxID == "" {
			return fmt.Errorf("created window %d pane %d has no tmux identity", idx, i)
		}
	}
	if expectedName := tmux.SafeWindowName(expected.Name, session); expectedName != "" && w.Name != expectedName {
		return fmt.Errorf("created window %d is named %q; wanted %q", idx, w.Name, expectedName)
	}
	if len(expected.Panes) > 1 && !layoutEquivalent(expected.Layout, wantPanes, w.Layout, len(w.Panes)) {
		return fmt.Errorf("created window %d has a different split topology", idx)
	}
	return nil
}
