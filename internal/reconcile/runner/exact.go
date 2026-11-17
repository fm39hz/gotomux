package runner

import (
	"context"
	"fmt"
	"sort"

	"github.com/fm39hz/gotomux/internal/model"
	core "github.com/fm39hz/gotomux/internal/reconcile/core"
	"github.com/fm39hz/gotomux/internal/tmuxlayout"
	"github.com/google/uuid"
)

// applyExactRebuild creates every desired window at a deterministic staging
// index before deleting originals. Operation markers make each phase resumable.
func (r Runner) applyExactRebuild(ctx context.Context, sessionName string, op core.Operation, initial model.Snapshot) error {
	if len(op.ExactOriginalWindows) == 0 || len(op.DesiredBaseline.Session().Windows) == 0 {
		return fmt.Errorf("exact rebuild plan lacks original or desired windows")
	}
	originals := make(map[string]model.Window, len(op.ExactOriginalWindows))
	for _, window := range op.ExactOriginalWindows {
		if window.TmuxID == "" {
			return fmt.Errorf("exact rebuild plan contains a window without runtime identity")
		}
		originals[window.TmuxID] = window
	}
	stageBase := maxWindowIndex(op.ExactOriginalWindows)
	if desiredMax := maxWindowIndex(op.DesiredBaseline.Session().Windows); desiredMax > stageBase {
		stageBase = desiredMax
	}
	stageBase++
	snapshot := initial
	if err := validateExactWindowSet(snapshot, op, originals); err != nil {
		return err
	}

	// Recreate first. This keeps at least one session window alive and ensures
	// retries can identify completed creations without guessing from names.
	for i, wanted := range op.DesiredBaseline.Session().Windows {
		snapshot, err := r.observeExact(ctx, sessionName, op)
		if err != nil {
			return err
		}
		opID := exactWindowOperationID(op, wanted.Idx)
		if runtimeID := targetWindowIDByOperation(snapshot, opID); runtimeID != "" {
			window, ok := targetWindowByRuntime(snapshot.Target.Session(), runtimeID)
			if _, original := originals[runtimeID]; original || !ok || (window.SlotID != "" && window.SlotID != stagedWindowSlot(op, wanted) && window.SlotID != wanted.SlotID) {
				return fmt.Errorf("exact operation marker %s is attached to an unexpected window", opID)
			}
			if window.SlotID != wanted.SlotID {
				if _, err := r.Tmux.CreateWindowWithOperationKeepingMarkers(ctx, snapshot.TargetSessionID, window.Idx, stageWindow(op, wanted), snapshot.Target.Cwd, opID); err != nil {
					return fmt.Errorf("resume staged window %s: %w", runtimeID, err)
				}
			}
			continue
		}
		index := stageBase + i
		if err := targetIndexAvailable(snapshot.Target.Session(), index); err != nil {
			return err
		}
		staged := stageWindow(op, wanted)
		if _, err := r.Tmux.CreateWindowWithOperationKeepingMarkers(ctx, snapshot.TargetSessionID, index, staged, snapshot.Target.Cwd, opID); err != nil {
			return fmt.Errorf("stage baseline window %d: %w", wanted.Idx, err)
		}
	}

	snapshot, err := r.observeExact(ctx, sessionName, op)
	if err != nil {
		return err
	}
	if err := validateExactWindowSet(snapshot, op, originals); err != nil {
		return err
	}
	for _, wanted := range op.DesiredBaseline.Session().Windows {
		runtimeID := targetWindowIDByOperation(snapshot, exactWindowOperationID(op, wanted.Idx))
		window, ok := targetWindowByRuntime(snapshot.Target.Session(), runtimeID)
		if runtimeID == "" || !ok || (window.SlotID != stagedWindowSlot(op, wanted) && window.SlotID != wanted.SlotID) {
			return fmt.Errorf("staged baseline slot %s is missing its operation marker", wanted.SlotID)
		}
		if err := validateExactReplacement(snapshot, op, wanted, window); err != nil {
			return err
		}
	}

	ordered := append([]model.Window(nil), op.ExactOriginalWindows...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Idx > ordered[j].Idx })
	for _, original := range ordered {
		snapshot, err = r.observeExact(ctx, sessionName, op)
		if err != nil {
			return err
		}
		current, ok := targetWindowByRuntime(snapshot.Target.Session(), original.TmuxID)
		if !ok {
			if objectByRuntime(snapshot.Objects, model.ObjectWindow, original.TmuxID) != nil {
				return fmt.Errorf("original window %s moved out of the target session during exact rebuild", original.TmuxID)
			}
			continue
		}
		if core.WindowFingerprint(current) != core.WindowFingerprint(original) {
			return fmt.Errorf("original window %s changed after exact plan creation", original.TmuxID)
		}
		if len(snapshot.Target.Windows) <= len(op.DesiredBaseline.Session().Windows) {
			return fmt.Errorf("refusing to remove original window %s before all replacements exist", original.TmuxID)
		}
		if err := r.Tmux.KillWindowAt(ctx, sessionName, original.TmuxID, current.Idx); err != nil {
			return fmt.Errorf("remove original window %s: %w", original.TmuxID, err)
		}
	}

	// Original slots are now free. Promote the operation-scoped temporary IDs
	// to the frozen slot IDs, retaining operation markers until final verify.
	for _, wanted := range op.DesiredBaseline.Session().Windows {
		snapshot, err = r.observeExact(ctx, sessionName, op)
		if err != nil {
			return err
		}
		windowID := targetWindowIDByOperation(snapshot, exactWindowOperationID(op, wanted.Idx))
		window, ok := targetWindowByRuntime(snapshot.Target.Session(), windowID)
		if !ok {
			return fmt.Errorf("staged window for baseline slot %s disappeared", wanted.SlotID)
		}
		if window.SlotID != wanted.SlotID {
			if window.SlotID != "" && window.SlotID != stagedWindowSlot(op, wanted) {
				return fmt.Errorf("staged window %s has an unexpected slot identity", windowID)
			}
			if window.SlotID != "" {
				if err := r.Tmux.UnsetStableID(ctx, model.ObjectWindow, windowID); err != nil {
					return fmt.Errorf("clear temporary window identity: %w", err)
				}
			}
			if err := r.Tmux.SetStableID(ctx, model.ObjectWindow, windowID, wanted.SlotID); err != nil {
				return fmt.Errorf("assign baseline window identity: %w", err)
			}
		}
		for paneIndex, wantedPane := range wanted.Panes {
			snapshot, err = r.observeExact(ctx, sessionName, op)
			if err != nil {
				return err
			}
			paneID := targetPaneIDByOperation(snapshot, exactPaneOperationID(exactWindowOperationID(op, wanted.Idx), paneIndex), windowID)
			if paneID == "" {
				return fmt.Errorf("staged pane for baseline slot %s disappeared", wantedPane.SlotID)
			}
			pane := objectByRuntime(snapshot.Objects, model.ObjectPane, paneID)
			if pane == nil {
				return fmt.Errorf("staged pane %s has no observed identity", paneID)
			}
			if pane.SlotID == wantedPane.SlotID {
				continue
			}
			if pane.SlotID != "" && pane.SlotID != stagedPaneSlot(op, wanted, paneIndex) {
				return fmt.Errorf("staged pane %s has an unexpected slot identity", paneID)
			}
			if pane.SlotID != "" {
				if err := r.Tmux.UnsetStableID(ctx, model.ObjectPane, paneID); err != nil {
					return fmt.Errorf("clear temporary pane identity: %w", err)
				}
			}
			if err := r.Tmux.SetStableID(ctx, model.ObjectPane, paneID, wantedPane.SlotID); err != nil {
				return fmt.Errorf("assign baseline pane identity: %w", err)
			}
		}
	}

	for _, wanted := range op.DesiredBaseline.Session().Windows {
		snapshot, err = r.observeExact(ctx, sessionName, op)
		if err != nil {
			return err
		}
		runtimeID := targetWindowIDBySlot(snapshot.Target.Session(), wanted.SlotID)
		window, ok := targetWindowByRuntime(snapshot.Target.Session(), runtimeID)
		if !ok || !operationMarkerForWindow(snapshot, runtimeID, exactWindowOperationID(op, wanted.Idx)) {
			return fmt.Errorf("staged baseline window %s disappeared before final placement", wanted.SlotID)
		}
		if window.Idx != wanted.Idx {
			if err := targetIndexAvailable(snapshot.Target.Session(), wanted.Idx); err != nil {
				return err
			}
			if err := r.Tmux.MoveWindowTo(ctx, snapshot.TargetSessionID, runtimeID, window.Idx, wanted.Idx); err != nil {
				return fmt.Errorf("place baseline window %d: %w", wanted.Idx, err)
			}
		}
	}

	snapshot, err = r.observeExact(ctx, sessionName, op)
	if err != nil {
		return err
	}
	return selectAndVerifyExact(ctx, r, sessionName, op, snapshot)
}

