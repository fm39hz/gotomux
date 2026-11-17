package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fm39hz/gotomux/internal/model"
	core "github.com/fm39hz/gotomux/internal/reconcile/core"
	"github.com/fm39hz/gotomux/internal/tmuxlayout"
)

type TmuxPort interface {
	Observe(ctx context.Context, targetName string) (*model.Snapshot, error)
	StabilizeWindowIndices(ctx context.Context, sessionID, operationID string) (func() error, error)
	MoveWindowTo(ctx context.Context, session, windowID string, from, to int) error
	LinkWindowTo(ctx context.Context, session, windowID string, index int) error
	CreateWindowWithOperation(ctx context.Context, session string, index int, window model.Window, sessionCwd, operationID string) (string, error)
	CreateWindowWithOperationKeepingMarkers(ctx context.Context, session string, index int, window model.Window, sessionCwd, operationID string) (string, error)
	AddPaneWithOperation(ctx context.Context, session, windowID string, expectedCount int, expectedPaneIDs, expectedPaneSlotIDs []string, pane model.Pane, operationID string) (string, error)
	ApplyLayoutAt(ctx context.Context, session, windowID string, index int, layout string) error
	SelectWindowByID(ctx context.Context, session, windowID string) error
	KillWindowAt(ctx context.Context, session, windowID string, index int) error
	ClearOperationID(ctx context.Context, kind model.ObjectKind, runtimeID string) error
	SetStableID(ctx context.Context, kind model.ObjectKind, runtimeID, stableID string) error
	UnsetStableID(ctx context.Context, kind model.ObjectKind, runtimeID string) error
}

type Journal interface {
	GetReconcile(id string) (*model.ReconcileJournal, error)
	UpdateReconcile(id string, status model.ReconcileJournalStatus, completedStep int, lastError string) error
}

type Runner struct {
	Tmux    TmuxPort
	Journal Journal
}

var ErrStalePlan = errors.New("reconcile plan is stale")

