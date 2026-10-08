package template

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/reconcile"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

type ReconcileOps interface {
	reconcile.Executor
	ActiveWindow(ctx context.Context, name string) (int, error)
}

// ReconcileSession brings the live topology toward an explicit frozen
// baseline. hard rebuilds every window and terminates its existing pane work.
func ReconcileSession(ctx context.Context, ctl ReconcileOps, st store.Storer, name string, hard bool) (string, error) {
	if ctl == nil || st == nil {
		return "", fmt.Errorf("reconcile: nil tmux or store")
	}
	if tmux.IsHiddenSession(name) {
		return "", fmt.Errorf("refusing to reconcile gotomuxd's hidden control session")
	}
	live, err := ctl.Freeze(ctx, name)
	if err != nil {
		return "", err
	}
	if live.Name != name {
		return "", fmt.Errorf("live session identity mismatch: requested %q, observed %q", name, live.Name)
	}
	base, err := st.GetBaseline(name)
	if err != nil {
		return "", err
	}
	if base == nil {
		base, err = st.Get(name)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		if base == nil {
			return "", fmt.Errorf("no reconcile baseline for %q; freeze the session first with gotomux -f", name)
		}
		if err := st.SaveBaseline(base); err != nil {
			return "", fmt.Errorf("migrate baseline %q: %w", name, err)
		}
	}
	if base.Name != "" && base.Name != name {
		return "", fmt.Errorf("baseline identity mismatch: requested %q, baseline contains %q", name, base.Name)
	}

	var report string
	var softPlan *reconcile.Plan
	if hard {
		report, err = reconcile.HardApply(ctx, ctl, name, base, live)
	} else {
		active, activeErr := ctl.ActiveWindow(ctx, name)
		if activeErr != nil {
			return "", fmt.Errorf("active window: %w", activeErr)
		}
		plan := reconcile.Build(base, live, active)
		softPlan = &plan
		targetCwd := base.Cwd
		if targetCwd == "" {
			targetCwd = live.Cwd
		}
		report, err = reconcile.Apply(ctx, ctl, name, targetCwd, plan)
		if len(plan.Ambiguous) > 0 {
			return report, err
		}
	}
	if err != nil {
		return report, err
	}
	if report == "" || strings.HasPrefix(report, "already aligned") || strings.HasPrefix(report, "reconcile skipped") {
		if softPlan != nil && len(softPlan.Matched) == len(base.Windows) && (!hasRuntimeBindings(base) || base.ServerKey != live.ServerKey) {
			updated, err := ctl.Freeze(ctx, name)
			if err != nil {
				return report, fmt.Errorf("reconcile unchanged but runtime binding refresh failed: %w", err)
			}
			if err := st.SaveBaseline(reconcile.RefreshBindings(base, updated)); err != nil {
				return report, fmt.Errorf("reconcile unchanged but runtime binding save failed: %w", err)
			}
		}
		return report, nil
	}
	updated, err := ctl.Freeze(ctx, name)
	if err != nil {
		return report, fmt.Errorf("reconcile applied but verification failed: %w", err)
	}
	if err := reconcile.Verify(base, updated, hard); err != nil {
		return report, fmt.Errorf("reconcile applied but topology verification failed: %w", err)
	}
	base = reconcile.RefreshBindings(base, updated)
	if err := st.SaveBaseline(base); err != nil {
		return report, fmt.Errorf("reconcile applied but runtime binding save failed: %w", err)
	}
	return report, nil
}

func hasRuntimeBindings(s *model.Session) bool {
	if s == nil || s.ServerKey == "" || len(s.Windows) == 0 {
		return false
	}
	for _, w := range s.Windows {
		if w.TmuxID == "" {
			return false
		}
		for _, p := range w.Panes {
			if p.TmuxID == "" {
				return false
			}
		}
	}
	return true
}
