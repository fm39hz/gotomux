package reconcile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/fm39hz/gotomux/internal/identity"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/tmuxlayout"
)

// Plan converts a resolved identity graph into deterministic operations. It
// never repairs an ambiguous/conflicting mapping and never emits a destructive
// operation under the preserve policy.
func Plan(desired DesiredState, live model.Snapshot, resolution Resolution, policy Policy) PlanResult {
	result := PlanResult{Status: PlanBlocked, Policy: policy, DesiredSession: desired.Session, ClassifierVersion: desired.ClassifierVersion, Resolution: resolution}
	if err := policy.Validate(); err != nil {
		result.BlockedReason = err.Error()
		return result
	}
	if resolution.Status != ResolutionReady {
		result.BlockedReason = "identity resolution is " + string(resolution.Status)
		return result
	}
	if !live.Complete || live.ServerKey == "" || resolution.ServerKey != live.ServerKey || live.TargetSessionID == "" || resolution.TargetSession.LiveRuntimeID != live.TargetSessionID {
		result.BlockedReason = "resolution and live snapshot do not describe the same complete session generation"
		return result
	}
	base := desired.Session
	if policy == PolicyPreserve {
		result.FinalActiveWindowID = live.ActiveWindowID
	}
	if err := validateDesiredIdentities(base); err != nil {
		result.BlockedReason = err.Error()
		return result
	}
	if len(resolution.Windows)+len(resolution.Missing) != len(base.Windows) {
		result.BlockedReason = "resolution does not cover every baseline window exactly once"
		return result
	}
	if policy == PolicyExact {
		for _, match := range resolution.Windows {
			if match.Match.OutsideTarget {
				result.BlockedReason = fmt.Sprintf("exact reconcile cannot replace baseline window %s while it belongs to another session", match.Match.LiveRuntimeID)
				return result
			}
		}
		for _, window := range live.Target.Windows {
			for _, member := range live.Memberships {
				if member.WindowID == window.TmuxID && member.SessionID != live.TargetSessionID {
					result.BlockedReason = fmt.Sprintf("exact reconcile cannot remove linked window %s without affecting another session", window.TmuxID)
					return result
				}
			}
		}
		if len(base.Windows) > 0 {
			result.FinalActiveSlotID = base.Windows[0].SlotID
		}
		if exactStateMatches(desired, live, resolution) {
			result.Operations = bindingOperations(desired, live, resolution, true, true)
			if len(result.Operations) == 0 {
				result.Status = PlanNoop
				result.ID = planID(desired, live, policy, nil)
				return result
			}
			result.Status = PlanReady
			result.StartSnapshotFingerprint = snapshotFingerprint(live)
			result.finish(desired, live)
			return result
		}
		operation := Operation{
			Kind:              OperationExactRebuild,
			Target:            RuntimeRef{ServerGeneration: live.ServerKey, Kind: ObjectSession, ID: live.TargetSessionID},
			TargetIdentityID:  resolution.TargetSession.LiveIdentityID,
			ExpectedServerKey: live.ServerKey, ExpectedSessionID: live.TargetSessionID,
			ExpectedFingerprint: snapshotFingerprint(live),
			Destructive:         true, DesiredSession: base,
			ExactOriginalWindows: append([]model.Window(nil), live.Target.Windows...),
			Reason:               "restore the complete frozen baseline and terminate replaced pane processes",
		}
		result.Operations = append(bindingOperations(desired, live, resolution, true, false), operation)
		result.Status = PlanReady
		result.StartSnapshotFingerprint = snapshotFingerprint(live)
		result.finish(desired, live)
		return result
	}

	if err := validateDesiredIndices(base); err != nil {
		result.BlockedReason = err.Error()
		return result
	}
	baseMax, liveMax := maxIndex(base.Windows), maxIndex(live.Target.Windows)
	matchedByBase := make(map[int]WindowResolution, len(resolution.Windows))
	liveByID := make(map[string]model.Window, len(live.Target.Windows))
	for _, window := range live.Target.Windows {
		liveByID[window.TmuxID] = window
	}
	usedBaseline := map[int]bool{}
	for _, match := range resolution.Windows {
		if match.Match.BaselineIndex < 0 || match.Match.BaselineIndex >= len(base.Windows) || usedBaseline[match.Match.BaselineIndex] {
			result.BlockedReason = "resolution contains an invalid or duplicate baseline window match"
			return result
		}
		usedBaseline[match.Match.BaselineIndex] = true
		matchedByBase[match.Match.BaselineIndex] = match
	}
	for _, index := range resolution.Missing {
		if index < 0 || index >= len(base.Windows) || usedBaseline[index] {
			result.BlockedReason = "resolution contains an invalid or duplicate missing baseline index"
			return result
		}
		usedBaseline[index] = true
	}
	for _, id := range resolution.Extra {
		if _, ok := liveByID[id]; !ok {
			result.BlockedReason = "resolution references an extra window missing from the live snapshot"
			return result
		}
	}
	for i := range base.Windows {
		if !usedBaseline[i] {
			result.BlockedReason = fmt.Sprintf("resolution omits baseline window %d", i)
			return result
		}
	}

	result.PreservedWindows = append(result.PreservedWindows, resolution.Extra...)
	for _, match := range resolution.Windows {
		result.PreservedPanes = append(result.PreservedPanes, match.Panes.Extra...)
	}
	sort.Strings(result.PreservedWindows)
	sort.Strings(result.PreservedPanes)

	tail := maxInt(baseMax, liveMax) + 1
	tempStart := tail + len(live.Target.Windows) + len(base.Windows) + 1
	staged := make(map[string]int)
	extraStaged := make(map[string]bool)
	nextTemp := tempStart
	for _, match := range resolution.Windows {
		if match.Match.OutsideTarget {
			continue
		}
		window, ok := liveByID[match.Match.LiveRuntimeID]
		if !ok {
			result.BlockedReason = "matched target window is absent from the detailed live snapshot"
			return result
		}
		want := base.Windows[match.Match.BaselineIndex]
		if window.Idx == want.Idx {
			continue
		}
		staged[window.TmuxID] = nextTemp
		result.Operations = append(result.Operations, moveOperation(live, window, window.Idx, nextTemp, true, "vacate a baseline destination before placement"))
		nextTemp++
	}
	for _, extraID := range resolution.Extra {
		window, ok := liveByID[extraID]
		if !ok || window.Idx > baseMax {
			continue
		}
		staged[extraID] = nextTemp
		extraStaged[extraID] = true
		result.Operations = append(result.Operations, moveOperation(live, window, window.Idx, nextTemp, true, "preserve an extra window outside baseline destination indices"))
		nextTemp++
	}

	for _, match := range resolution.Windows {
		want := base.Windows[match.Match.BaselineIndex]
		target := RuntimeRef{ServerGeneration: live.ServerKey, Kind: ObjectWindow, ID: match.Match.LiveRuntimeID}
		if match.Match.OutsideTarget {
			if len(match.Panes.Missing) > 0 || len(match.Panes.Extra) > 0 || !windowLayoutMatches(want, live.Objects, target.ID) {
				result.BlockedReason = fmt.Sprintf("preserve reconcile cannot repair shared window %s without changing its other session", target.ID)
				return result
			}
			result.Operations = append(result.Operations, Operation{
				Kind: OperationLinkWindow, Target: target, ExpectedServerKey: live.ServerKey, ExpectedSessionID: live.TargetSessionID,
				TargetIdentityID:    match.Match.LiveIdentityID,
				ExpectedFingerprint: objectFingerprint(objectByRuntime(live.Objects, ObjectWindow, target.ID)),
				ToIndex:             want.Idx, Index: want.Idx, Reason: "restore target-session membership for the identified tmux window",
			})
			continue
		}
		window := liveByID[target.ID]
		currentIndex := window.Idx
		if temp, ok := staged[target.ID]; ok {
			currentIndex = temp
		}
		if currentIndex != want.Idx {
			copy := window
			copy.Idx = currentIndex
			result.Operations = append(result.Operations, moveOperation(live, copy, currentIndex, want.Idx, false, "place the identified window at its baseline index"))
		}
		if len(window.Panes) < len(want.Panes) {
			missing := append([]int(nil), match.Panes.Missing...)
			sort.Ints(missing)
			expectedPaneCount := len(window.Panes)
			expectedPaneIDs := make([]string, 0, len(window.Panes))
			expectedPaneSlotIDs := make([]string, 0, len(window.Panes)+len(missing))
			for _, pane := range window.Panes {
				expectedPaneIDs = append(expectedPaneIDs, pane.TmuxID)
				if pane.SlotID != "" {
					expectedPaneSlotIDs = append(expectedPaneSlotIDs, pane.SlotID)
				}
			}
			for _, paneIndex := range missing {
				if paneIndex < 0 || paneIndex >= len(want.Panes) {
					result.BlockedReason = "resolution contains an invalid missing pane index"
					return result
				}
				pane := want.Panes[paneIndex]
				expectedFingerprint := ""
				if expectedPaneCount == len(window.Panes) {
					expectedFingerprint = windowFingerprint(window)
				}
				result.Operations = append(result.Operations, Operation{
					Kind: OperationAddPane, Target: target, ExpectedServerKey: live.ServerKey, ExpectedSessionID: live.TargetSessionID,
					TargetIdentityID: match.Match.LiveIdentityID, ExpectedFingerprint: expectedFingerprint, ExpectedPaneCount: expectedPaneCount, Index: paneIndex,
					ExpectedPaneIDs: append([]string(nil), expectedPaneIDs...), ExpectedPaneSlotIDs: append([]string(nil), expectedPaneSlotIDs...),
					Pane: pane, Reason: "restore a missing baseline pane while preserving surviving pane processes",
				})
				expectedPaneCount++
				if pane.SlotID != "" {
					expectedPaneSlotIDs = append(expectedPaneSlotIDs, pane.SlotID)
				}
			}
		}
		result.Operations = appendLayoutOperation(result.Operations, desired, live, match, window, target)
	}

	for _, missingIndex := range resolution.Missing {
		want := base.Windows[missingIndex]
		result.Operations = append(result.Operations, Operation{
			Kind: OperationCreateWindow, Target: RuntimeRef{ServerGeneration: live.ServerKey, Kind: ObjectSession, ID: live.TargetSessionID}, TargetIdentityID: resolution.TargetSession.LiveIdentityID,
			ExpectedServerKey: live.ServerKey, ExpectedSessionID: live.TargetSessionID,
			Index: want.Idx, ToIndex: want.Idx,
			Window: want, Reason: "create a missing baseline window",
		})
	}
	extraIDs := make([]string, 0, len(extraStaged))
	for extraID := range extraStaged {
		extraIDs = append(extraIDs, extraID)
	}
	sort.Strings(extraIDs)
	for _, extraID := range extraIDs {
		window := liveByID[extraID]
		currentIndex := staged[extraID]
		copy := window
		copy.Idx = currentIndex
		result.Operations = append(result.Operations, moveOperation(live, copy, currentIndex, tail, false, "move a preserved extra window to the tail"))
		tail++
	}
	if active := live.ActiveWindowID; active != "" {
		if current, ok := liveByID[active]; ok {
			desiredIndex := current.Idx
			for _, match := range resolution.Windows {
				if match.Match.LiveRuntimeID == active && !match.Match.OutsideTarget {
					desiredIndex = base.Windows[match.Match.BaselineIndex].Idx
					break
				}
			}
			if extraStaged[active] {
				for _, op := range result.Operations {
					if op.Kind == OperationMoveWindow && op.Target.ID == active && !op.Temporary {
						desiredIndex = op.ToIndex
						break
					}
				}
			}
			if desiredIndex != current.Idx {
				target := RuntimeRef{ServerGeneration: live.ServerKey, Kind: ObjectWindow, ID: active}
				copy := current
				copy.Idx = desiredIndex
				result.Operations = append(result.Operations, Operation{Kind: OperationSelectWindow, Target: target, TargetIdentityID: current.SlotID, ExpectedServerKey: live.ServerKey, ExpectedSessionID: live.TargetSessionID, ExpectedFingerprint: windowFingerprint(copy), Index: desiredIndex, Reason: "preserve the user's active window"})
			}
		}
	}
	slotBindings := bindingOperations(desired, live, resolution, false, true)
	sessionBindings := bindingOperations(desired, live, resolution, true, false)
	topologyOperations := result.Operations
	result.Operations = append(append(sessionBindings, slotBindings...), topologyOperations...)
	if len(result.Operations) == 0 {
		result.Status = PlanNoop
		result.ID = planID(desired, live, policy, nil)
		return result
	}
	result.Status = PlanReady
	result.StartSnapshotFingerprint = snapshotFingerprint(live)
	result.finish(desired, live)
	return result
}

