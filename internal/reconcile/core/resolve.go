package reconcile

import (
	"fmt"
	"sort"

	"github.com/fm39hz/gotomux/internal/identity"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/tmuxlayout"
)

// Resolve matches a desired baseline to a complete live snapshot. It performs
// no I/O and never treats a name or index as sufficient identity evidence.
func Resolve(desired DesiredState, live model.Snapshot) Resolution {
	base := desired.Baseline
	r := Resolution{Status: ResolutionReady, ServerKey: live.ServerKey}
	if !live.Complete || live.ServerKey == "" || live.Target.ServerKey != live.ServerKey || live.TargetSessionID == "" || live.Target.TmuxID != live.TargetSessionID {
		r.addConflict("session", nil, "live observation is incomplete or has inconsistent server/session identity")
		return r
	}
	if err := validateBaselineIdentities(base); err != nil {
		r.addConflict("baseline", nil, err.Error())
		return r
	}
	if err := validateObjectIndex(live); err != nil {
		r.addConflict("server snapshot", nil, err.Error())
		return r
	}
	sessionMatch, sessionConflict := resolveSession(base, live)
	if sessionConflict != nil {
		r.Conflicts = append(r.Conflicts, *sessionConflict)
		r.Status = ResolutionConflict
		return r
	}
	r.TargetSession = sessionMatch

	baseSeenLogical, baseSeenRuntime := map[string]bool{}, map[string]bool{}
	usedLive := map[string]bool{}
	for bi, bw := range base.Desired.Windows {
		if bw.SlotID != "" {
			if baseSeenLogical[bw.SlotID] {
				r.addConflict(windowLabel(bw), nil, "baseline repeats a window slot ID")
				continue
			}
			baseSeenLogical[bw.SlotID] = true
		}
		if sameServerGeneration(base.Bindings.ServerKey, live.ServerKey) && base.Bindings.Windows[bw.SlotID] != "" {
			runtimeID := base.Bindings.Windows[bw.SlotID]
			if baseSeenRuntime[runtimeID] {
				r.addConflict(windowLabel(bw), []string{runtimeID}, "baseline repeats a runtime window ID in this server generation")
				continue
			}
			baseSeenRuntime[runtimeID] = true
		}
		match, conflict, anchored := resolveWindowAnchor(base, live, bi, bw)
		if conflict != nil {
			r.Conflicts = append(r.Conflicts, *conflict)
			continue
		}
		if anchored {
			if usedLive[match.LiveRuntimeID] {
				r.addConflict(windowLabel(bw), []string{match.LiveRuntimeID}, "multiple baseline windows resolve to one live window")
				continue
			}
			usedLive[match.LiveRuntimeID] = true
			windowResolution := WindowResolution{Match: match}
			if !match.OutsideTarget {
				if liveWindow, ok := targetWindowByID(live.Target, match.LiveRuntimeID); ok {
					panes, paneAmbiguous, paneConflicts := resolvePanes(base, live, bi, liveWindow)
					windowResolution.Panes = panes
					r.Ambiguous = append(r.Ambiguous, paneAmbiguous...)
					r.Conflicts = append(r.Conflicts, paneConflicts...)
				}
			} else {
				panes, paneAmbiguous, paneConflicts := resolvePaneReferencesOutside(base, live, bi, match.LiveRuntimeID)
				windowResolution.Panes = panes
				r.Ambiguous = append(r.Ambiguous, paneAmbiguous...)
				r.Conflicts = append(r.Conflicts, paneConflicts...)
			}
			r.Windows = append(r.Windows, windowResolution)
			continue
		}
		r.Missing = append(r.Missing, bi)
	}
	for _, window := range live.Target.Windows {
		if !usedLive[window.TmuxID] {
			r.Extra = append(r.Extra, window.TmuxID)
		}
	}

	sort.Ints(r.Missing)
	sort.Strings(r.Extra)
	sort.Slice(r.Windows, func(i, j int) bool { return r.Windows[i].Match.BaselineIndex < r.Windows[j].Match.BaselineIndex })
	if len(r.Conflicts) > 0 {
		r.Status = ResolutionConflict
	} else {
		for _, ambiguity := range r.Ambiguous {
			if !ambiguity.EquivalentGroup {
				r.Status = ResolutionAmbiguous
				break
			}
		}
	}
	return r
}