func selectAndVerifyExact(ctx context.Context, r Runner, sessionName string, op core.Operation, snapshot model.Snapshot) error {
	if slot := planFinalSlot(op.DesiredBaseline.Session()); slot != "" {
		windowID := targetWindowIDBySlot(snapshot.Target.Session(), slot)
		if windowID == "" {
			return fmt.Errorf("exact rebuild lost the final active baseline window")
		}
		if err := r.Tmux.SelectWindowByID(ctx, snapshot.TargetSessionID, windowID); err != nil {
			return fmt.Errorf("select first baseline window: %w", err)
		}
	}
	fresh, err := r.observeExact(ctx, sessionName, op)
	if err != nil {
		return err
	}
	resolution := core.Resolve(core.DesiredState{Baseline: op.DesiredBaseline}, fresh)
	if err := core.Verify(core.DesiredState{Baseline: op.DesiredBaseline}, fresh, resolution, core.PolicyExact, core.PlanResult{FinalActiveSlotID: planFinalSlot(op.DesiredBaseline.Session())}); err != nil {
		return fmt.Errorf("exact rebuild did not verify: %w", err)
	}
	return nil
}

func exactWindowOperationID(op core.Operation, index int) string {
	return fmt.Sprintf("%s/window/%d", op.ID, index)
}

