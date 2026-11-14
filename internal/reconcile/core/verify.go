package reconcile

import (
	"fmt"

	"github.com/fm39hz/gotomux/internal/classify"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/tmuxlayout"
)

// Verify checks the required postconditions against the same identity lineage
// used by Resolve and Plan. It never rediscovers a baseline object by index or
// display name.
func Verify(desired DesiredState, live model.Snapshot, resolution Resolution, policy Policy, plan PlanResult) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if !live.Complete || live.ServerKey == "" || resolution.Status != ResolutionReady || resolution.ServerKey != live.ServerKey {
		return fmt.Errorf("verification requires a complete, identity-resolved snapshot")
	}
	if err := validateDesiredIdentities(desired.Session); err != nil {
		return fmt.Errorf("verify desired identity: %w", err)
	}
	if err := validateObjectIndex(live); err != nil {
		return fmt.Errorf("verify live identity: %w", err)
	}
	if desired.Session.LogicalID == "" || live.Target.LogicalID != desired.Session.LogicalID {
		return fmt.Errorf("target session logical identity does not match baseline")
	}
	if desired.Session.TmuxID != "" && desired.Session.ServerKey == live.ServerKey && desired.Session.TmuxID != live.Target.TmuxID {
		return fmt.Errorf("target session runtime identity changed within the same server generation")
	}
	if len(resolution.Missing) != 0 || len(resolution.Windows) != len(desired.Session.Windows) {
		return fmt.Errorf("baseline has %d missing window(s)", len(resolution.Missing))
	}
	if policy == PolicyExact && len(resolution.Extra) != 0 {
		return fmt.Errorf("exact reconciliation left %d extra window(s)", len(resolution.Extra))
	}
	matched := make(map[int]bool, len(resolution.Windows))
	for _, windowResolution := range resolution.Windows {
		match := windowResolution.Match
		if match.BaselineIndex < 0 || match.BaselineIndex >= len(desired.Session.Windows) || matched[match.BaselineIndex] {
			return fmt.Errorf("verification received duplicate or invalid baseline window mapping")
		}
		matched[match.BaselineIndex] = true
		want := desired.Session.Windows[match.BaselineIndex]
		if match.OutsideTarget {
			return fmt.Errorf("baseline window %q is still outside the target session", want.Name)
		}
		got, ok := targetWindowByID(live.Target, match.LiveRuntimeID)
		if !ok || !isWindowMember(live.Memberships, live.TargetSessionID, got.TmuxID) {
			return fmt.Errorf("baseline window %q is not a member of the target session", want.Name)
		}
		if got.Idx != want.Idx {
			return fmt.Errorf("identified window %s is at index %d; baseline requires %d", got.TmuxID, got.Idx, want.Idx)
		}
		if got.SlotID != want.SlotID {
			return fmt.Errorf("identified window %s has slot ID %q; baseline requires %q", got.TmuxID, got.SlotID, want.SlotID)
		}
		wantRole, gotRole := semanticWindowRole(desired.Session, want), semanticWindowRole(live.Target, got)
		if wantRole != "" && gotRole != wantRole {
			return fmt.Errorf("window %s has semantic role %q; baseline requires %q", got.TmuxID, gotRole, wantRole)
		}
		if len(windowResolution.Panes.Missing) != 0 {
			return fmt.Errorf("window %s still has %d missing baseline pane(s)", got.TmuxID, len(windowResolution.Panes.Missing))
		}
		if policy == PolicyExact && (len(windowResolution.Panes.Extra) != 0 || len(got.Panes) != len(want.Panes)) {
			return fmt.Errorf("window %s does not have the exact baseline pane set", got.TmuxID)
		}
		paneMatched := make(map[int]bool, len(windowResolution.Panes.Matches))
		for _, paneMatch := range windowResolution.Panes.Matches {
			if paneMatch.BaselineIndex < 0 || paneMatch.BaselineIndex >= len(want.Panes) || paneMatched[paneMatch.BaselineIndex] {
				return fmt.Errorf("window %s has duplicate or invalid pane mapping", got.TmuxID)
			}
			paneMatched[paneMatch.BaselineIndex] = true
			wantPane := want.Panes[paneMatch.BaselineIndex]
			livePane := paneByRuntimeID(got, paneMatch.LiveRuntimeID)
			if livePane == nil || livePane.SlotID != wantPane.SlotID {
				return fmt.Errorf("baseline pane %s is not represented by its identified live pane", wantPane.SlotID)
			}
		}
		if len(paneMatched) != len(want.Panes) {
			return fmt.Errorf("window %s has unresolved baseline pane identity", got.TmuxID)
		}
		if want.Layout != "" && (policy == PolicyExact || len(windowResolution.Panes.Extra) == 0) {
			equal, known := tmuxlayout.Compare(want.Layout, len(want.Panes), got.Layout, len(got.Panes))
			if !known || !equal {
				return fmt.Errorf("window %s split topology does not match baseline", got.TmuxID)
			}
		}
	}
	if policy == PolicyExact && len(live.Target.Windows) != len(desired.Session.Windows) {
		return fmt.Errorf("exact reconciliation left %d live windows; baseline has %d", len(live.Target.Windows), len(desired.Session.Windows))
	}
	if plan.FinalActiveSlotID != "" && live.ActiveWindowID != "" {
		active := targetWindowByIDValue(live.Target, live.ActiveWindowID)
		if active == nil || active.SlotID != plan.FinalActiveSlotID {
			return fmt.Errorf("active window does not match the planned baseline slot")
		}
	} else if plan.FinalActiveWindowID != "" && live.ActiveWindowID != "" && live.ActiveWindowID != plan.FinalActiveWindowID {
		return fmt.Errorf("active window identity changed")
	}
	return nil
}