func bindingOperations(desired DesiredState, live model.Snapshot, resolution Resolution, includeSession, includeSlots bool) []Operation {
	var operations []Operation
	if includeSession && live.Target.LogicalID == "" {
		operations = append(operations, Operation{
			Kind: OperationBindIdentity, Target: RuntimeRef{ServerGeneration: live.ServerKey, Kind: ObjectSession, ID: live.TargetSessionID},
			TargetIdentityID: desired.Session.LogicalID, ExpectedServerKey: live.ServerKey, ExpectedSessionID: live.TargetSessionID,
			Reason: "bind the verified live session to its baseline identity",
		})
	}
	if !includeSlots {
		return operations
	}
	for _, windowMatch := range resolution.Windows {
		baselineWindow := desired.Session.Windows[windowMatch.Match.BaselineIndex]
		if windowMatch.Match.LiveIdentityID == "" {
			target := RuntimeRef{ServerGeneration: live.ServerKey, Kind: ObjectWindow, ID: windowMatch.Match.LiveRuntimeID}
			fingerprint := objectFingerprint(objectByRuntime(live.Objects, ObjectWindow, windowMatch.Match.LiveRuntimeID))
			if window, ok := targetWindowByID(live.Target, windowMatch.Match.LiveRuntimeID); ok {
				fingerprint = windowFingerprint(window)
			}
			operations = append(operations, Operation{
				Kind: OperationBindIdentity, Target: target, TargetIdentityID: baselineWindow.SlotID,
				ExpectedServerKey: live.ServerKey, ExpectedSessionID: live.TargetSessionID, ExpectedFingerprint: fingerprint,
				Reason: "persist the resolved baseline window slot before topology operations",
			})
		}
		for _, paneMatch := range windowMatch.Panes.Matches {
			baselinePane := baselineWindow.Panes[paneMatch.BaselineIndex]
			if paneMatch.LiveIdentityID != "" {
				continue
			}
			parentID := windowMatch.Match.LiveRuntimeID
			if object := objectByRuntime(live.Objects, ObjectPane, paneMatch.LiveRuntimeID); object != nil {
				parentID = object.ParentID
			}
			fingerprint := objectFingerprint(objectByRuntime(live.Objects, ObjectPane, paneMatch.LiveRuntimeID))
			if window, ok := targetWindowByID(live.Target, parentID); ok {
				if pane := paneByRuntimeID(window, paneMatch.LiveRuntimeID); pane != nil {
					fingerprint = paneFingerprint(*pane)
				}
			}
			operations = append(operations, Operation{
				Kind: OperationBindIdentity, Target: RuntimeRef{ServerGeneration: live.ServerKey, Kind: ObjectPane, ID: paneMatch.LiveRuntimeID},
				TargetIdentityID: baselinePane.SlotID, ExpectedServerKey: live.ServerKey, ExpectedSessionID: live.TargetSessionID,
				ExpectedParentID: parentID, ExpectedFingerprint: fingerprint,
				Reason: "persist the resolved baseline pane slot before topology operations",
			})
		}
	}
	return operations
}