func resolveSession(baseline model.Baseline, live model.Snapshot) (ObjectMatch, *Conflict) {
	target := live.Target
	base := baseline.Desired
	runtimeID := baseline.Bindings.SessionID
	match := ObjectMatch{Kind: ObjectSession, BaselineIndex: -1, BaselineIdentityID: base.LogicalID, LiveIdentityID: target.LogicalID, BaselineRuntimeID: runtimeID, LiveRuntimeID: target.TmuxID}
	sameGeneration := baseline.Bindings.ServerKey != "" && baseline.Bindings.ServerKey == live.ServerKey
	if target.LogicalID != "" {
		if base.LogicalID != target.LogicalID {
			return ObjectMatch{}, &Conflict{Baseline: base.Name, Live: []string{target.Name}, Reason: "baseline and live session have different logical IDs"}
		}
		// A logical instance may be recreated while the tmux server survives.
		// Cached runtime bindings do not veto a unique current logical owner;
		// validateObjectIndex rejects duplicate live owners before this point.
		match.Evidence = []Evidence{{Class: EvidenceLogical, Signal: "session-logical-id", Value: base.LogicalID}}
		return match, nil
	}
	if sameGeneration && runtimeID != "" && runtimeID == target.TmuxID {
		match.Evidence = []Evidence{{Class: EvidenceRuntime, Signal: "session-id", Value: target.TmuxID}}
		return match, nil
	}
	return ObjectMatch{}, &Conflict{Baseline: base.Name, Live: []string{target.Name}, Reason: "session identity cannot be established from durable or same-generation runtime evidence"}
}

func resolveWindowAnchor(base model.Baseline, live model.Snapshot, index int, window model.WindowSpec) (ObjectMatch, *Conflict, bool) {
	sameGeneration := sameServerGeneration(base.Bindings.ServerKey, live.ServerKey)
	var tagged, runtime *model.ObjectIdentity
	runtimeID := base.Bindings.Windows[window.SlotID]
	if window.SlotID != "" {
		matches := objectsByStableID(live.Objects, ObjectWindow, window.SlotID)
		if len(matches) > 1 {
			return ObjectMatch{}, &Conflict{Baseline: windowLabel(window), Live: runtimeLabels(matches), Reason: "window slot ID is duplicated on the tmux server"}, false
		}
		if len(matches) == 1 {
			tagged = matches[0]
		}
	}
	if sameGeneration && runtimeID != "" {
		runtime = objectByRuntimeID(live.Objects, ObjectWindow, runtimeID)
	}
	if tagged != nil && runtime != nil && tagged.RuntimeID != runtime.RuntimeID {
		return ObjectMatch{}, &Conflict{Baseline: windowLabel(window), Live: []string{tagged.RuntimeID, runtime.RuntimeID}, Reason: "window slot and runtime bindings identify different objects"}, false
	}
	if tagged != nil {
		return windowObjectMatch(index, window, runtimeID, *tagged, live, Evidence{Class: EvidenceLogical, Signal: "window-slot-id", Value: window.SlotID}), nil, true
	}
	if runtime != nil {
		if runtime.SlotID != "" && runtime.SlotID != window.SlotID {
			return ObjectMatch{}, &Conflict{Baseline: windowLabel(window), Live: []string{runtime.RuntimeID}, Reason: "same runtime window ID carries a different slot ID"}, false
		}
		return windowObjectMatch(index, window, runtimeID, *runtime, live, Evidence{Class: EvidenceRuntime, Signal: "window-id", Value: runtimeID}), nil, true
	}
	return ObjectMatch{}, nil, false
}

func windowObjectMatch(index int, baseline model.WindowSpec, baselineRuntimeID string, live model.ObjectIdentity, snapshot model.Snapshot, evidence Evidence) ObjectMatch {
	return ObjectMatch{Kind: ObjectWindow, BaselineIndex: index, BaselineIdentityID: baseline.SlotID, LiveIdentityID: live.SlotID, BaselineRuntimeID: baselineRuntimeID, LiveRuntimeID: live.RuntimeID, OutsideTarget: !isWindowMember(snapshot.Memberships, snapshot.TargetSessionID, live.RuntimeID), Evidence: []Evidence{evidence}}
}