// Execute applies a persisted plan one operation at a time. The journal cursor
// advances only after observing the postcondition; if tmux succeeded but the
// journal write failed, the next call recognizes that operation and advances
// without repeating it.
func (r Runner) Execute(ctx context.Context, sessionName, journalID string, plan core.PlanResult) (retErr error) {
	if plan.PlanSchemaVersion != core.CurrentPlanSchemaVersion {
		return fmt.Errorf("reconcile runner refuses plan schema version %d; expected %d", plan.PlanSchemaVersion, core.CurrentPlanSchemaVersion)
	}
	if plan.Status == core.PlanNoop {
		if r.Journal == nil || journalID == "" {
			return fmt.Errorf("reconcile no-op requires a journal for verified baseline commit")
		}
		journal, err := r.Journal.GetReconcile(journalID)
		if err != nil {
			return err
		}
		if journal == nil {
			return fmt.Errorf("reconcile journal %q does not exist", journalID)
		}
		if journal.Status == model.JournalPlanned || journal.Status == model.JournalNeedsAttention {
			return r.Journal.UpdateReconcile(journalID, model.JournalRunning, 0, "")
		}
		if journal.Status == model.JournalRunning {
			return nil
		}
		return fmt.Errorf("no-op reconcile journal %q cannot run from status %s", journalID, journal.Status)
	}
	if plan.Status != core.PlanReady {
		return fmt.Errorf("reconcile runner refuses %s plan: %s", plan.Status, plan.BlockedReason)
	}
	if r.Tmux == nil || r.Journal == nil || sessionName == "" || journalID == "" {
		return fmt.Errorf("reconcile runner: missing tmux, journal, session, or operation ID")
	}
	journal, err := r.Journal.GetReconcile(journalID)
	if err != nil {
		return fmt.Errorf("load reconcile journal: %w", err)
	}
	if journal == nil {
		return fmt.Errorf("reconcile journal %q does not exist", journalID)
	}
	if journal.PlanJSON != "" {
		var persisted core.PlanResult
		if err := json.Unmarshal([]byte(journal.PlanJSON), &persisted); err != nil || persisted.ID != plan.ID || persisted.Policy != plan.Policy || persisted.PlanSchemaVersion != plan.PlanSchemaVersion {
			return fmt.Errorf("reconcile journal plan does not match the supplied plan")
		}
	}
	if journal.Status == model.JournalComplete {
		return nil
	}
	if journal.Status == model.JournalAborted {
		return fmt.Errorf("reconcile journal %q was aborted", journalID)
	}
	freshPlan := journal.Status == model.JournalPlanned && journal.CompletedStep == 0
	if journal.CompletedStep < 0 || journal.CompletedStep > len(plan.Operations) {
		return r.needsAttention(journalID, journal.CompletedStep, fmt.Errorf("journal cursor %d is outside plan length %d", journal.CompletedStep, len(plan.Operations)))
	}
	if journal.Status == model.JournalPlanned || journal.Status == model.JournalNeedsAttention {
		if err := r.Journal.UpdateReconcile(journalID, model.JournalRunning, journal.CompletedStep, ""); err != nil {
			return fmt.Errorf("resume reconcile journal: %w", err)
		}
	}
	snapshot, err := r.Tmux.Observe(ctx, sessionName)
	if err != nil {
		return r.needsAttention(journalID, journal.CompletedStep, fmt.Errorf("observe before execution: %w", err))
	}
	var pending *core.Operation
	if journal.CompletedStep < len(plan.Operations) {
		pending = &plan.Operations[journal.CompletedStep]
	}
	if err := sameTarget(plan, *snapshot, journal, pending); err != nil {
		if journal.CompletedStep == 0 {
			_ = r.Journal.UpdateReconcile(journalID, model.JournalAborted, 0, err.Error())
			return fmt.Errorf("%w: %v", ErrStalePlan, err)
		}
		return r.needsAttention(journalID, journal.CompletedStep, err)
	}
	if freshPlan && plan.StartSnapshotFingerprint != "" && core.SnapshotFingerprint(*snapshot) != plan.StartSnapshotFingerprint {
		err := fmt.Errorf("initial snapshot fingerprint changed")
		_ = r.Journal.UpdateReconcile(journalID, model.JournalAborted, 0, err.Error())
		return fmt.Errorf("%w: %v", ErrStalePlan, err)
	}
	if planNeedsStableIndices(plan) {
		restore, err := r.Tmux.StabilizeWindowIndices(ctx, snapshot.TargetSessionID, journalID)
		if err != nil {
			return r.needsAttention(journalID, journal.CompletedStep, fmt.Errorf("stabilize window indices: %w", err))
		}
		completed := journal.CompletedStep
		defer func() {
			if restoreErr := restore(); restoreErr != nil {
				_ = r.Journal.UpdateReconcile(journalID, model.JournalNeedsAttention, completed, "restore renumber-windows: "+restoreErr.Error())
				if retErr == nil {
					retErr = fmt.Errorf("restore renumber-windows: %w", restoreErr)
				} else {
					retErr = fmt.Errorf("%w (also failed to restore renumber-windows: %v)", retErr, restoreErr)
				}
			}
		}()
		defer func() { completed = journal.CompletedStep }()
	}

	for step := journal.CompletedStep; step < len(plan.Operations); step++ {
		op := plan.Operations[step]
		if op.Kind == core.OperationApplyLayout && len(op.Window.Panes) == 0 {
			op, err = hydrateLayoutOperation(plan, op)
			if err != nil {
				return r.needsAttention(journalID, step, err)
			}
		}
		snapshot, err = r.Tmux.Observe(ctx, sessionName)
		if err != nil {
			return r.needsAttention(journalID, step, fmt.Errorf("observe before operation %s: %w", op.ID, err))
		}
		if err := sameTarget(plan, *snapshot, journal, &op); err != nil {
			return r.needsAttention(journalID, step, err)
		}
		if operationSatisfied(op, *snapshot) {
			if err := r.Journal.UpdateReconcile(journalID, model.JournalRunning, step+1, ""); err != nil {
				return r.needsAttention(journalID, step, fmt.Errorf("advance journal after observed operation: %w", err))
			}
			journal.CompletedStep = step + 1
			continue
		}
		if err := checkPreconditions(op, *snapshot); err != nil {
			return r.needsAttention(journalID, step, fmt.Errorf("operation %s precondition: %w", op.ID, err))
		}
		if err := r.apply(ctx, sessionName, op, *snapshot); err != nil {
			return r.needsAttention(journalID, step, fmt.Errorf("operation %s: %w", op.ID, err))
		}
		after, err := r.Tmux.Observe(ctx, sessionName)
		if err != nil {
			return r.needsAttention(journalID, step, fmt.Errorf("observe after operation %s: %w", op.ID, err))
		}
		if err := sameTarget(plan, *after, journal, nil); err != nil {
			return r.needsAttention(journalID, step, err)
		}
		if !operationSatisfied(op, *after) {
			return r.needsAttention(journalID, step, fmt.Errorf("operation %s (%s) postcondition was not observed", op.ID, op.Kind))
		}
		if err := r.Journal.UpdateReconcile(journalID, model.JournalRunning, step+1, ""); err != nil {
			return r.needsAttention(journalID, step, fmt.Errorf("advance journal after operation %s: %w", op.ID, err))
		}
		journal.CompletedStep = step + 1
	}
	return nil
}