func appendLayoutOperation(operations []Operation, desired DesiredState, live model.Snapshot, match WindowResolution, window model.Window, target RuntimeRef) []Operation {
	want := desired.Session.Windows[match.Match.BaselineIndex]
	if len(window.Panes)+len(match.Panes.Missing) < len(want.Panes) || len(window.Panes) > len(want.Panes) || want.Layout == "" {
		return operations
	}
	equal, known := tmuxlayout.Compare(want.Layout, len(want.Panes), window.Layout, len(window.Panes))
	if known && equal {
		return operations
	}
	operations = append(operations, Operation{
		Kind: OperationApplyLayout, Target: target, TargetIdentityID: match.Match.LiveIdentityID, ExpectedServerKey: live.ServerKey, ExpectedSessionID: live.TargetSessionID,
		ExpectedPaneCount: len(want.Panes), Window: model.Window{Layout: want.Layout},
		Reason: "restore baseline split topology without replacing surviving pane processes",
	})
	return operations
}

func exactStateMatches(desired DesiredState, live model.Snapshot, resolution Resolution) bool {
	base := desired.Session
	if len(resolution.Missing) != 0 || len(resolution.Extra) != 0 || len(resolution.Windows) != len(base.Windows) {
		return false
	}
	for _, match := range resolution.Windows {
		if match.Match.OutsideTarget || match.Match.BaselineIndex < 0 || match.Match.BaselineIndex >= len(base.Windows) {
			return false
		}
		want := base.Windows[match.Match.BaselineIndex]
		got, ok := targetWindowByID(live.Target, match.Match.LiveRuntimeID)
		if !ok || got.Idx != want.Idx || len(match.Panes.Missing) != 0 || len(match.Panes.Extra) != 0 || len(got.Panes) != len(want.Panes) {
			return false
		}
		wantRole, gotRole := semanticWindowRole(base, want), semanticWindowRole(live.Target, got)
		if wantRole != "" && gotRole != "" && wantRole != gotRole {
			return false
		}
		if want.Layout != "" {
			equal, known := tmuxlayout.Compare(want.Layout, len(want.Panes), got.Layout, len(got.Panes))
			if !known || !equal {
				return false
			}
		}
	}
	return true
}