// resolvePanes establishes ownership from stable tags or same-generation
// runtime lineage. Similar commands, cwd, names and geometry never assign a
// baseline identity to a newly created, unmanaged pane.
func resolvePanes(base model.Baseline, live model.Snapshot, windowIndex int, liveWindow model.Window) (PaneResolution, []Ambiguity, []Conflict) {
	baselineWindow := base.Desired.Windows[windowIndex]
	var out PaneResolution
	var conflicts []Conflict
	used := map[string]bool{}
	for index, pane := range baselineWindow.Panes {
		match, conflict, anchored := resolvePaneAnchor(base, live, baselineWindow, liveWindow, index, pane)
		if conflict != nil {
			conflicts = append(conflicts, *conflict)
			continue
		}
		if !anchored {
			out.Missing = append(out.Missing, index)
			continue
		}
		if used[match.LiveRuntimeID] {
			conflicts = append(conflicts, Conflict{Baseline: fmt.Sprintf("%s pane %d", windowLabel(baselineWindow), index), Live: []string{match.LiveRuntimeID}, Reason: "multiple baseline panes resolve to one live pane"})
			continue
		}
		used[match.LiveRuntimeID] = true
		out.Matches = append(out.Matches, match)
	}
	for _, pane := range liveWindow.Panes {
		if !used[pane.TmuxID] {
			out.Extra = append(out.Extra, pane.TmuxID)
		}
	}
	sort.Ints(out.Missing)
	sort.Strings(out.Extra)
	return out, nil, conflicts
}

func resolvePaneReferencesOutside(base model.Baseline, live model.Snapshot, windowIndex int, windowID string) (PaneResolution, []Ambiguity, []Conflict) {
	want := base.Desired.Windows[windowIndex]
	var out PaneResolution
	var conflicts []Conflict
	refs := map[string]model.ObjectIdentity{}
	for _, object := range live.Objects {
		if object.Kind == ObjectPane && object.ParentID == windowID {
			refs[object.RuntimeID] = object
		}
	}
	used := map[string]bool{}
	for index, pane := range want.Panes {
		var found *model.ObjectIdentity
		if pane.SlotID != "" {
			matches := objectsByStableID(live.Objects, ObjectPane, pane.SlotID)
			if len(matches) > 1 {
				conflicts = append(conflicts, Conflict{Baseline: fmt.Sprintf("%s pane %d", windowLabel(want), index), Live: runtimeLabels(matches), Reason: "pane slot ID is duplicated on the tmux server"})
				continue
			}
			if len(matches) == 1 {
				found = matches[0]
			}
		}
		runtimeID := base.Bindings.Panes[pane.SlotID]
		if found == nil && sameServerGeneration(base.Bindings.ServerKey, live.ServerKey) {
			if ref, ok := refs[runtimeID]; ok {
				copy := ref
				found = &copy
			}
		}
		if found == nil {
			out.Missing = append(out.Missing, index)
			continue
		}
		if found.ParentID != windowID {
			conflicts = append(conflicts, Conflict{Baseline: fmt.Sprintf("%s pane %d", windowLabel(want), index), Live: []string{found.RuntimeID}, Reason: "identified pane belongs to a different window"})
			continue
		}
		if found.SlotID != "" && found.SlotID != pane.SlotID {
			conflicts = append(conflicts, Conflict{Baseline: fmt.Sprintf("%s pane %d", windowLabel(want), index), Live: []string{found.RuntimeID}, Reason: "pane slot and runtime bindings conflict"})
			continue
		}
		used[found.RuntimeID] = true
		out.Matches = append(out.Matches, ObjectMatch{Kind: ObjectPane, BaselineIndex: index, BaselineIdentityID: pane.SlotID, LiveIdentityID: found.SlotID, BaselineRuntimeID: runtimeID, LiveRuntimeID: found.RuntimeID, Evidence: []Evidence{{Class: EvidenceRuntime, Signal: "pane-reference", Value: found.RuntimeID}}})
	}
	for id := range refs {
		if !used[id] {
			out.Extra = append(out.Extra, id)
		}
	}
	sort.Ints(out.Missing)
	sort.Strings(out.Extra)
	return out, nil, conflicts
}