func exactPaneOperationID(windowOperationID string, index int) string {
	return fmt.Sprintf("%s/pane/%d", windowOperationID, index)
}

func exactOperationMarkersValid(snapshot model.Snapshot, op core.Operation) bool {
	for _, window := range op.DesiredBaseline.Session().Windows {
		windowID := targetWindowIDBySlot(snapshot.Target.Session(), window.SlotID)
		windowOperationID := exactWindowOperationID(op, window.Idx)
		if windowID == "" {
			return false
		}
		windowObject := objectByRuntime(snapshot.Objects, model.ObjectWindow, windowID)
		if windowObject == nil || windowObject.OperationID != "" && windowObject.OperationID != windowOperationID {
			return false
		}
		liveWindow, _ := targetWindowByRuntime(snapshot.Target.Session(), windowID)
		for paneIndex, wantedPane := range window.Panes {
			var runtimeID string
			for _, pane := range liveWindow.Panes {
				if pane.SlotID == wantedPane.SlotID {
					runtimeID = pane.TmuxID
					break
				}
			}
			object := objectByRuntime(snapshot.Objects, model.ObjectPane, runtimeID)
			if object == nil || object.OperationID != "" && object.OperationID != exactPaneOperationID(windowOperationID, paneIndex) {
				return false
			}
		}
	}
	return true
}

func (r Runner) observeExact(ctx context.Context, sessionName string, op core.Operation) (model.Snapshot, error) {
	snapshot, err := r.Tmux.Observe(ctx, sessionName)
	if err != nil {
		return model.Snapshot{}, fmt.Errorf("observe exact rebuild: %w", err)
	}
	if !snapshot.Complete || snapshot.ServerKey != op.ExpectedServerKey || snapshot.TargetSessionID != op.ExpectedSessionID {
		return model.Snapshot{}, ErrStalePlan
	}
	if op.TargetIdentityID != "" && snapshot.Target.LogicalID != op.TargetIdentityID {
		return model.Snapshot{}, fmt.Errorf("exact rebuild target session identity changed")
	}
	return *snapshot, nil
}

func validateExactWindowSet(snapshot model.Snapshot, op core.Operation, originals map[string]model.Window) error {
	for _, window := range snapshot.Target.Windows {
		if _, ok := originals[window.TmuxID]; ok {
			continue
		}
		marked := false
		for _, wanted := range op.DesiredBaseline.Session().Windows {
			if operationMarkerForWindow(snapshot, window.TmuxID, exactWindowOperationID(op, wanted.Idx)) {
				if window.SlotID != "" && window.SlotID != wanted.SlotID && window.SlotID != stagedWindowSlot(op, wanted) {
					return fmt.Errorf("exact window operation marker is attached to an unexpected slot")
				}
				marked = true
				break
			}
		}
		if !marked {
			return fmt.Errorf("unexpected window %s appeared during exact rebuild; plan is stale", window.TmuxID)
		}
	}
	return nil
}