func planNeedsStableIndices(plan core.PlanResult) bool {
	for _, op := range plan.Operations {
		switch op.Kind {
		case core.OperationMoveWindow, core.OperationLinkWindow, core.OperationCreateWindow, core.OperationExactRebuild:
			return true
		}
	}
	return false
}

func (r Runner) apply(ctx context.Context, session string, op core.Operation, snapshot model.Snapshot) error {
	switch op.Kind {
	case core.OperationMoveWindow:
		return r.Tmux.MoveWindowTo(ctx, snapshot.TargetSessionID, op.Target.ID, op.FromIndex, op.ToIndex)
	case core.OperationLinkWindow:
		if member, ok := windowMembership(snapshot, snapshot.TargetSessionID, op.Target.ID); ok {
			return r.Tmux.MoveWindowTo(ctx, snapshot.TargetSessionID, op.Target.ID, member.Index, op.ToIndex)
		}
		return r.Tmux.LinkWindowTo(ctx, snapshot.TargetSessionID, op.Target.ID, op.ToIndex)
	case core.OperationCreateWindow:
		_, err := r.Tmux.CreateWindowWithOperation(ctx, snapshot.TargetSessionID, op.Index, op.Window, snapshot.Target.Cwd, op.ID)
		return err
	case core.OperationAddPane:
		_, err := r.Tmux.AddPaneWithOperation(ctx, snapshot.TargetSessionID, op.Target.ID, op.ExpectedPaneCount, op.ExpectedPaneIDs, op.ExpectedPaneSlotIDs, op.Pane, op.ID)
		return err
	case core.OperationApplyLayout:
		window, ok := targetWindow(snapshot, op.Target.ID)
		if !ok {
			return fmt.Errorf("layout target window %s is not in the observed session", op.Target.ID)
		}
		layout, err := layoutForLivePaneSlots(op.Window.Layout, op.Window.Panes, window.Panes)
		if err != nil {
			return err
		}
		return r.Tmux.ApplyLayoutAt(ctx, snapshot.TargetSessionID, op.Target.ID, op.Index, layout)
	case core.OperationSelectWindow:
		return r.Tmux.SelectWindowByID(ctx, snapshot.TargetSessionID, op.Target.ID)
	case core.OperationBindIdentity:
		return r.Tmux.SetStableID(ctx, op.Target.Kind, op.Target.ID, op.TargetIdentityID)
	case core.OperationExactRebuild:
		return r.applyExactRebuild(ctx, session, op, snapshot)
	default:
		return fmt.Errorf("unknown reconcile operation kind %q", op.Kind)
	}
}

func layoutForLivePaneSlots(layout string, desired, live []model.Pane) (string, error) {
	if !strings.HasPrefix(strings.TrimSpace(layout), "{") {
		return layout, nil
	}
	liveBySlot := make(map[string]model.Pane, len(live))
	for _, pane := range live {
		if pane.SlotID != "" {
			liveBySlot[pane.SlotID] = pane
		}
	}
	remap := make(map[int]string, len(desired))
	indexRemap := make(map[int]int, len(desired))
	for _, pane := range desired {
		livePane, ok := liveBySlot[pane.SlotID]
		if !ok || livePane.TmuxID == "" {
			return "", fmt.Errorf("layout pane slot %s has no live pane binding", pane.SlotID)
		}
		remap[pane.Idx] = livePane.TmuxID
		indexRemap[pane.Idx] = livePane.Idx
	}
	return tmuxlayout.RemapPaneBindingsByIndex(layout, remap, indexRemap)
}