func validateDesiredIndices(session model.Session) error {
	seen := map[int]bool{}
	for _, window := range session.Windows {
		if window.Idx < 0 || seen[window.Idx] {
			return fmt.Errorf("baseline has invalid or duplicate window index %d", window.Idx)
		}
		seen[window.Idx] = true
		if len(window.Panes) == 0 {
			return fmt.Errorf("baseline window %d has no panes", window.Idx)
		}
	}
	return nil
}

func validateDesiredIdentities(session model.Session) error {
	seen := map[string]string{}
	add := func(kind, runtimeID, stableID string) error {
		if stableID == "" {
			return fmt.Errorf("desired %s at %s has no identity; freeze the intended session again before reconciling", kind, runtimeID)
		}
		if err := identity.Validate(stableID); err != nil {
			return fmt.Errorf("desired %s at %s has malformed stable ID: %w", kind, runtimeID, err)
		}
		if prior, exists := seen[stableID]; exists {
			return fmt.Errorf("desired stable ID %s is duplicated by %s and %s", stableID, prior, kind+" "+runtimeID)
		}
		seen[stableID] = kind + " " + runtimeID
		return nil
	}
	if err := add("session", session.TmuxID, session.LogicalID); err != nil {
		return err
	}
	for _, window := range session.Windows {
		if err := add("window", window.TmuxID, window.SlotID); err != nil {
			return err
		}
		for _, pane := range window.Panes {
			if err := add("pane", pane.TmuxID, pane.SlotID); err != nil {
				return err
			}
		}
	}
	return nil
}

