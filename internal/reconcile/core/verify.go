package reconcile

import (
	"fmt"
	"reflect"

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
	if err := validateDesiredRecipe(desired.Baseline.Desired); err != nil {
		return fmt.Errorf("verify desired identity: %w", err)
	}
	if err := validateObjectIndex(live); err != nil {
		return fmt.Errorf("verify live identity: %w", err)
	}
	if desired.Baseline.Desired.LogicalID == "" || live.Target.LogicalID != desired.Baseline.Desired.LogicalID {
		return fmt.Errorf("target session logical identity does not match baseline")
	}
	recipe := desired.Baseline.Desired
	if len(resolution.Missing) != 0 || len(resolution.Windows) != len(recipe.Windows) {
		return fmt.Errorf("baseline has %d missing window(s)", len(resolution.Missing))
	}
	if policy == PolicyExact && len(resolution.Extra) != 0 {
		return fmt.Errorf("exact reconciliation left %d extra window(s)", len(resolution.Extra))
	}
	matched := make(map[int]bool, len(resolution.Windows))
	for _, windowResolution := range resolution.Windows {
		match := windowResolution.Match
		if match.BaselineIndex < 0 || match.BaselineIndex >= len(desired.Baseline.Desired.Windows) || matched[match.BaselineIndex] {
			return fmt.Errorf("verification received duplicate or invalid baseline window mapping")
		}
		matched[match.BaselineIndex] = true
		want := recipe.Windows[match.BaselineIndex]
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
		wantRole, gotRole := semanticRecipeWindowRole(desired.ClassifierVersion, recipe.Windows[match.BaselineIndex]), semanticObservedWindowRole(live.Target, got)
		if wantRole != "" && gotRole != wantRole && (policy == PolicyExact || !hasDurableIdentity(match)) {
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
			paneOrderMatches, paneOrderKnown := paneSlotLayoutMatches(want, got, windowResolution.Panes)
			if paneOrderKnown && !paneOrderMatches {
				return fmt.Errorf("window %s pane slots do not occupy their frozen layout positions", got.TmuxID)
			}
		}
	}
	if policy == PolicyExact && len(live.Target.Windows) != len(desired.Baseline.Desired.Windows) {
		return fmt.Errorf("exact reconciliation left %d live windows; baseline has %d", len(live.Target.Windows), len(desired.Baseline.Desired.Windows))
	}
	return nil
}

func hasDurableIdentity(match ObjectMatch) bool {
	for _, evidence := range match.Evidence {
		if evidence.Class == EvidenceRuntime || evidence.Class == EvidenceLogical {
			return true
		}
	}
	return false
}

func paneSlotLayoutMatches(want model.WindowSpec, got model.Window, resolution PaneResolution) (matches, known bool) {
	wantBindings, err := tmuxlayout.PaneBindings(want.Layout)
	if err != nil {
		return false, true
	}
	if len(wantBindings) == 0 {
		return true, false
	}
	gotBindings, err := tmuxlayout.PaneBindings(got.Layout)
	if err != nil || len(gotBindings) != len(wantBindings) {
		return false, true
	}
	baselineSlotByRuntime := make(map[string]string, len(want.Panes))
	baselineSlotByIndex := make(map[int]string, len(want.Panes))
	for _, pane := range want.Panes {
		if pane.SlotID == "" {
			return false, true
		}
		baselineSlotByRuntime[pane.SlotID] = pane.SlotID
		baselineSlotByIndex[pane.Idx] = pane.SlotID
	}
	liveSlotByRuntime := make(map[string]string, len(resolution.Matches))
	for _, match := range resolution.Matches {
		if match.BaselineIndex < 0 || match.BaselineIndex >= len(want.Panes) || match.LiveRuntimeID == "" {
			return false, true
		}
		liveSlotByRuntime[match.LiveRuntimeID] = want.Panes[match.BaselineIndex].SlotID
	}
	wantSlots := make([]string, 0, len(wantBindings))
	gotSlots := make([]string, 0, len(gotBindings))
	for _, binding := range wantBindings {
		slotID := baselineSlotByRuntime[binding.RuntimeID]
		if slotID == "" {
			slotID = baselineSlotByIndex[binding.Index]
		}
		if slotID == "" {
			return false, true
		}
		wantSlots = append(wantSlots, slotID)
	}
	for _, binding := range gotBindings {
		slotID := liveSlotByRuntime[binding.RuntimeID]
		if slotID == "" {
			return false, true
		}
		gotSlots = append(gotSlots, slotID)
	}
	return reflect.DeepEqual(wantSlots, gotSlots), true
}