func resolvePaneAnchor(base model.Baseline, live model.Snapshot, baseWindow model.WindowSpec, liveWindow model.Window, index int, pane model.PaneSpec) (ObjectMatch, *Conflict, bool) {
	sameGeneration := base.Bindings.ServerKey != "" && base.Bindings.ServerKey == live.ServerKey
	var tagged, runtime *model.ObjectIdentity
	runtimeID := base.Bindings.Panes[pane.SlotID]
	if pane.SlotID != "" {
		matches := objectsByStableID(live.Objects, ObjectPane, pane.SlotID)
		if len(matches) > 1 {
			return ObjectMatch{}, &Conflict{Baseline: fmt.Sprintf("%s pane %d", windowLabel(baseWindow), index), Live: runtimeLabels(matches), Reason: "pane slot ID is duplicated on the tmux server"}, false
		}
		if len(matches) == 1 {
			tagged = matches[0]
		}
	}
	if sameGeneration && runtimeID != "" {
		runtime = objectByRuntimeID(live.Objects, ObjectPane, runtimeID)
	}
	if tagged != nil && runtime != nil && tagged.RuntimeID != runtime.RuntimeID {
		return ObjectMatch{}, &Conflict{Baseline: fmt.Sprintf("%s pane %d", windowLabel(baseWindow), index), Live: []string{tagged.RuntimeID, runtime.RuntimeID}, Reason: "pane slot and runtime bindings identify different objects"}, false
	}
	if tagged != nil {
		if tagged.ParentID != liveWindow.TmuxID {
			return ObjectMatch{}, &Conflict{Baseline: fmt.Sprintf("%s pane %d", windowLabel(baseWindow), index), Live: []string{tagged.RuntimeID}, Reason: "pane slot belongs to a different window"}, false
		}
		return paneObjectMatch(index, pane, runtimeID, *tagged, Evidence{Class: EvidenceLogical, Signal: "pane-slot-id", Value: pane.SlotID}), nil, true
	}
	if runtime != nil {
		if runtime.ParentID != liveWindow.TmuxID {
			return ObjectMatch{}, &Conflict{Baseline: fmt.Sprintf("%s pane %d", windowLabel(baseWindow), index), Live: []string{runtime.RuntimeID}, Reason: "same runtime pane ID belongs to a different window"}, false
		}
		if runtime.SlotID != "" && runtime.SlotID != pane.SlotID {
			return ObjectMatch{}, &Conflict{Baseline: fmt.Sprintf("%s pane %d", windowLabel(baseWindow), index), Live: []string{runtime.RuntimeID}, Reason: "same runtime pane ID carries a different slot ID"}, false
		}
		return paneObjectMatch(index, pane, runtimeID, *runtime, Evidence{Class: EvidenceRuntime, Signal: "pane-id", Value: runtimeID}), nil, true
	}
	return ObjectMatch{}, nil, false
}

func paneObjectMatch(index int, baseline model.PaneSpec, baselineRuntimeID string, live model.ObjectIdentity, evidence Evidence) ObjectMatch {
	return ObjectMatch{Kind: ObjectPane, BaselineIndex: index, BaselineIdentityID: baseline.SlotID, LiveIdentityID: live.SlotID, BaselineRuntimeID: baselineRuntimeID, LiveRuntimeID: live.RuntimeID, Evidence: []Evidence{evidence}}
}

