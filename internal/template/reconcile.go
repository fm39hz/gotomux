package template

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fm39hz/gotomux/internal/identity"
	"github.com/fm39hz/gotomux/internal/model"
	core "github.com/fm39hz/gotomux/internal/reconcile/core"
	"github.com/fm39hz/gotomux/internal/reconcile/runner"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

type ReconcileOps interface {
	runner.TmuxPort
}

// ReconcileSession coordinates one full observe/resolve/plan/run/verify/commit
// cycle. Session names select the live target; persisted logical IDs decide
// which baseline belongs to it.
func ReconcileSession(ctx context.Context, ctl ReconcileOps, st store.Storer, name string, hard bool) (string, error) {
	if ctl == nil || st == nil {
		return "", fmt.Errorf("reconcile: nil tmux or store")
	}
	if name == "" {
		return "", fmt.Errorf("reconcile: empty target session")
	}
	if tmux.IsHiddenSession(name) {
		return "", fmt.Errorf("refusing to reconcile gotomuxd's hidden control session")
	}
	live, err := ctl.Observe(ctx, name)
	if err != nil {
		return "", err
	}
	if !live.Complete || live.Target.Name != name {
		return "", fmt.Errorf("reconcile: target session observation is incomplete or changed")
	}
	base, err := loadReconcileBaseline(st, name, live.Target.LogicalID)
	if err != nil {
		return "", err
	}
	if base == nil {
		return "", fmt.Errorf("no reconcile baseline for %q; freeze the session first with gotomux -f", name)
	}
	if err := requireReconcileBaselineIdentity(*base); err != nil {
		return "", fmt.Errorf("%w; freeze the intended session again with gotomux -f", err)
	}
	policy := core.PolicyPreserve
	if hard {
		policy = core.PolicyExact
	}
	active, err := findActiveJournal(st, *base, live.Target, name)
	if err != nil {
		return "", err
	}
	if active != nil {
		var plan core.PlanResult
		if err := json.Unmarshal([]byte(active.PlanJSON), &plan); err != nil {
			return "", fmt.Errorf("reconcile journal %q has invalid plan JSON: %w", active.ID, err)
		}
		if plan.Policy != policy {
			return "", fmt.Errorf("reconcile journal %q uses policy %q; resume it with the same policy", active.ID, plan.Policy)
		}
		return executeAndCommit(ctx, ctl, st, name, active.ID, plan)
	}

	desired := core.DesiredState{Session: *base}
	desired.Session.Name = live.Target.Name
	stampSemanticMetadata(&desired.Session)
	desired.ClassifierVersion = desired.Session.ClassifierVersion
	resolution := core.Resolve(desired, *live)
	if resolution.Status != core.ResolutionReady {
		return resolutionReport(resolution), nil
	}
	equivalentGroups := make([]core.Ambiguity, 0)
	for _, ambiguity := range resolution.Ambiguous {
		if ambiguity.EquivalentGroup {
			equivalentGroups = append(equivalentGroups, ambiguity)
		}
	}
	plan := core.Plan(desired, *live, resolution, policy)
	if plan.Status == core.PlanBlocked {
		return "", fmt.Errorf("reconcile planning failed: %s", plan.BlockedReason)
	}
	plan.Resolution.Ambiguous = append(plan.Resolution.Ambiguous, equivalentGroups...)
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("serialize reconcile plan: %w", err)
	}
	journalID, err := identity.New()
	if err != nil {
		return "", err
	}
	journal := model.ReconcileJournal{
		ID: journalID, SessionKey: desired.Session.LogicalID, SessionName: name,
		SessionLogicalID: desired.Session.LogicalID, Policy: string(policy),
		Status: model.JournalPlanned, PlanJSON: string(planJSON),
	}
	if err := st.StartReconcile(journal); err != nil {
		if active, getErr := st.GetActiveReconcile(desired.Session.LogicalID); getErr == nil && active != nil {
			return "", fmt.Errorf("reconcile %q is already active as journal %s (%s)", name, active.ID, active.Status)
		}
		return "", err
	}
	return executeAndCommit(ctx, ctl, st, name, journalID, plan)
}