func stageWindow(op core.Operation, wanted model.Window) model.Window {
	staged := wanted
	staged.SlotID = stagedWindowSlot(op, wanted)
	staged.Panes = append([]model.Pane(nil), wanted.Panes...)
	for i := range staged.Panes {
		staged.Panes[i].SlotID = stagedPaneSlot(op, wanted, i)
	}
	return staged
}

func stagedWindowSlot(op core.Operation, wanted model.Window) string {
	return deterministicUUID(op.ID + "/stage-window/" + wanted.SlotID)
}

func stagedPaneSlot(op core.Operation, wanted model.Window, paneIndex int) string {
	return deterministicUUID(fmt.Sprintf("%s/stage-pane/%s/%d", op.ID, wanted.SlotID, paneIndex))
}

func deterministicUUID(seed string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(seed)).String()
}

func targetPaneIDByOperation(snapshot model.Snapshot, operationID, parentID string) string {
	for _, object := range snapshot.Objects {
		if object.Kind == model.ObjectPane && object.ParentID == parentID && object.OperationID == operationID {
			return object.RuntimeID
		}
	}
	return ""
}

func targetIndexAvailable(session model.Session, index int) error {
	for _, window := range session.Windows {
		if window.Idx == index {
			return fmt.Errorf("exact rebuild staging index %d is occupied by %s", index, window.TmuxID)
		}
	}
	return nil
}

func targetWindowIDBySlot(session model.Session, slotID string) string {
	for _, window := range session.Windows {
		if window.SlotID == slotID {
			return window.TmuxID
		}
	}
	return ""
}

func targetWindowByRuntime(session model.Session, id string) (model.Window, bool) {
	for _, window := range session.Windows {
		if window.TmuxID == id {
			return window, true
		}
	}
	return model.Window{}, false
}

func targetWindowIndex(session model.Session, id string) (int, bool) {
	window, ok := targetWindowByRuntime(session, id)
	return window.Idx, ok
}

func targetWindowIDByOperation(snapshot model.Snapshot, operationID string) string {
	for _, object := range snapshot.Objects {
		if object.Kind == model.ObjectWindow && object.OperationID == operationID {
			if _, ok := targetWindowByRuntime(snapshot.Target.Session(), object.RuntimeID); ok {
				return object.RuntimeID
			}
		}
	}
	return ""
}

func operationMarkerForWindow(snapshot model.Snapshot, runtimeID, operationID string) bool {
	object := objectByRuntime(snapshot.Objects, model.ObjectWindow, runtimeID)
	return object != nil && object.OperationID == operationID
}

func maxWindowIndex(windows []model.Window) int {
	max := -1
	for _, window := range windows {
		if window.Idx > max {
			max = window.Idx
		}
	}
	return max
}

func planFinalSlot(session model.Session) string {
	if len(session.Windows) == 0 {
		return ""
	}
	return session.Windows[0].SlotID
}

// validateExactReplacement is the destructive phase gate. A window marker
// alone cannot prove that all pane creation and layout steps have finished.
func validateExactReplacement(snapshot model.Snapshot, op core.Operation, wanted, window model.Window) error {
	if len(window.Panes) != len(wanted.Panes) {
		return fmt.Errorf("replacement window %s has incomplete pane set", window.TmuxID)
	}
	seen := map[string]bool{}
	windowOp := exactWindowOperationID(op, wanted.Idx)
	for index, pane := range wanted.Panes {
		runtimeID := targetPaneIDByOperation(snapshot, exactPaneOperationID(windowOp, index), window.TmuxID)
		livePane := paneByRuntime(window, runtimeID)
		if runtimeID == "" || seen[runtimeID] || livePane == nil || livePane.SlotID != pane.SlotID && livePane.SlotID != stagedPaneSlot(op, wanted, index) {
			return fmt.Errorf("replacement pane %s/%d lacks unique operation-owned identity", window.TmuxID, index)
		}
		seen[runtimeID] = true
	}
	if wanted.Layout != "" {
		equal, known := tmuxlayout.Compare(wanted.Layout, len(wanted.Panes), window.Layout, len(window.Panes))
		if !known || !equal {
			return fmt.Errorf("replacement window %s layout is incomplete", window.TmuxID)
		}
	}
	return nil
}