func validateBaselineIdentities(base model.Baseline) error {
	if base.SchemaVersion != model.CurrentBaselineSchemaVersion {
		return fmt.Errorf("baseline schema version %d is unsupported; freeze the session again", base.SchemaVersion)
	}
	if base.Desired.LogicalID == "" {
		return fmt.Errorf("baseline has no session identity")
	}
	if len(base.Desired.Windows) == 0 {
		return fmt.Errorf("baseline has no windows")
	}
	seen := map[string]string{}
	check := func(kind ObjectKind, runtimeID, id string) error {
		if id == "" {
			return fmt.Errorf("baseline %s has no identity", kind)
		}
		if err := identity.Validate(id); err != nil {
			return fmt.Errorf("baseline %s has malformed identity: %w", kind, err)
		}
		key := string(kind) + " " + runtimeID
		if old, ok := seen[id]; ok && old != key {
			return fmt.Errorf("baseline stable ID %s is duplicated by %s and %s", id, old, key)
		}
		seen[id] = key
		return nil
	}
	if err := check(ObjectSession, base.Bindings.SessionID, base.Desired.LogicalID); err != nil {
		return err
	}
	for _, window := range base.Desired.Windows {
		if err := check(ObjectWindow, base.Bindings.Windows[window.SlotID], window.SlotID); err != nil {
			return err
		}
		if len(window.Panes) == 0 {
			return fmt.Errorf("baseline window %d has no panes", window.Idx)
		}
		for _, pane := range window.Panes {
			if err := check(ObjectPane, base.Bindings.Panes[pane.SlotID], pane.SlotID); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateObjectIndex(snapshot model.Snapshot) error {
	objects := snapshot.Objects
	seen := map[string]bool{}
	logical := map[string]string{}
	operations := map[string]string{}
	byRuntime := make(map[string]model.ObjectIdentity, len(objects))
	paneCounts := map[string]int{}
	for _, object := range objects {
		if object.RuntimeID == "" {
			return fmt.Errorf("server snapshot contains an object without runtime ID")
		}
		if err := (RuntimeRef{ServerGeneration: snapshot.ServerKey, Kind: object.Kind, ID: object.RuntimeID}).Validate(); err != nil {
			return fmt.Errorf("server snapshot contains invalid runtime identity: %w", err)
		}
		key := string(object.Kind) + " " + object.RuntimeID
		if seen[key] {
			return fmt.Errorf("server snapshot repeats runtime object %s", key)
		}
		seen[key] = true
		byRuntime[key] = object
		if object.Kind == ObjectPane {
			paneCounts[object.ParentID]++
		}
		stableID := object.SlotID
		if object.Kind == ObjectSession {
			stableID = object.LogicalID
		}
		if stableID != "" {
			if err := identity.Validate(stableID); err != nil {
				return fmt.Errorf("server snapshot has malformed stable ID on %s: %w", key, err)
			}
			if prior, ok := logical[stableID]; ok && prior != key {
				return fmt.Errorf("server snapshot duplicates stable ID %s on %s and %s", stableID, prior, key)
			}
			logical[stableID] = key
		}
		if object.OperationID != "" {
			if prior, ok := operations[object.OperationID]; ok && prior != key {
				return fmt.Errorf("server snapshot duplicates reconcile operation marker %s on %s and %s", object.OperationID, prior, key)
			}
			operations[object.OperationID] = key
		}
	}
	for _, object := range objects {
		if object.Kind == ObjectWindow && object.PaneCount != paneCounts[object.RuntimeID] {
			return fmt.Errorf("server snapshot window %s pane count is incomplete", object.RuntimeID)
		}
	}
	targetSession, ok := byRuntime[string(ObjectSession)+" "+snapshot.Target.TmuxID]
	if !ok || targetSession.LogicalID != snapshot.Target.LogicalID {
		return fmt.Errorf("server snapshot target session is absent or has inconsistent logical identity")
	}
	sessionRefs := map[string]model.SessionIdentity{}
	for _, session := range snapshot.Sessions {
		if session.Name == "" || session.RuntimeID == "" {
			return fmt.Errorf("server snapshot contains incomplete session reference")
		}
		if _, exists := sessionRefs[session.RuntimeID]; exists {
			return fmt.Errorf("server snapshot repeats session runtime ID %s", session.RuntimeID)
		}
		sessionRefs[session.RuntimeID] = session
		ref, ok := byRuntime[string(ObjectSession)+" "+session.RuntimeID]
		if !ok || ref.LogicalID != session.LogicalID {
			return fmt.Errorf("server snapshot session %s has inconsistent identity metadata", session.RuntimeID)
		}
	}
	if _, ok := sessionRefs[snapshot.TargetSessionID]; !ok {
		return fmt.Errorf("target session is absent from server session index")
	}
	if snapshot.ActiveWindowID != "" && !isWindowMember(snapshot.Memberships, snapshot.TargetSessionID, snapshot.ActiveWindowID) {
		return fmt.Errorf("active window is not a member of the target session")
	}
	seenMembership := map[string]bool{}
	seenIndex := map[string]string{}
	for _, member := range snapshot.Memberships {
		if _, ok := sessionRefs[member.SessionID]; !ok {
			return fmt.Errorf("membership references unknown session %s", member.SessionID)
		}
		if _, ok := byRuntime[string(ObjectWindow)+" "+member.WindowID]; !ok {
			return fmt.Errorf("membership references unknown window %s", member.WindowID)
		}
		key := member.SessionID + "\x00" + member.WindowID
		if seenMembership[key] {
			return fmt.Errorf("duplicate membership for window %s", member.WindowID)
		}
		seenMembership[key] = true
		indexKey := fmt.Sprintf("%s\x00%d", member.SessionID, member.Index)
		if prior, exists := seenIndex[indexKey]; exists && prior != member.WindowID {
			return fmt.Errorf("session %s has duplicate window index %d", member.SessionID, member.Index)
		}
		seenIndex[indexKey] = member.WindowID
	}
	for _, window := range snapshot.Target.Windows {
		object, ok := byRuntime[string(ObjectWindow)+" "+window.TmuxID]
		objectLayout := tmuxlayout.ForStore(object.Layout, object.PaneCount)
		if !ok || object.SlotID != window.SlotID || object.PaneCount != len(window.Panes) || objectLayout != window.Layout || !isWindowMember(snapshot.Memberships, snapshot.TargetSessionID, window.TmuxID) {
			return fmt.Errorf("target window %s is absent or has inconsistent membership/identity", window.TmuxID)
		}
		for _, pane := range window.Panes {
			object, ok := byRuntime[string(ObjectPane)+" "+pane.TmuxID]
			if !ok || object.SlotID != pane.SlotID || object.ParentID != window.TmuxID {
				return fmt.Errorf("target pane %s is absent or has inconsistent parent/identity", pane.TmuxID)
			}
		}
	}
	return nil
}

func objectsByStableID(objects []model.ObjectIdentity, kind ObjectKind, stableID string) []*model.ObjectIdentity {
	var found []*model.ObjectIdentity
	for i := range objects {
		objectID := objects[i].SlotID
		if kind == ObjectSession {
			objectID = objects[i].LogicalID
		}
		if objects[i].Kind == kind && objectID == stableID {
			found = append(found, &objects[i])
		}
	}
	return found
}

func objectByRuntimeID(objects []model.ObjectIdentity, kind ObjectKind, runtimeID string) *model.ObjectIdentity {
	for i := range objects {
		if objects[i].Kind == kind && objects[i].RuntimeID == runtimeID {
			return &objects[i]
		}
	}
	return nil
}

func runtimeLabels(objects []*model.ObjectIdentity) []string {
	labels := make([]string, 0, len(objects))
	for _, object := range objects {
		labels = append(labels, object.RuntimeID)
	}
	sort.Strings(labels)
	return labels
}

func targetWindowByID(session model.ObservedSession, runtimeID string) (model.Window, bool) {
	for _, window := range session.Windows {
		if window.TmuxID == runtimeID {
			return window, true
		}
	}
	return model.Window{}, false
}

func isWindowMember(memberships []model.WindowMembership, sessionID, windowID string) bool {
	for _, membership := range memberships {
		if membership.SessionID == sessionID && membership.WindowID == windowID {
			return true
		}
	}
	return false
}

func (r *Resolution) addConflict(baseline string, live []string, reason string) {
	r.Conflicts = append(r.Conflicts, Conflict{Baseline: baseline, Live: live, Reason: reason})
	r.Status = ResolutionConflict
}

func sameServerGeneration(a, b string) bool { return a != "" && a == b }

func windowLabel(window model.WindowSpec) string {
	if window.Name != "" {
		return fmt.Sprintf("%s@index-%d", window.Name, window.Idx)
	}
	return fmt.Sprintf("window@index-%d", window.Idx)
}