func moveOperation(live model.Snapshot, window model.Window, from, to int, temporary bool, reason string) Operation {
	return Operation{
		Kind:              OperationMoveWindow,
		Target:            RuntimeRef{ServerGeneration: live.ServerKey, Kind: ObjectWindow, ID: window.TmuxID},
		TargetIdentityID:  window.SlotID,
		ExpectedServerKey: live.ServerKey, ExpectedSessionID: live.TargetSessionID,
		ExpectedFingerprint: windowFingerprint(window), FromIndex: from, ToIndex: to, Index: to, Temporary: temporary, Reason: reason,
	}
}

func windowFingerprint(window model.Window) string {
	copy := window
	copy.SlotID = ""
	copy.Panes = append([]model.Pane(nil), window.Panes...)
	for i := range copy.Panes {
		copy.Panes[i].CmdPath = ""
		copy.Panes[i].SlotID = ""
	}
	return digest(copy)
}

func paneFingerprint(pane model.Pane) string {
	copy := pane
	copy.CmdPath = ""
	copy.SlotID = ""
	return digest(copy)
}

func objectFingerprint(object *model.ObjectIdentity) string {
	copy := *object
	copy.LogicalID = ""
	copy.SlotID = ""
	return digest(copy)
}

func snapshotFingerprint(snapshot model.Snapshot) string {
	copy := snapshot
	copy.Target.LogicalID = ""
	copy.Target.Windows = append([]model.Window(nil), snapshot.Target.Windows...)
	copy.Sessions = append([]model.SessionIdentity(nil), snapshot.Sessions...)
	copy.Objects = append([]model.ObjectIdentity(nil), snapshot.Objects...)
	for i := range copy.Target.Windows {
		copy.Target.Windows[i].SlotID = ""
		copy.Target.Windows[i].Panes = append([]model.Pane(nil), snapshot.Target.Windows[i].Panes...)
		for j := range copy.Target.Windows[i].Panes {
			copy.Target.Windows[i].Panes[j].CmdPath = ""
			copy.Target.Windows[i].Panes[j].SlotID = ""
		}
	}
	for i := range copy.Sessions {
		copy.Sessions[i].LogicalID = ""
	}
	for i := range copy.Objects {
		copy.Objects[i].LogicalID = ""
		copy.Objects[i].SlotID = ""
	}
	return digest(copy)
}

