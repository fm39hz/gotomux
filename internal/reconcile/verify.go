package reconcile

import (
	"fmt"

	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// Verify checks the topology promised by a completed operation before runtime
// IDs are written back to the persistent baseline.
func Verify(base, live *model.Session, hard bool) error {
	if base == nil || live == nil {
		return fmt.Errorf("missing baseline or live snapshot")
	}
	if hard && len(live.Windows) != len(base.Windows) {
		return fmt.Errorf("hard reconcile produced %d windows; baseline has %d", len(live.Windows), len(base.Windows))
	}
	byIndex := make(map[int]model.Window, len(live.Windows))
	for _, w := range live.Windows {
		byIndex[w.Idx] = w
	}
	for _, want := range base.Windows {
		got, ok := byIndex[want.Idx]
		if !ok {
			return fmt.Errorf("baseline window %q at index %d is missing", want.Name, want.Idx)
		}
		if expectedName := tmux.SafeWindowName(want.Name, base.Name); expectedName != "" && got.Name != expectedName {
			return fmt.Errorf("window %d name is %q; baseline expects %q", want.Idx, got.Name, expectedName)
		}
		if hard && len(got.Panes) != len(want.Panes) {
			return fmt.Errorf("window %d has %d panes; baseline expects %d", want.Idx, len(got.Panes), len(want.Panes))
		}
		if !hard && len(got.Panes) < len(want.Panes) {
			return fmt.Errorf("window %d has %d panes; baseline requires at least %d", want.Idx, len(got.Panes), len(want.Panes))
		}
		if len(got.Panes) == len(want.Panes) && !layoutEquivalent(want.Layout, len(want.Panes), got.Layout, len(got.Panes)) {
			return fmt.Errorf("window %d split topology does not match baseline", want.Idx)
		}
	}
	if hard {
		for idx := range byIndex {
			found := false
			for _, want := range base.Windows {
				if want.Idx == idx {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("hard reconcile left extra window at index %d", idx)
			}
		}
	}
	return nil
}

func layoutEquivalent(a string, aPanes int, b string, bPanes int) bool {
	if tmux.IsLayoutDump(a) && tmux.IsLayoutDump(b) {
		return tmux.LayoutTopology(a) == tmux.LayoutTopology(b)
	}
	return tmux.LayoutForShape(a, aPanes) == tmux.LayoutForShape(b, bPanes)
}
