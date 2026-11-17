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
	"github.com/fm39hz/gotomux/internal/sessionguard"
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
	release, err := sessionguard.Acquire(live.ServerKey, live.TargetSessionID)
	if err != nil {
		return "", err
	}
	defer func() { _ = release() }()
	// Observation precedes locking only to discover the incarnation key.
	// Re-observe under the lock before selecting a baseline or mutating tmux.
	locked, err := ctl.Observe(ctx, name)
	if err != nil {
		return "", err
	}
	if !locked.Complete || locked.ServerKey != live.ServerKey || locked.TargetSessionID != live.TargetSessionID {
		return "", fmt.Errorf("session incarnation changed before reconcile lock was acquired")
	}
	live = locked
	base, err := loadReconcileBaseline(st, name, live.Target.LogicalID)
	if err != nil {
		return "", err
	}
	if base == nil {
		return "", fmt.Errorf("no reconcile baseline for %q; freeze the session first with gotomux -f", name)
	}
	baseSession := base.Session()
	if err := requireReconcileBaselineIdentity(baseSession); err != nil {
		return "", fmt.Errorf("%w; freeze the intended session again with gotomux -f", err)
	}
	policy := core.PolicyPreserve
	if hard {
		policy = core.PolicyExact
	}
	active, err := findActiveJournal(st, baseSession, live.Target.Session(), name)
	if err != nil {
		return "", err
	}
	if active != nil {
		plan, decodeErr := decodeJournalPlan(st, active)
		if decodeErr != nil {
			return "", decodeErr
		}
		if plan == nil {
			active = nil // safe legacy/unsupported plan was aborted before mutation
		} else if plan.Policy != policy {
			return "", fmt.Errorf("reconcile journal %q uses policy %q; resume it with the same policy", active.ID, plan.Policy)
		} else if active.Status == model.JournalNeedsAttention && len(plan.Operations) > 0 && active.CompletedStep == len(plan.Operations) {
			// The cursor proves every operation's postcondition was observed. A
			// repeated terminal verification must not execute that operation list
			// again. Commit if the current complete observation now verifies;
			// otherwise only preserve may replan, and only after operation markers
			// prove there is no staged/uncertain object to recover.
			desired := core.DesiredState{Baseline: plan.DesiredBaseline, ClassifierVersion: plan.ClassifierVersion}
			resolution := core.Resolve(desired, *live)
			verifyErr := core.Verify(desired, *live, resolution, plan.Policy, *plan)
			if verifyErr == nil {
				return executeAndCommit(ctx, ctl, st, name, active.ID, *plan)
			}
			if plan.Policy != core.PolicyPreserve || hasTargetOperationMarkers(*live, *plan) {
				return "", fmt.Errorf("reconcile journal %q is at terminal verification with no safe automatic recovery: %w; journal remains needs_attention", active.ID, verifyErr)
			}
			if err := st.UpdateReconcile(active.ID, model.JournalAborted, active.CompletedStep, "terminal verification failed; no operation markers remain; replanning from a fresh snapshot: "+verifyErr.Error()); err != nil {
				return "", fmt.Errorf("abort terminal reconcile journal %q before replanning: %w", active.ID, err)
			}
			active = nil
		} else if plan.Status == core.PlanNoop && active.Status == model.JournalNeedsAttention {
			// A no-op journal has no tmux mutations to recover. If its verification
			// failed, its plan is stale by definition; release the claim and resolve
			// the current snapshot again instead of trapping the session behind it.
			if err := st.UpdateReconcile(active.ID, model.JournalAborted, active.CompletedStep, "stale no-op plan failed verification; recomputing from current snapshot"); err != nil {
				return "", fmt.Errorf("abort stale no-op reconcile journal %q: %w", active.ID, err)
			}
		} else if active != nil {
			return executeAndCommit(ctx, ctl, st, name, active.ID, *plan)
		}
	}

	baseSession.Name = live.Target.Name
	stampSemanticMetadata(&baseSession)
	base.Desired = model.RecipeFromSession(baseSession)
	base.ClassifierVersion = baseSession.ClassifierVersion
	desired := core.DesiredState{Baseline: *base, ClassifierVersion: base.ClassifierVersion}
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
		ID: journalID, SessionKey: desired.Baseline.Desired.LogicalID, SessionName: name,
		SessionLogicalID: desired.Baseline.Desired.LogicalID, Policy: string(policy),
		Status: model.JournalPlanned, PlanJSON: string(planJSON),
	}
	if err := st.StartReconcile(journal); err != nil {
		if active, getErr := st.GetActiveReconcile(desired.Baseline.Desired.LogicalID); getErr == nil && active != nil {
			return "", fmt.Errorf("reconcile %q is already active as journal %s (%s)", name, active.ID, active.Status)
		}
		return "", err
	}
	return executeAndCommit(ctx, ctl, st, name, journalID, plan)
}