func WindowFingerprint(window model.Window) string { return windowFingerprint(window) }

func PaneFingerprint(pane model.Pane) string { return paneFingerprint(pane) }

func ObjectFingerprint(object model.ObjectIdentity) string { return objectFingerprint(&object) }

func SnapshotFingerprint(snapshot model.Snapshot) string { return snapshotFingerprint(snapshot) }

func digest(value any) string {
	body, _ := json.Marshal(value)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func planID(desired DesiredState, live model.Snapshot, policy Policy, operations []Operation) string {
	value := struct {
		Baseline model.Session
		Server   string
		Session  string
		Policy   Policy
		Ops      []Operation
	}{desired.Session, live.ServerKey, live.TargetSessionID, policy, operations}
	return digest(value)
}

func (p *PlanResult) finish(desired DesiredState, live model.Snapshot) {
	p.ID = planID(desired, live, p.Policy, p.Operations)
	for i := range p.Operations {
		p.Operations[i].ID = fmt.Sprintf("%s/%04d", p.ID, i+1)
	}
}

func objectByRuntime(objects []model.ObjectIdentity, kind ObjectKind, runtimeID string) *model.ObjectIdentity {
	for i := range objects {
		if objects[i].Kind == kind && objects[i].RuntimeID == runtimeID {
			return &objects[i]
		}
	}
	return nil
}

func windowLayoutMatches(want model.Window, objects []model.ObjectIdentity, runtimeID string) bool {
	if want.Layout == "" {
		return true
	}
	object := objectByRuntime(objects, ObjectWindow, runtimeID)
	if object == nil {
		return false
	}
	equal, known := tmuxlayout.Compare(want.Layout, len(want.Panes), object.Layout, object.PaneCount)
	return known && equal
}

func maxIndex(windows []model.Window) int {
	max := -1
	for _, window := range windows {
		if window.Idx > max {
			max = window.Idx
		}
	}
	return max
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