// hydrateLayoutOperation upgrades persisted plans created before layout
// operations carried their desired pane-slot list. The target runtime window
// is resolved through the plan's identity lineage, never through its name or
// index.
func hydrateLayoutOperation(plan core.PlanResult, op core.Operation) (core.Operation, error) {
	var desired *model.Window
	for _, match := range plan.Resolution.Windows {
		if match.Match.LiveRuntimeID != op.Target.ID {
			continue
		}
		if match.Match.BaselineIndex < 0 || match.Match.BaselineIndex >= len(plan.DesiredBaseline.Session().Windows) {
			return op, fmt.Errorf("layout operation %s has an invalid baseline window mapping", op.ID)
		}
		if desired != nil {
			return op, fmt.Errorf("layout operation %s has ambiguous baseline window mapping", op.ID)
		}
		want := plan.DesiredBaseline.Session().Windows[match.Match.BaselineIndex]
		desired = &want
	}
	if desired == nil {
		return op, fmt.Errorf("layout operation %s has no identity-resolved desired window", op.ID)
	}
	if op.ExpectedPaneCount != 0 && len(desired.Panes) != op.ExpectedPaneCount {
		return op, fmt.Errorf("layout operation %s expects %d panes but its desired window has %d", op.ID, op.ExpectedPaneCount, len(desired.Panes))
	}
	if op.Window.Layout != "" {
		equal, known := tmuxlayout.Compare(op.Window.Layout, len(desired.Panes), desired.Layout, len(desired.Panes))
		if !known || !equal {
			return op, fmt.Errorf("layout operation %s no longer matches its desired baseline topology", op.ID)
		}
	}
	op.Window.Panes = append([]model.Pane(nil), desired.Panes...)
	return op, nil
}

func (r Runner) needsAttention(id string, step int, cause error) error {
	if err := r.Journal.UpdateReconcile(id, model.JournalNeedsAttention, step, cause.Error()); err != nil {
		return fmt.Errorf("%v (also failed to persist needs_attention: %w)", cause, err)
	}
	return cause
}

func sameTarget(plan core.PlanResult, snapshot model.Snapshot, journal *model.ReconcileJournal, pending *core.Operation) error {
	if !snapshot.Complete || snapshot.ServerKey == "" || snapshot.ServerKey != plan.Resolution.ServerKey {
		return fmt.Errorf("tmux server generation changed or observation is incomplete")
	}
	if snapshot.TargetSessionID != plan.Resolution.TargetSession.LiveRuntimeID {
		return fmt.Errorf("target session runtime identity changed")
	}
	if journal.SessionLogicalID != "" && snapshot.Target.LogicalID != journal.SessionLogicalID {
		mayBindSession := snapshot.Target.LogicalID == "" && pending != nil && pending.Kind == core.OperationBindIdentity && pending.Target.Kind == model.ObjectSession && pending.Target.ID == snapshot.TargetSessionID && pending.TargetIdentityID == journal.SessionLogicalID
		if !mayBindSession {
			return fmt.Errorf("target session logical identity changed")
		}
	}
	resolution := core.Resolve(core.DesiredState{Baseline: plan.DesiredBaseline, ClassifierVersion: plan.ClassifierVersion}, snapshot)
	if resolution.Status != core.ResolutionReady {
		if len(resolution.Conflicts) > 0 {
			return fmt.Errorf("identity resolution changed during execution: %s: %s", resolution.Status, resolution.Conflicts[0].Reason)
		}
		return fmt.Errorf("identity resolution changed during execution: %s", resolution.Status)
	}
	if plan.Policy == core.PolicyPreserve {
		if err := sameResolutionLineage(plan.Resolution, resolution); err != nil {
			return err
		}
	}
	return nil
}