// RefreshBindings carries verified runtime IDs into the baseline using the
// resolver's explicit object mapping. Index is never used to choose a live
// object for a baseline row.
func RefreshBindings(desired model.Baseline, live model.Snapshot, resolution Resolution) (model.Baseline, error) {
	if !live.Complete || live.ServerKey == "" || resolution.Status != ResolutionReady || resolution.ServerKey != live.ServerKey {
		return model.Baseline{}, fmt.Errorf("cannot refresh bindings from an incomplete or unresolved snapshot")
	}
	if err := validateDesiredRecipe(desired.Desired); err != nil {
		return model.Baseline{}, err
	}
	if desired.Desired.LogicalID != live.Target.LogicalID {
		return model.Baseline{}, fmt.Errorf("session logical identity changed before binding refresh")
	}
	updated := cloneBaseline(desired)
	updated.Bindings = model.RuntimeBindings{ServerKey: live.ServerKey, SessionID: live.Target.TmuxID, Windows: map[string]string{}, Panes: map[string]string{}}
	seenWindows := map[int]bool{}
	for _, wr := range resolution.Windows {
		m := wr.Match
		if m.OutsideTarget || m.BaselineIndex < 0 || m.BaselineIndex >= len(updated.Desired.Windows) || seenWindows[m.BaselineIndex] {
			return model.Baseline{}, fmt.Errorf("cannot refresh an invalid/out-of-session window mapping")
		}
		seenWindows[m.BaselineIndex] = true
		want := updated.Desired.Windows[m.BaselineIndex]
		got, ok := targetWindowByID(live.Target, m.LiveRuntimeID)
		if !ok || got.SlotID != want.SlotID || !isWindowMember(live.Memberships, live.TargetSessionID, got.TmuxID) {
			return model.Baseline{}, fmt.Errorf("window mapping failed identity verification")
		}
		updated.Bindings.Windows[want.SlotID] = got.TmuxID
		seenPanes := map[int]bool{}
		for _, pm := range wr.Panes.Matches {
			if pm.BaselineIndex < 0 || pm.BaselineIndex >= len(want.Panes) || seenPanes[pm.BaselineIndex] {
				return model.Baseline{}, fmt.Errorf("window %s has an invalid pane binding map", got.TmuxID)
			}
			seenPanes[pm.BaselineIndex] = true
			pane := want.Panes[pm.BaselineIndex]
			lp := paneByRuntimeID(got, pm.LiveRuntimeID)
			if lp == nil || lp.SlotID != pane.SlotID || lp.TmuxID == "" {
				return model.Baseline{}, fmt.Errorf("pane mapping failed identity verification")
			}
			updated.Bindings.Panes[pane.SlotID] = lp.TmuxID
		}
		if len(seenPanes) != len(want.Panes) {
			return model.Baseline{}, fmt.Errorf("window %s is missing verified pane bindings", got.TmuxID)
		}
		if len(wr.Panes.Missing) != 0 {
			return model.Baseline{}, fmt.Errorf("window %s still has missing panes", got.TmuxID)
		}
	}
	if len(seenWindows) != len(updated.Desired.Windows) {
		return model.Baseline{}, fmt.Errorf("baseline is missing verified window bindings")
	}
	updated.SchemaVersion = model.CurrentBaselineSchemaVersion
	return updated, nil
}

func cloneBaseline(b model.Baseline) model.Baseline {
	b.Desired.Windows = append([]model.WindowSpec(nil), b.Desired.Windows...)
	for i := range b.Desired.Windows {
		b.Desired.Windows[i].Panes = append([]model.PaneSpec(nil), b.Desired.Windows[i].Panes...)
	}
	b.Bindings.Windows = copyBindings(b.Bindings.Windows)
	b.Bindings.Panes = copyBindings(b.Bindings.Panes)
	return b
}

func copyBindings(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func paneByRuntimeID(window model.Window, id string) *model.Pane {
	for i := range window.Panes {
		if window.Panes[i].TmuxID == id {
			return &window.Panes[i]
		}
	}
	return nil
}