// decodeJournalPlan is the only entry point for persisted plans. Version zero
// predates stable layout-slot bindings. It is safe to discard only a journal
// that has not started; cursor zero on a running/attention journal is
// ambiguous because tmux may have changed before its cursor was persisted.
func decodeJournalPlan(st store.Storer, journal *model.ReconcileJournal) (*core.PlanResult, error) {
	var plan core.PlanResult
	if err := json.Unmarshal([]byte(journal.PlanJSON), &plan); err != nil {
		return nil, fmt.Errorf("reconcile journal %q has invalid plan JSON at cursor %d: %w", journal.ID, journal.CompletedStep, err)
	}
	if plan.PlanSchemaVersion == core.CurrentPlanSchemaVersion {
		if plan.ID == "" || plan.Policy.Validate() != nil || plan.DesiredBaseline.Session().LogicalID != journal.SessionLogicalID || plan.Resolution.ServerKey == "" || plan.Resolution.TargetSession.LiveRuntimeID == "" {
			return nil, quarantineJournal(st, journal, "plan identity or policy envelope is incomplete")
		}
		if journal.CompletedStep < 0 || journal.CompletedStep > len(plan.Operations) {
			return nil, quarantineJournal(st, journal, fmt.Sprintf("cursor %d is outside plan length %d", journal.CompletedStep, len(plan.Operations)))
		}
		return &plan, nil
	}
	if journal.CompletedStep == 0 && journal.Status == model.JournalPlanned {
		if err := st.UpdateReconcile(journal.ID, model.JournalAborted, 0, fmt.Sprintf("plan schema version %d is unsupported; safely replanned before execution", plan.PlanSchemaVersion)); err != nil {
			return nil, fmt.Errorf("abort unsupported unstarted reconcile journal %q: %w", journal.ID, err)
		}
		return nil, nil
	}
	if journal.Status != model.JournalNeedsAttention {
		if err := st.UpdateReconcile(journal.ID, model.JournalNeedsAttention, journal.CompletedStep, fmt.Sprintf("unsupported plan schema version %d; mutation state must be inspected", plan.PlanSchemaVersion)); err != nil {
			return nil, fmt.Errorf("unsupported plan schema version %d and failed to quarantine journal %q: %w", plan.PlanSchemaVersion, journal.ID, err)
		}
	}
	return nil, fmt.Errorf("reconcile journal %q uses unsupported plan schema version %d at cursor %d; no operations were replayed and the journal remains needs_attention", journal.ID, plan.PlanSchemaVersion, journal.CompletedStep)
}

func quarantineJournal(st store.Storer, journal *model.ReconcileJournal, reason string) error {
	if journal.CompletedStep == 0 && journal.Status == model.JournalPlanned {
		if err := st.UpdateReconcile(journal.ID, model.JournalAborted, 0, "invalid unstarted plan: "+reason); err != nil {
			return fmt.Errorf("invalid plan (%s); failed to abort unstarted journal: %w", reason, err)
		}
		return fmt.Errorf("invalid unstarted reconcile plan: %s; journal aborted before execution", reason)
	}
	if journal.Status != model.JournalNeedsAttention {
		if err := st.UpdateReconcile(journal.ID, model.JournalNeedsAttention, journal.CompletedStep, "invalid persisted plan: "+reason); err != nil {
			return fmt.Errorf("invalid plan (%s); failed to quarantine journal: %w", reason, err)
		}
	}
	return fmt.Errorf("invalid reconcile plan: %s; journal %q remains needs_attention", reason, journal.ID)
}

func hasTargetOperationMarkers(snapshot model.Snapshot, plan core.PlanResult) bool {
	targetWindows := make(map[string]bool, len(snapshot.Target.Windows))
	for _, window := range snapshot.Target.Windows {
		targetWindows[window.TmuxID] = true
	}
	for _, object := range snapshot.Objects {
		if object.OperationID == "" {
			continue
		}
		for _, operation := range plan.Operations {
			if object.OperationID == operation.ID || strings.HasPrefix(object.OperationID, operation.ID+"/") {
				return true
			}
		}

		switch object.Kind {
		case model.ObjectSession:
			if object.RuntimeID == snapshot.TargetSessionID {
				return true
			}
		case model.ObjectWindow:
			if isWindowMemberOf(snapshot.Memberships, snapshot.TargetSessionID, object.RuntimeID) {
				return true
			}
		case model.ObjectPane:
			if targetWindows[object.ParentID] {
				return true
			}
		}
	}
	return false
}

func isWindowMemberOf(memberships []model.WindowMembership, sessionID, windowID string) bool {
	for _, membership := range memberships {
		if membership.SessionID == sessionID && membership.WindowID == windowID {
			return true
		}
	}
	return false
}

func loadReconcileBaseline(st store.Storer, name, liveLogicalID string) (*model.Baseline, error) {
	if liveLogicalID != "" {
		base, err := st.GetBaselineStateByLogicalID(liveLogicalID)
		if err != nil || base != nil {
			return base, err
		}
		byName, err := st.GetBaselineState(name)
		if err != nil {
			return nil, err
		}
		if byName != nil && byName.Desired.LogicalID != liveLogicalID {
			return nil, fmt.Errorf("live session %q has logical identity %s but its name is associated with baseline identity %s", name, liveLogicalID, byName.Desired.LogicalID)
		}
		return nil, nil
	}
	base, err := st.GetBaselineState(name)
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
	desired := core.DesiredState{Baseline: plan.DesiredBaseline, ClassifierVersion: plan.ClassifierVersion}
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
	updated, err := core.RefreshBindings(plan.DesiredBaseline, *fresh, resolution)
	if err != nil {
		_ = st.UpdateReconcile(journalID, model.JournalNeedsAttention, len(plan.Operations), "binding refresh failed: "+err.Error())
		return "", fmt.Errorf("reconcile verified but binding refresh failed: %w", err)
	}
	commitErr := st.CommitReconcileState(updated, journalID)
	if commitErr != nil {
		return "", fmt.Errorf("reconcile verified but baseline/journal commit failed: %w", commitErr)
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
	for _, baselineWindow := range plan.DesiredBaseline.Session().Windows {
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
			return "already aligned"
		}
		return fmt.Sprintf("already aligned; preserved %d extra window(s) and %d extra pane(s)%s", len(plan.PreservedWindows), len(plan.PreservedPanes), groupNote)
	}
	if plan.Policy == core.PolicyExact {
		return fmt.Sprintf("hard reconciled: rebuilt %d window(s); prior pane processes were terminated%s", len(plan.DesiredBaseline.Session().Windows), groupNote)
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