func sameResolutionLineage(planned, current core.Resolution) error {
	currentWindows := make(map[int]core.WindowResolution, len(current.Windows))
	for _, window := range current.Windows {
		currentWindows[window.Match.BaselineIndex] = window
	}
	for _, expected := range planned.Windows {
		observed, ok := currentWindows[expected.Match.BaselineIndex]
		if !ok || observed.Match.LiveRuntimeID != expected.Match.LiveRuntimeID {
			return fmt.Errorf("resolved baseline window %d changed identity during execution", expected.Match.BaselineIndex)
		}
		currentPanes := make(map[int]core.ObjectMatch, len(observed.Panes.Matches))
		for _, pane := range observed.Panes.Matches {
			currentPanes[pane.BaselineIndex] = pane
		}
		for _, pane := range expected.Panes.Matches {
			if got, ok := currentPanes[pane.BaselineIndex]; !ok || got.LiveRuntimeID != pane.LiveRuntimeID {
				return fmt.Errorf("resolved baseline pane %d in window %d changed identity during execution", pane.BaselineIndex, expected.Match.BaselineIndex)
			}
		}
	}
	return nil
}

func checkPreconditions(op core.Operation, snapshot model.Snapshot) error {
	if op.ExpectedServerKey == "" || op.ExpectedServerKey != snapshot.ServerKey || op.ExpectedSessionID == "" || op.ExpectedSessionID != snapshot.TargetSessionID {
		return fmt.Errorf("server/session precondition changed")
	}
	if err := op.Target.Validate(); err != nil {
		return err
	}
	switch op.Kind {
	case core.OperationBindIdentity:
		current, err := stableIdentityAt(snapshot, op.Target.Kind, op.Target.ID)
		if err != nil {
			return err
		}
		if current != "" && current != op.TargetIdentityID {
			return fmt.Errorf("target already carries a different stable identity")
		}
		switch op.Target.Kind {
		case model.ObjectSession:
			if snapshot.TargetSessionID != op.Target.ID {
				return fmt.Errorf("session identity target changed")
			}
		case model.ObjectWindow:
			window, ok := targetWindow(snapshot, op.Target.ID)
			if op.ExpectedFingerprint != "" {
				if ok && core.WindowFingerprint(window) != op.ExpectedFingerprint {
					return fmt.Errorf("window changed before slot binding")
				}
				if !ok {
					object := objectByRuntime(snapshot.Objects, model.ObjectWindow, op.Target.ID)
					if object == nil || core.ObjectFingerprint(*object) != op.ExpectedFingerprint {
						return fmt.Errorf("external window changed before slot binding")
					}
				}
			} else if !ok {
				return fmt.Errorf("window identity target is no longer on the observed server")
			}
		case model.ObjectPane:
			object := objectByRuntime(snapshot.Objects, model.ObjectPane, op.Target.ID)
			if object == nil || op.ExpectedParentID != "" && object.ParentID != op.ExpectedParentID {
				return fmt.Errorf("pane identity target moved or disappeared")
			}
			window, inTarget := targetWindow(snapshot, object.ParentID)
			if inTarget {
				pane := paneByRuntime(window, op.Target.ID)
				if pane == nil || op.ExpectedFingerprint != "" && core.PaneFingerprint(*pane) != op.ExpectedFingerprint {
					return fmt.Errorf("pane changed before slot binding")
				}
			} else if op.ExpectedFingerprint == "" || core.ObjectFingerprint(*object) != op.ExpectedFingerprint {
				return fmt.Errorf("external pane changed before slot binding")
			}
		default:
			return fmt.Errorf("unsupported identity binding kind %q", op.Target.Kind)
		}
	case core.OperationMoveWindow:
		window, ok := targetWindow(snapshot, op.Target.ID)
		if !ok || window.Idx != op.FromIndex {
			return fmt.Errorf("source window %s is not at index %d", op.Target.ID, op.FromIndex)
		}
		if op.ExpectedFingerprint != "" && core.WindowFingerprint(window) != op.ExpectedFingerprint {
			return fmt.Errorf("source window fingerprint changed")
		}
		if targetIndexOccupied(snapshot.Target.Windows, op.ToIndex, op.Target.ID) {
			return fmt.Errorf("destination index %d is occupied", op.ToIndex)
		}
	case core.OperationLinkWindow:
		object := objectByRuntime(snapshot.Objects, model.ObjectWindow, op.Target.ID)
		if object == nil || (op.TargetIdentityID != "" && object.SlotID != op.TargetIdentityID) {
			return fmt.Errorf("source window identity is no longer present")
		}
		if op.ExpectedFingerprint != "" && core.ObjectFingerprint(*object) != op.ExpectedFingerprint {
			return fmt.Errorf("source window identity fingerprint changed")
		}
		if targetIndexOccupied(snapshot.Target.Windows, op.ToIndex, "") {
			return fmt.Errorf("link destination index %d is occupied", op.ToIndex)
		}
	case core.OperationCreateWindow:
		if targetIndexOccupied(snapshot.Target.Windows, op.Index, "") && !operationMarkerPresent(snapshot, model.ObjectWindow, op.ID, "") {
			return fmt.Errorf("create destination index %d is occupied", op.Index)
		}
	case core.OperationAddPane, core.OperationApplyLayout:
		window, ok := targetWindow(snapshot, op.Target.ID)
		if !ok {
			return fmt.Errorf("target window %s is no longer in session", op.Target.ID)
		}
		if op.TargetIdentityID != "" && window.SlotID != op.TargetIdentityID {
			return fmt.Errorf("target window slot ID changed")
		}
		if op.Kind == core.OperationAddPane && !hasExpectedPanes(window, op) {
			return fmt.Errorf("one or more expected panes disappeared")
		}
		recoverablePane := op.Kind == core.OperationAddPane && len(window.Panes) == op.ExpectedPaneCount+1
		if op.ExpectedPaneCount > 0 && len(window.Panes) != op.ExpectedPaneCount && !recoverablePane && !operationMarkerPresent(snapshot, model.ObjectPane, op.ID, op.Target.ID) {
			return fmt.Errorf("target pane count changed from %d to %d", op.ExpectedPaneCount, len(window.Panes))
		}
		if op.ExpectedFingerprint != "" && core.WindowFingerprint(window) != op.ExpectedFingerprint {
			return fmt.Errorf("target window fingerprint changed")
		}
	case core.OperationSelectWindow:
		if _, ok := targetWindow(snapshot, op.Target.ID); !ok {
			return fmt.Errorf("active window target disappeared")
		}
	}
	return nil
}

