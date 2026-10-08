package reconcile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/fm39hz/gotomux/internal/model"
)

// HardApply rebuilds every baseline window and removes every current window.
// Callers must obtain explicit user confirmation before invoking it.
func HardApply(ctx context.Context, ops Executor, session string, base, live *model.Session) (report string, retErr error) {
	if ops == nil || base == nil || live == nil || len(base.Windows) == 0 {
		return "", fmt.Errorf("hard reconcile: missing executor or non-empty baseline")
	}
	if live.ServerKey == "" {
		return "", fmt.Errorf("hard reconcile cannot verify tmux server identity")
	}
	if err := validateHardBaseline(base, live); err != nil {
		return "", err
	}
	current, err := observe(ctx, ops, session, live.ServerKey)
	if err != nil {
		return "", err
	}
	if err := samePlannedSession(live, current); err != nil {
		return "", err
	}
	restoreOption, err := disableRenumber(ctx, ops, session)
	if err != nil {
		return "", err
	}
	defer func() { retErr = combineRestoreError(retErr, restoreOption()) }()

	anchor := maxWindowIndex(live.Windows)
	if baseMax := maxWindowIndex(base.Windows); baseMax > anchor {
		anchor = baseMax
	}
	anchor++
	root := base.Cwd
	if root == "" {
		root = live.Cwd
	}
	tmp := model.Window{Name: "__gotomux_reconcile_anchor", Cwd: root, Panes: []model.Pane{{
		Cwd: root, Cmd: "sleep", StartCmd: "sleep 86400",
	}}}
	beforeAnchor, err := observe(ctx, ops, session, live.ServerKey)
	if err != nil {
		return "", err
	}
	if err := samePlannedSession(live, beforeAnchor); err != nil {
		return "", err
	}
	if err := expectedWindowAt(beforeAnchor, anchor); err != nil {
		return "", err
	}
	if err := ops.NewWindowAt(ctx, session, anchor, tmp, root); err != nil {
		return "", fmt.Errorf("create temporary anchor window: %w", err)
	}
	state, err := observe(ctx, ops, session, live.ServerKey)
	if err != nil {
		return "", err
	}
	if err := verifyCreatedWindow(state, anchor, tmp, session); err != nil {
		return "", fmt.Errorf("temporary anchor verification failed: %w", err)
	}
	anchorWindow, _ := findWindow(state, "", anchor)

	old := append([]model.Window(nil), live.Windows...)
	sort.Slice(old, func(i, j int) bool { return old[i].Idx > old[j].Idx })
	remaining := append([]model.Window(nil), old...)
	for _, w := range old {
		before, err := observe(ctx, ops, session, live.ServerKey)
		if err != nil {
			return "", err
		}
		if err := verifyHardWindowSet(before, remaining, anchorWindow); err != nil {
			return "", fmt.Errorf("hard rebuild state changed before removing window %d: %w", w.Idx, err)
		}
		currentWindow, ok := findWindow(before, w.TmuxID, w.Idx)
		if !ok || verifyWindowSnapshot(currentWindow, w, w.Idx) != nil {
			return "", fmt.Errorf("existing window %d changed before hard rebuild", w.Idx)
		}
		anchorWindow, ok = findWindow(before, anchorWindow.TmuxID, anchor)
		if !ok {
			return "", fmt.Errorf("temporary anchor window disappeared before hard rebuild")
		}
		if err := ops.KillWindowAt(ctx, session, w.TmuxID, w.Idx); err != nil {
			return "", fmt.Errorf("remove existing window %d: %w", w.Idx, err)
		}
		for i, current := range remaining {
			if current.TmuxID == w.TmuxID {
				remaining = append(remaining[:i], remaining[i+1:]...)
				break
			}
		}
		after, err := observe(ctx, ops, session, live.ServerKey)
		if err != nil {
			return "", err
		}
		if err := verifyHardWindowSet(after, remaining, anchorWindow); err != nil {
			return "", fmt.Errorf("hard rebuild state changed after removing window %d: %w", w.Idx, err)
		}
	}
	created := make([]model.Window, 0, len(base.Windows))
	for _, source := range base.Windows {
		before, err := observe(ctx, ops, session, live.ServerKey)
		if err != nil {
			return "", err
		}
		if err := verifyHardWindowSet(before, created, anchorWindow); err != nil {
			return "", fmt.Errorf("hard rebuild state changed before creating window %d: %w", source.Idx, err)
		}
		if err := expectedWindowAt(before, source.Idx); err != nil {
			return "", err
		}
		w := source
		w.TmuxID = ""
		// Freeze stores the full tmux layout dump for instance baselines. Keep
		// it here; reducing it to a layout class would lose the frozen split tree.
		w.Layout = source.Layout
		w.Panes = append([]model.Pane(nil), source.Panes...)
		for i := range w.Panes {
			w.Panes[i].TmuxID = ""
		}
		if err := ops.NewWindowAt(ctx, session, source.Idx, w, root); err != nil {
			return "", fmt.Errorf("recreate baseline window %d: %w", source.Idx, err)
		}
		after, err := observe(ctx, ops, session, live.ServerKey)
		if err != nil {
			return "", err
		}
		if err := verifyCreatedWindow(after, source.Idx, w, session); err != nil {
			return "", fmt.Errorf("baseline window %d verification failed: %w", source.Idx, err)
		}
		createdWindow, ok := findWindow(after, "", source.Idx)
		if !ok {
			return "", fmt.Errorf("baseline window %d disappeared after creation", source.Idx)
		}
		created = append(created, createdWindow)
	}
	beforeAnchor, err = observe(ctx, ops, session, live.ServerKey)
	if err != nil {
		return "", err
	}
	if err := verifyHardWindowSet(beforeAnchor, created, anchorWindow); err != nil {
		return "", fmt.Errorf("hard rebuild state changed before anchor cleanup: %w", err)
	}
	if err := ops.KillWindowAt(ctx, session, anchorWindow.TmuxID, anchor); err != nil {
		return "", fmt.Errorf("remove temporary anchor: %w", err)
	}
	afterAnchor, err := observe(ctx, ops, session, live.ServerKey)
	if err != nil {
		return "", err
	}
	if _, ok := findWindow(afterAnchor, anchorWindow.TmuxID, anchor); ok {
		return "", fmt.Errorf("temporary anchor window remained after cleanup")
	}
	if err := Verify(base, afterAnchor, true); err != nil {
		return "", fmt.Errorf("hard rebuild did not reach baseline: %w", err)
	}
	_ = ops.SelectWindow(ctx, session, base.Windows[0].Idx)
	return fmt.Sprintf("hard reconciled: rebuilt %d window(s); prior pane processes were terminated", len(base.Windows)), nil
}