func loadReconcileBaseline(st store.Storer, name, liveLogicalID string) (*model.Session, error) {
	if liveLogicalID != "" {
		base, err := st.GetBaselineByLogicalID(liveLogicalID)
		if err != nil || base != nil {
			return base, err
		}
	}
	base, err := st.GetBaseline(name)
	if err != nil || base != nil {
		return base, err
	}
	// A preset is not sufficient proof of baseline/session identity. Require an
	// explicit freeze before reconciling a manually launched or replaced session.
	preset, err := st.Get(name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if preset != nil {
		return nil, fmt.Errorf("%q has a preset but no frozen reconcile baseline; freeze the live session first with gotomux -f", name)
	}
	return nil, nil
}

func requireReconcileBaselineIdentity(baseline model.Session) error {
	if baseline.SchemaVersion != model.CurrentBaselineSchemaVersion {
		return fmt.Errorf("baseline schema version %d is unsupported", baseline.SchemaVersion)
	}
	if baseline.LogicalID == "" {
		return fmt.Errorf("baseline has no session identity")
	}
	if len(baseline.Windows) == 0 {
		return fmt.Errorf("baseline has no window slots")
	}
	for wi, window := range baseline.Windows {
		if window.SlotID == "" {
			return fmt.Errorf("baseline window %d has no slot identity", wi)
		}
		if len(window.Panes) == 0 {
			return fmt.Errorf("baseline window %d has no panes", wi)
		}
		for pi, pane := range window.Panes {
			if pane.SlotID == "" {
				return fmt.Errorf("baseline window %d pane %d has no slot identity", wi, pi)
			}
		}
	}
	return nil
}

func findActiveJournal(st store.Storer, baseline, live model.Session, name string) (*model.ReconcileJournal, error) {
	keys := []string{baseline.LogicalID, live.LogicalID}
	seen := map[string]bool{}
	for _, key := range keys {
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		journal, err := st.GetActiveReconcile(key)
		if err != nil || journal != nil {
			return journal, err
		}
	}
	// The session option can be manually removed while a journal is active.
	// Recover by the server-scoped tmux session identity recorded in its plan.
	journals, err := st.ListActiveReconciles()
	if err != nil {
		return nil, err
	}
	var matched *model.ReconcileJournal
	for i := range journals {
		var plan core.PlanResult
		if err := json.Unmarshal([]byte(journals[i].PlanJSON), &plan); err != nil {
			continue
		}
		if plan.Resolution.ServerKey != live.ServerKey || plan.Resolution.TargetSession.LiveRuntimeID != live.TmuxID {
			continue
		}
		if matched != nil {
			return nil, fmt.Errorf("multiple active reconcile journals match runtime session %s", live.TmuxID)
		}
		copy := journals[i]
		matched = &copy
	}
	return matched, nil
}

func executeAndCommit(ctx context.Context, ctl ReconcileOps, st store.Storer, sessionName, journalID string, plan core.PlanResult) (string, error) {
	run := runner.Runner{Tmux: ctl, Journal: st}
	if err := run.Execute(ctx, sessionName, journalID, plan); err != nil {
		return "", err
	}
	fresh, err := ctl.Observe(ctx, sessionName)
	if err != nil {
		_ = st.UpdateReconcile(journalID, model.JournalNeedsAttention, len(plan.Operations), "post-run observation failed: "+err.Error())
		return "", fmt.Errorf("reconcile ran but final observation failed: %w", err)
	}
	desired := core.DesiredState{Session: plan.DesiredSession, ClassifierVersion: plan.ClassifierVersion}
	resolution := core.Resolve(desired, *fresh)
	if err := core.Verify(desired, *fresh, resolution, plan.Policy, plan); err != nil {
		_ = st.UpdateReconcile(journalID, model.JournalNeedsAttention, len(plan.Operations), "verification failed: "+err.Error())
		return "", fmt.Errorf("reconcile ran but verification failed: %w", err)
	}
	if plan.Policy == core.PolicyExact {
		if err := clearExactOperationMarkers(ctx, ctl, plan, *fresh); err != nil {
			_ = st.UpdateReconcile(journalID, model.JournalNeedsAttention, len(plan.Operations), "exact marker cleanup failed: "+err.Error())
			return "", fmt.Errorf("exact rebuild verified but recovery marker cleanup failed: %w", err)
		}
		fresh, err = ctl.Observe(ctx, sessionName)
		if err != nil {
			_ = st.UpdateReconcile(journalID, model.JournalNeedsAttention, len(plan.Operations), "post-cleanup observation failed: "+err.Error())
			return "", fmt.Errorf("exact rebuild markers cleared but final observation failed: %w", err)
		}
		resolution = core.Resolve(desired, *fresh)
		if err := core.Verify(desired, *fresh, resolution, plan.Policy, plan); err != nil {
			_ = st.UpdateReconcile(journalID, model.JournalNeedsAttention, len(plan.Operations), "post-cleanup verification failed: "+err.Error())
			return "", fmt.Errorf("exact rebuild did not verify after marker cleanup: %w", err)
		}
	}
	updated, err := core.RefreshBindings(plan.DesiredSession, *fresh, resolution)
	if err != nil {
		_ = st.UpdateReconcile(journalID, model.JournalNeedsAttention, len(plan.Operations), "binding refresh failed: "+err.Error())
		return "", fmt.Errorf("reconcile verified but binding refresh failed: %w", err)
	}
	if err := st.CommitReconcile(&updated, journalID); err != nil {
		return "", fmt.Errorf("reconcile verified but baseline/journal commit failed: %w", err)
	}
	return reconcileReport(plan), nil
}

func clearExactOperationMarkers(ctx context.Context, ctl ReconcileOps, plan core.PlanResult, snapshot model.Snapshot) error {
	var exact *core.Operation
	for i := range plan.Operations {
		if plan.Operations[i].Kind == core.OperationExactRebuild {
			exact = &plan.Operations[i]
			break
		}
	}
	if exact == nil {
		return nil
	}
	for _, baselineWindow := range plan.DesiredSession.Windows {
		var liveWindow *model.Window
		for i := range snapshot.Target.Windows {
			if snapshot.Target.Windows[i].SlotID == baselineWindow.SlotID {
				liveWindow = &snapshot.Target.Windows[i]
				break
			}
		}
		if liveWindow == nil {
			return fmt.Errorf("baseline window slot %s disappeared before marker cleanup", baselineWindow.SlotID)
		}
		windowOperationID := fmt.Sprintf("%s/window/%d", exact.ID, baselineWindow.Idx)
		windowObject := reconcileObjectByRuntime(snapshot.Objects, model.ObjectWindow, liveWindow.TmuxID)
		if windowObject == nil {
			return fmt.Errorf("baseline window %s has no observed operation metadata", liveWindow.TmuxID)
		}
		if windowObject.OperationID != "" && windowObject.OperationID != windowOperationID {
			return fmt.Errorf("baseline window %s carries foreign operation marker %q", liveWindow.TmuxID, windowObject.OperationID)
		}
		if windowObject.OperationID == windowOperationID {
			if err := ctl.ClearOperationID(ctx, model.ObjectWindow, liveWindow.TmuxID); err != nil {
				return fmt.Errorf("clear exact window marker %s: %w", liveWindow.TmuxID, err)
			}
		}
		for paneIndex, baselinePane := range baselineWindow.Panes {
			var livePane *model.Pane
			for i := range liveWindow.Panes {
				if liveWindow.Panes[i].SlotID == baselinePane.SlotID {
					livePane = &liveWindow.Panes[i]
					break
				}
			}
			if livePane == nil {
				return fmt.Errorf("baseline pane slot %s disappeared before marker cleanup", baselinePane.SlotID)
			}
			paneOperationID := fmt.Sprintf("%s/pane/%d", windowOperationID, paneIndex)
			paneObject := reconcileObjectByRuntime(snapshot.Objects, model.ObjectPane, livePane.TmuxID)
			if paneObject == nil {
				return fmt.Errorf("baseline pane %s has no observed operation metadata", livePane.TmuxID)
			}
			if paneObject.OperationID != "" && paneObject.OperationID != paneOperationID {
				return fmt.Errorf("baseline pane %s carries foreign operation marker %q", livePane.TmuxID, paneObject.OperationID)
			}
			if paneObject.OperationID == paneOperationID {
				if err := ctl.ClearOperationID(ctx, model.ObjectPane, livePane.TmuxID); err != nil {
					return fmt.Errorf("clear exact pane marker %s: %w", livePane.TmuxID, err)
				}
			}
		}
	}
	return nil
}

func reconcileObjectByRuntime(objects []model.ObjectIdentity, kind model.ObjectKind, runtimeID string) *model.ObjectIdentity {
	for i := range objects {
		if objects[i].Kind == kind && objects[i].RuntimeID == runtimeID {
			return &objects[i]
		}
	}
	return nil
}

func resolutionReport(resolution core.Resolution) string {
	lines := []string{"reconcile skipped: identity resolution is " + string(resolution.Status)}
	for _, conflict := range resolution.Conflicts {
		line := fmt.Sprintf("  %s: %s", conflict.Baseline, conflict.Reason)
		if len(conflict.Live) > 0 {
			line += " (live: " + strings.Join(conflict.Live, ", ") + ")"
		}
		lines = append(lines, line)
	}
	for _, ambiguous := range resolution.Ambiguous {
		lines = append(lines, fmt.Sprintf("  %s: %s (%s)", ambiguous.Baseline, strings.Join(ambiguous.Live, ", "), ambiguous.Reason))
	}
	return strings.Join(lines, "\n")
}

func reconcileReport(plan core.PlanResult) string {
	groupNote := ""
	groupCount := 0
	for _, ambiguity := range plan.Resolution.Ambiguous {
		if ambiguity.EquivalentGroup {
			groupCount++
		}
	}
	if groupCount > 0 {
		groupNote = fmt.Sprintf("; resolved %d interchangeable live group(s) while preserving their processes", groupCount)
	}
	if plan.Status == core.PlanNoop {
		if len(plan.PreservedWindows) == 0 && len(plan.PreservedPanes) == 0 {
			if groupNote != "" {
				return "already aligned" + groupNote
			}
			return ""
		}
		return fmt.Sprintf("already aligned; preserved %d extra window(s) and %d extra pane(s)%s", len(plan.PreservedWindows), len(plan.PreservedPanes), groupNote)
	}
	if plan.Policy == core.PolicyExact {
		return fmt.Sprintf("hard reconciled: rebuilt %d window(s); prior pane processes were terminated%s", len(plan.DesiredSession.Windows), groupNote)
	}
	moves := map[string]bool{}
	created, repaired := 0, 0
	for _, op := range plan.Operations {
		switch op.Kind {
		case core.OperationMoveWindow:
			moves[op.Target.ID] = true
		case core.OperationCreateWindow:
			created++
		case core.OperationAddPane, core.OperationApplyLayout:
			repaired++
		}
	}
	return fmt.Sprintf("reconciled: %d window move(s), %d recreated, %d repaired, %d extra window(s) and %d extra pane(s) preserved%s", len(moves), created, repaired, len(plan.PreservedWindows), len(plan.PreservedPanes), groupNote)
}