func operationSatisfied(op core.Operation, snapshot model.Snapshot) bool {
	switch op.Kind {
	case core.OperationBindIdentity:
		current, err := stableIdentityAt(snapshot, op.Target.Kind, op.Target.ID)
		return err == nil && current == op.TargetIdentityID
	case core.OperationMoveWindow:
		member, ok := windowMembership(snapshot, snapshot.TargetSessionID, op.Target.ID)
		return ok && member.Index == op.ToIndex
	case core.OperationLinkWindow:
		member, ok := windowMembership(snapshot, snapshot.TargetSessionID, op.Target.ID)
		return ok && member.Index == op.ToIndex
	case core.OperationCreateWindow:
		window := createdWindow(snapshot, op)
		return window != nil && createdWindowMatches(*window, op.Window, op.Index)
	case core.OperationAddPane:
		window, ok := targetWindow(snapshot, op.Target.ID)
		if !ok {
			return false
		}
		for _, pane := range window.Panes {
			if op.Pane.SlotID != "" && pane.SlotID == op.Pane.SlotID {
				return true
			}
		}
		return false
	case core.OperationApplyLayout:
		window, ok := targetWindow(snapshot, op.Target.ID)
		if !ok {
			return false
		}
		equal, known := tmuxlayout.Compare(op.Window.Layout, len(window.Panes), window.Layout, len(window.Panes))
		if !known || !equal {
			return false
		}
		expectedLayout, err := layoutForLivePaneSlots(op.Window.Layout, op.Window.Panes, window.Panes)
		if err != nil {
			return false
		}
		expectedPaneIDs, err := tmuxlayout.PaneIDs(expectedLayout)
		if err != nil {
			return false
		}
		actualPaneIDs, err := tmuxlayout.PaneIDs(window.Layout)
		if err != nil {
			return false
		}
		return len(expectedPaneIDs) == 0 || strings.Join(expectedPaneIDs, ",") == strings.Join(actualPaneIDs, ",")
	case core.OperationSelectWindow:
		return snapshot.ActiveWindowID == op.Target.ID
	case core.OperationExactRebuild:
		resolution := core.Resolve(core.DesiredState{Baseline: op.DesiredBaseline}, snapshot)
		if resolution.Status != core.ResolutionReady || core.Plan(core.DesiredState{Baseline: op.DesiredBaseline}, snapshot, resolution, core.PolicyExact).Status != core.PlanNoop {
			return false
		}
		if !exactOperationMarkersValid(snapshot, op) {
			return false
		}
		return true
	default:
		return false
	}
}