func verifyHardWindowSet(state *model.Session, expected []model.Window, anchor model.Window) error {
	if state == nil || len(state.Windows) != len(expected)+1 {
		return fmt.Errorf("expected %d baseline windows plus anchor; observed %d", len(expected), len(state.Windows))
	}
	if _, ok := findWindow(state, anchor.TmuxID, anchor.Idx); !ok {
		return fmt.Errorf("temporary anchor identity or index changed")
	}
	for _, w := range expected {
		actual, ok := findWindow(state, w.TmuxID, w.Idx)
		if !ok {
			return fmt.Errorf("expected window %q at index %d is missing", w.Name, w.Idx)
		}
		if err := verifyWindowSnapshot(actual, w, w.Idx); err != nil {
			return fmt.Errorf("window %q changed: %w", w.Name, err)
		}
	}
	return nil
}

func samePlannedSession(planned, observed *model.Session) error {
	if planned == nil || observed == nil || planned.ServerKey == "" || planned.ServerKey != observed.ServerKey {
		return fmt.Errorf("tmux server generation changed before reconciliation; rerun")
	}
	if len(planned.Windows) != len(observed.Windows) {
		return fmt.Errorf("session window membership changed before reconciliation; rerun")
	}
	for _, expected := range planned.Windows {
		actual, ok := findWindow(observed, expected.TmuxID, expected.Idx)
		if !ok || verifyWindowSnapshot(actual, expected, expected.Idx) != nil {
			return fmt.Errorf("window %d changed before reconciliation; rerun", expected.Idx)
		}
	}
	return nil
}

func validateHardBaseline(base, live *model.Session) error {
	seen := make(map[int]bool, len(base.Windows))
	root := base.Cwd
	if root == "" {
		root = live.Cwd
	}
	for _, w := range base.Windows {
		if w.Idx < 0 || seen[w.Idx] {
			return fmt.Errorf("hard reconcile: baseline has invalid or duplicate window index %d", w.Idx)
		}
		seen[w.Idx] = true
		if len(w.Panes) == 0 {
			return fmt.Errorf("hard reconcile: baseline window %d has no panes", w.Idx)
		}
		if err := validateWindowPaths(w, root); err != nil {
			return fmt.Errorf("hard reconcile baseline window %d: %w", w.Idx, err)
		}
	}
	if root != "" {
		info, err := os.Stat(filepath.Clean(root))
		if err != nil || !info.IsDir() {
			return fmt.Errorf("hard reconcile: baseline root %q is unavailable", root)
		}
	}
	return nil
}