// RefreshBindings carries verified runtime IDs into the baseline using the
// resolver's explicit object mapping. Index is never used to choose a live
// object for a baseline row.
func RefreshBindings(desired model.Session, live model.Snapshot, resolution Resolution) (model.Session, error) {
	if !live.Complete || live.ServerKey == "" || resolution.Status != ResolutionReady || resolution.ServerKey != live.ServerKey {
		return model.Session{}, fmt.Errorf("cannot refresh bindings from an incomplete or unresolved snapshot")
	}
	if err := validateDesiredIdentities(desired); err != nil {
		return model.Session{}, err
	}
	if desired.LogicalID != live.Target.LogicalID {
		return model.Session{}, fmt.Errorf("session logical identity changed before binding refresh")
	}
	updated := cloneSession(desired)
	updated.TmuxID = live.Target.TmuxID
	updated.ServerKey = live.ServerKey
	updated.SchemaVersion = model.CurrentBaselineSchemaVersion
	updated.ClassifierVersion = classify.WindowRoleVersion
	seenWindows := map[int]bool{}
	for _, windowResolution := range resolution.Windows {
		match := windowResolution.Match
		if match.OutsideTarget || match.BaselineIndex < 0 || match.BaselineIndex >= len(updated.Windows) || seenWindows[match.BaselineIndex] {
			return model.Session{}, fmt.Errorf("cannot refresh an invalid/out-of-session window mapping")
		}
		seenWindows[match.BaselineIndex] = true
		liveWindow, ok := targetWindowByID(live.Target, match.LiveRuntimeID)
		if !ok || liveWindow.SlotID != updated.Windows[match.BaselineIndex].SlotID {
			return model.Session{}, fmt.Errorf("window mapping failed identity verification")
		}
		baseWindow := &updated.Windows[match.BaselineIndex]
		baseWindow.TmuxID = liveWindow.TmuxID
		baseWindow.Role = semanticWindowRole(live.Target, liveWindow)
		paneSeen := map[int]bool{}
		for _, paneMatch := range windowResolution.Panes.Matches {
			if paneMatch.BaselineIndex < 0 || paneMatch.BaselineIndex >= len(baseWindow.Panes) || paneSeen[paneMatch.BaselineIndex] {
				return model.Session{}, fmt.Errorf("window %s has an invalid pane binding map", liveWindow.TmuxID)
			}
			paneSeen[paneMatch.BaselineIndex] = true
			livePane := paneByRuntimeID(liveWindow, paneMatch.LiveRuntimeID)
			if livePane == nil || livePane.SlotID != baseWindow.Panes[paneMatch.BaselineIndex].SlotID {
				return model.Session{}, fmt.Errorf("pane mapping failed identity verification")
			}
			baseWindow.Panes[paneMatch.BaselineIndex].TmuxID = livePane.TmuxID
		}
		if len(paneSeen) != len(baseWindow.Panes) {
			return model.Session{}, fmt.Errorf("window %s is missing verified pane bindings", liveWindow.TmuxID)
		}
	}
	if len(seenWindows) != len(updated.Windows) {
		return model.Session{}, fmt.Errorf("baseline is missing verified window bindings")
	}
	return updated, nil
}

func cloneSession(session model.Session) model.Session {
	copy := session
	copy.Windows = append([]model.Window(nil), session.Windows...)
	for i := range copy.Windows {
		copy.Windows[i].Panes = append([]model.Pane(nil), session.Windows[i].Panes...)
	}
	return copy
}

func paneByRuntimeID(window model.Window, id string) *model.Pane {
	for i := range window.Panes {
		if window.Panes[i].TmuxID == id {
			return &window.Panes[i]
		}
	}
	return nil
}

func targetWindowByIDValue(session model.Session, id string) *model.Window {
	for i := range session.Windows {
		if session.Windows[i].TmuxID == id {
			return &session.Windows[i]
		}
	}
	return nil
}