func createdWindow(snapshot model.Snapshot, op core.Operation) *model.Window {
	for i := range snapshot.Target.Windows {
		window := &snapshot.Target.Windows[i]
		if op.Window.SlotID != "" && window.SlotID == op.Window.SlotID {
			return window
		}
		if op.ID != "" {
			if object := objectByRuntime(snapshot.Objects, model.ObjectWindow, window.TmuxID); object != nil && object.OperationID == op.ID {
				return window
			}
		}
	}
	return nil
}

func createdWindowMatches(got, want model.Window, index int) bool {
	if got.Idx != index || len(got.Panes) != len(want.Panes) {
		return false
	}
	for _, pane := range want.Panes {
		if pane.SlotID == "" {
			continue
		}
		found := false
		for _, livePane := range got.Panes {
			if livePane.SlotID == pane.SlotID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if want.Layout != "" {
		equal, known := tmuxlayout.Compare(want.Layout, len(want.Panes), got.Layout, len(got.Panes))
		if !known || !equal {
			return false
		}
	}
	return true
}

func operationMarkerPresent(snapshot model.Snapshot, kind model.ObjectKind, operationID, parentID string) bool {
	for _, object := range snapshot.Objects {
		if object.Kind == kind && object.OperationID == operationID && (parentID == "" || object.ParentID == parentID) {
			return true
		}
	}
	return false
}

func stableIdentityAt(snapshot model.Snapshot, kind model.ObjectKind, runtimeID string) (string, error) {
	object := objectByRuntime(snapshot.Objects, kind, runtimeID)
	if object == nil {
		return "", fmt.Errorf("%s identity target %s is absent", kind, runtimeID)
	}
	if kind == model.ObjectSession {
		return object.LogicalID, nil
	}
	return object.SlotID, nil
}

func paneByRuntime(window model.Window, runtimeID string) *model.Pane {
	for i := range window.Panes {
		if window.Panes[i].TmuxID == runtimeID {
			return &window.Panes[i]
		}
	}
	return nil
}

func targetWindow(snapshot model.Snapshot, id string) (model.Window, bool) {
	for _, window := range snapshot.Target.Windows {
		if window.TmuxID == id {
			return window, true
		}
	}
	return model.Window{}, false
}

func windowMembership(snapshot model.Snapshot, sessionID, windowID string) (model.WindowMembership, bool) {
	for _, membership := range snapshot.Memberships {
		if membership.SessionID == sessionID && membership.WindowID == windowID {
			return membership, true
		}
	}
	return model.WindowMembership{}, false
}

func targetIndexOccupied(windows []model.Window, index int, exceptID string) bool {
	for _, window := range windows {
		if window.Idx == index && window.TmuxID != exceptID {
			return true
		}
	}
	return false
}

func objectByRuntime(objects []model.ObjectIdentity, kind model.ObjectKind, id string) *model.ObjectIdentity {
	for i := range objects {
		if objects[i].Kind == kind && objects[i].RuntimeID == id {
			return &objects[i]
		}
	}
	return nil
}

func hasExpectedPanes(window model.Window, operation core.Operation) bool {
	for _, id := range operation.ExpectedPaneIDs {
		found := false
		for _, pane := range window.Panes {
			if pane.TmuxID == id {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, logicalID := range operation.ExpectedPaneSlotIDs {
		found := false
		for _, pane := range window.Panes {
			if pane.SlotID == logicalID {
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
