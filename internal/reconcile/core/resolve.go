package reconcile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fm39hz/gotomux/internal/identity"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/tmuxlayout"
)

// Resolve matches a desired baseline to a complete live snapshot. It performs
// no I/O and never treats a name or index as sufficient identity evidence.
func Resolve(desired DesiredState, live model.Snapshot) Resolution {
	base := desired.Session
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
	var semanticBase []int
	usedLive := map[string]bool{}
	for bi, bw := range base.Windows {
		if bw.SlotID != "" {
			if baseSeenLogical[bw.SlotID] {
				r.addConflict(windowLabel(bw), nil, "baseline repeats a window slot ID")
				continue
			}
			baseSeenLogical[bw.SlotID] = true
		}
		if sameServer(&base, &live.Target) && bw.TmuxID != "" {
			if baseSeenRuntime[bw.TmuxID] {
				r.addConflict(windowLabel(bw), []string{bw.TmuxID}, "baseline repeats a runtime window ID in this server generation")
				continue
			}
			baseSeenRuntime[bw.TmuxID] = true
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
		semanticBase = append(semanticBase, bi)
	}

	var semanticLive []model.Window
	for _, lw := range live.Target.Windows {
		if !usedLive[lw.TmuxID] && lw.SlotID == "" {
			semanticLive = append(semanticLive, lw)
		}
	}
	scores := make([][]scoreVector, len(semanticBase))
	allowed := make([][]bool, len(semanticBase))
	evidence := make([][]semanticEvidence, len(semanticBase))
	for ri, bi := range semanticBase {
		scores[ri] = make([]scoreVector, len(semanticLive))
		allowed[ri] = make([]bool, len(semanticLive))
		evidence[ri] = make([]semanticEvidence, len(semanticLive))
		for li, lw := range semanticLive {
			e := semanticWindowEvidence(&base, base.Windows[bi], &live.Target, lw)
			scores[ri][li], allowed[ri][li], evidence[ri][li] = e.score, e.allowed, e
		}
	}
	semanticUsed := map[string]bool{}
	groupResolved := resolveEquivalentWindowGroup(&r, base, live, semanticBase, semanticLive, scores, allowed, evidence, semanticUsed)
	if !groupResolved {
		assignment, optimum := maxLexAssignment(scores, allowed)
		ambiguousBase := map[int]bool{}
		for ri, li := range assignment {
			if li < 0 || li >= len(semanticLive) || !allowed[ri][li] {
				continue
			}
			alternative := cloneAllowed(allowed)
			alternative[ri][li] = false
			_, alternativeScore := maxLexAssignment(scores, alternative)
			if alternativeScore.equal(optimum) {
				labels := semanticCandidates(evidence[ri], semanticLive)
				r.Ambiguous = append(r.Ambiguous, Ambiguity{Baseline: windowLabel(base.Windows[semanticBase[ri]]), Live: labels, Reason: "multiple one-to-one semantic assignments have equal evidence", Evidence: evidence[ri][li].items})
				ambiguousBase[semanticBase[ri]] = true
				r.Missing = append(r.Missing, semanticBase[ri])
				continue
			}
			lw := semanticLive[li]
			semanticUsed[lw.TmuxID] = true
			match := ObjectMatch{Kind: ObjectWindow, BaselineIndex: semanticBase[ri], BaselineIdentityID: base.Windows[semanticBase[ri]].SlotID, LiveIdentityID: lw.SlotID, BaselineRuntimeID: base.Windows[semanticBase[ri]].TmuxID, LiveRuntimeID: lw.TmuxID, Evidence: evidence[ri][li].items}
			panes, paneAmbiguous, paneConflicts := resolvePanes(base, live, semanticBase[ri], lw)
			r.Windows = append(r.Windows, WindowResolution{Match: match, Panes: panes})
			r.Ambiguous = append(r.Ambiguous, paneAmbiguous...)
			r.Conflicts = append(r.Conflicts, paneConflicts...)
		}
		for ri, bi := range semanticBase {
			if ambiguousBase[bi] {
				continue
			}
			if assignment[ri] >= 0 && assignment[ri] < len(semanticLive) && allowed[ri][assignment[ri]] {
				continue
			}
			var plausible []string
			var plausibleEvidence []Evidence
			for li, e := range evidence[ri] {
				if e.plausible {
					plausible = append(plausible, windowLabel(semanticLive[li]))
					plausibleEvidence = append(plausibleEvidence, e.items...)
				}
			}
			if len(plausible) > 0 {
				sort.Strings(plausible)
				r.Ambiguous = append(r.Ambiguous, Ambiguity{Baseline: windowLabel(base.Windows[bi]), Live: uniqueStrings(plausible), Reason: "candidate evidence is insufficient for a safe identity match", Evidence: dedupeEvidence(plausibleEvidence)})
				r.Missing = append(r.Missing, bi)
				continue
			}
			r.Missing = append(r.Missing, bi)
		}
	}

	for _, lw := range live.Target.Windows {
		if !usedLive[lw.TmuxID] && !semanticUsed[lw.TmuxID] {
			r.Extra = append(r.Extra, lw.TmuxID)
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

func resolveSession(base model.Session, live model.Snapshot) (ObjectMatch, *Conflict) {
	target := live.Target
	match := ObjectMatch{Kind: ObjectSession, BaselineIndex: -1, BaselineIdentityID: base.LogicalID, LiveIdentityID: target.LogicalID, BaselineRuntimeID: base.TmuxID, LiveRuntimeID: target.TmuxID}
	sameGeneration := base.ServerKey != "" && base.ServerKey == live.ServerKey
	if target.LogicalID != "" {
		if base.LogicalID != target.LogicalID {
			return ObjectMatch{}, &Conflict{Baseline: base.Name, Live: []string{target.Name}, Reason: "baseline and live session have different logical IDs"}
		}
		if sameGeneration && base.TmuxID != "" && target.TmuxID != base.TmuxID {
			return ObjectMatch{}, &Conflict{Baseline: base.Name, Live: []string{target.TmuxID}, Reason: "logical session ID conflicts with runtime session ID in the same server generation"}
		}
		match.Evidence = []Evidence{{Class: EvidenceLogical, Signal: "session-logical-id", Value: base.LogicalID}}
		return match, nil
	}
	if sameGeneration && base.TmuxID != "" && base.TmuxID == target.TmuxID {
		match.Evidence = []Evidence{{Class: EvidenceRuntime, Signal: "session-id", Value: target.TmuxID}}
		return match, nil
	}
	return ObjectMatch{}, &Conflict{Baseline: base.Name, Live: []string{target.Name}, Reason: "session identity cannot be established from durable or same-generation runtime evidence"}
}

func resolveWindowAnchor(base model.Session, live model.Snapshot, index int, window model.Window) (ObjectMatch, *Conflict, bool) {
	sameGeneration := sameServer(&base, &live.Target)
	var tagged, runtime *model.ObjectIdentity
	if window.SlotID != "" {
		matches := objectsByStableID(live.Objects, ObjectWindow, window.SlotID)
		if len(matches) > 1 {
			return ObjectMatch{}, &Conflict{Baseline: windowLabel(window), Live: runtimeLabels(matches), Reason: "window slot ID is duplicated on the tmux server"}, false
		}
		if len(matches) == 1 {
			tagged = matches[0]
		}
	}
	if sameGeneration && window.TmuxID != "" {
		runtime = objectByRuntimeID(live.Objects, ObjectWindow, window.TmuxID)
	}
	if tagged != nil && runtime != nil && tagged.RuntimeID != runtime.RuntimeID {
		return ObjectMatch{}, &Conflict{Baseline: windowLabel(window), Live: []string{tagged.RuntimeID, runtime.RuntimeID}, Reason: "window slot and runtime bindings identify different objects"}, false
	}
	if tagged != nil {
		return windowObjectMatch(index, window, *tagged, live, Evidence{Class: EvidenceLogical, Signal: "window-slot-id", Value: window.SlotID}), nil, true
	}
	if runtime != nil {
		if runtime.SlotID != "" && runtime.SlotID != window.SlotID {
			return ObjectMatch{}, &Conflict{Baseline: windowLabel(window), Live: []string{runtime.RuntimeID}, Reason: "same runtime window ID carries a different slot ID"}, false
		}
		return windowObjectMatch(index, window, *runtime, live, Evidence{Class: EvidenceRuntime, Signal: "window-id", Value: window.TmuxID}), nil, true
	}
	return ObjectMatch{}, nil, false
}

func windowObjectMatch(index int, baseline model.Window, live model.ObjectIdentity, snapshot model.Snapshot, evidence Evidence) ObjectMatch {
	return ObjectMatch{Kind: ObjectWindow, BaselineIndex: index, BaselineIdentityID: baseline.SlotID, LiveIdentityID: live.SlotID, BaselineRuntimeID: baseline.TmuxID, LiveRuntimeID: live.RuntimeID, OutsideTarget: !isWindowMember(snapshot.Memberships, snapshot.TargetSessionID, live.RuntimeID), Evidence: []Evidence{evidence}}
}

func resolvePanes(base model.Session, live model.Snapshot, windowIndex int, liveWindow model.Window) (PaneResolution, []Ambiguity, []Conflict) {
	baselineWindow := base.Windows[windowIndex]
	var out PaneResolution
	var ambiguities []Ambiguity
	var conflicts []Conflict
	used := map[string]bool{}
	var semanticBase []int
	for index, pane := range baselineWindow.Panes {
		match, conflict, anchored := resolvePaneAnchor(base, live, baselineWindow, liveWindow, index, pane)
		if conflict != nil {
			conflicts = append(conflicts, *conflict)
			continue
		}
		if anchored {
			if used[match.LiveRuntimeID] {
				conflicts = append(conflicts, Conflict{Baseline: fmt.Sprintf("%s pane %d", windowLabel(baselineWindow), index), Live: []string{match.LiveRuntimeID}, Reason: "multiple baseline panes resolve to one live pane"})
				continue
			}
			used[match.LiveRuntimeID] = true
			out.Matches = append(out.Matches, match)
			continue
		}
		semanticBase = append(semanticBase, index)
	}
	var semanticLive []model.Pane
	for _, pane := range liveWindow.Panes {
		if !used[pane.TmuxID] && pane.SlotID == "" {
			semanticLive = append(semanticLive, pane)
		}
	}
	scores := make([][]scoreVector, len(semanticBase))
	allowed := make([][]bool, len(semanticBase))
	evidence := make([][]semanticEvidence, len(semanticBase))
	for row, baselineIndex := range semanticBase {
		scores[row] = make([]scoreVector, len(semanticLive))
		allowed[row] = make([]bool, len(semanticLive))
		evidence[row] = make([]semanticEvidence, len(semanticLive))
		for col, pane := range semanticLive {
			ev := semanticPaneEvidence(&base, baselineWindow.Panes[baselineIndex], &live.Target, pane)
			scores[row][col], allowed[row][col], evidence[row][col] = ev.score, ev.allowed, ev
		}
	}
	semanticUsed := map[string]bool{}
	if equivalentSemanticGroup(scores, allowed) {
		baseRows := append([]int(nil), semanticBase...)
		liveRows := append([]model.Pane(nil), semanticLive...)
		sort.Slice(baseRows, func(i, j int) bool {
			return baselineWindow.Panes[baseRows[i]].Idx < baselineWindow.Panes[baseRows[j]].Idx
		})
		sort.Slice(liveRows, func(i, j int) bool { return liveRows[i].TmuxID < liveRows[j].TmuxID })
		count := minInt(len(baseRows), len(liveRows))
		var baselineLabels, liveLabels []string
		for i := 0; i < count; i++ {
			bi, pane := baseRows[i], liveRows[i]
			baseline := baselineWindow.Panes[bi]
			baselineLabels = append(baselineLabels, fmt.Sprintf("pane[%d]", baseline.Idx))
			liveLabels = append(liveLabels, pane.TmuxID)
			semanticUsed[pane.TmuxID] = true
			out.Matches = append(out.Matches, ObjectMatch{Kind: ObjectPane, BaselineIndex: bi, BaselineIdentityID: baseline.SlotID, LiveIdentityID: pane.SlotID, BaselineRuntimeID: baseline.TmuxID, LiveRuntimeID: pane.TmuxID, Evidence: append(append([]Evidence(nil), evidence[0][0].items...), Evidence{Class: EvidenceSemantic, Signal: "equivalent-group", Value: "interchangeable panes in an identified window"})})
		}
		for _, missing := range baseRows[count:] {
			out.Missing = append(out.Missing, missing)
		}
		ambiguities = append(ambiguities, Ambiguity{Baseline: fmt.Sprintf("%s panes %s", windowLabel(baselineWindow), strings.Join(baselineLabels, ", ")), Live: liveLabels, Reason: "untagged panes share an interchangeable semantic signature; grouped without replacing processes", EquivalentGroup: true})
	} else {
		assignment, optimum := maxLexAssignment(scores, allowed)
		ambiguousBase := map[int]bool{}
		for row, col := range assignment {
			if col < 0 || col >= len(semanticLive) || !allowed[row][col] {
				continue
			}
			alternative := cloneAllowed(allowed)
			alternative[row][col] = false
			_, other := maxLexAssignment(scores, alternative)
			if other.equal(optimum) {
				labels := paneCandidates(evidence[row], semanticLive)
				ambiguities = append(ambiguities, Ambiguity{Baseline: fmt.Sprintf("%s pane %d", windowLabel(baselineWindow), semanticBase[row]), Live: labels, Reason: "multiple one-to-one pane assignments have equal evidence", Evidence: evidence[row][col].items})
				ambiguousBase[semanticBase[row]] = true
				for _, pane := range semanticLive {
					if contains(labels, pane.TmuxID) {
						semanticUsed[pane.TmuxID] = true
					}
				}
				continue
			}
			baseline, pane := baselineWindow.Panes[semanticBase[row]], semanticLive[col]
			semanticUsed[pane.TmuxID] = true
			out.Matches = append(out.Matches, ObjectMatch{Kind: ObjectPane, BaselineIndex: semanticBase[row], BaselineIdentityID: baseline.SlotID, LiveIdentityID: pane.SlotID, BaselineRuntimeID: baseline.TmuxID, LiveRuntimeID: pane.TmuxID, Evidence: evidence[row][col].items})
		}
		for row, baselineIndex := range semanticBase {
			if ambiguousBase[baselineIndex] || assignment[row] >= 0 && assignment[row] < len(semanticLive) && allowed[row][assignment[row]] {
				continue
			}
			var plausible []string
			var evidenceItems []Evidence
			for col, ev := range evidence[row] {
				if ev.plausible {
					plausible = append(plausible, semanticLive[col].TmuxID)
					evidenceItems = append(evidenceItems, ev.items...)
				}
			}
			if len(plausible) > 0 {
				ambiguities = append(ambiguities, Ambiguity{Baseline: fmt.Sprintf("%s pane %d", windowLabel(baselineWindow), baselineIndex), Live: uniqueStrings(plausible), Reason: "pane candidate evidence is insufficient for a safe match", Evidence: dedupeEvidence(evidenceItems)})
			} else {
				out.Missing = append(out.Missing, baselineIndex)
			}
		}
	}
	for _, pane := range liveWindow.Panes {
		if !used[pane.TmuxID] && !semanticUsed[pane.TmuxID] {
			out.Extra = append(out.Extra, pane.TmuxID)
		}
	}
	sort.Ints(out.Missing)
	sort.Strings(out.Extra)
	sort.Slice(out.Matches, func(i, j int) bool { return out.Matches[i].BaselineIndex < out.Matches[j].BaselineIndex })
	return out, ambiguities, conflicts
}

func resolvePaneReferencesOutside(base model.Session, live model.Snapshot, windowIndex int, windowID string) (PaneResolution, []Ambiguity, []Conflict) {
	want := base.Windows[windowIndex]
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
		if found == nil && sameServer(&base, &live.Target) {
			if ref, ok := refs[pane.TmuxID]; ok {
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
		out.Matches = append(out.Matches, ObjectMatch{Kind: ObjectPane, BaselineIndex: index, BaselineIdentityID: pane.SlotID, LiveIdentityID: found.SlotID, BaselineRuntimeID: pane.TmuxID, LiveRuntimeID: found.RuntimeID, Evidence: []Evidence{{Class: EvidenceRuntime, Signal: "pane-reference", Value: found.RuntimeID}}})
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

func resolvePaneAnchor(base model.Session, live model.Snapshot, baseWindow, liveWindow model.Window, index int, pane model.Pane) (ObjectMatch, *Conflict, bool) {
	sameGeneration := base.ServerKey != "" && base.ServerKey == live.ServerKey
	var tagged, runtime *model.ObjectIdentity
	if pane.SlotID != "" {
		matches := objectsByStableID(live.Objects, ObjectPane, pane.SlotID)
		if len(matches) > 1 {
			return ObjectMatch{}, &Conflict{Baseline: fmt.Sprintf("%s pane %d", windowLabel(baseWindow), index), Live: runtimeLabels(matches), Reason: "pane slot ID is duplicated on the tmux server"}, false
		}
		if len(matches) == 1 {
			tagged = matches[0]
		}
	}
	if sameGeneration && pane.TmuxID != "" {
		runtime = objectByRuntimeID(live.Objects, ObjectPane, pane.TmuxID)
	}
	if tagged != nil && runtime != nil && tagged.RuntimeID != runtime.RuntimeID {
		return ObjectMatch{}, &Conflict{Baseline: fmt.Sprintf("%s pane %d", windowLabel(baseWindow), index), Live: []string{tagged.RuntimeID, runtime.RuntimeID}, Reason: "pane slot and runtime bindings identify different objects"}, false
	}
	if tagged != nil {
		if tagged.ParentID != liveWindow.TmuxID {
			return ObjectMatch{}, &Conflict{Baseline: fmt.Sprintf("%s pane %d", windowLabel(baseWindow), index), Live: []string{tagged.RuntimeID}, Reason: "pane slot belongs to a different window"}, false
		}
		return paneObjectMatch(index, pane, *tagged, Evidence{Class: EvidenceLogical, Signal: "pane-slot-id", Value: pane.SlotID}), nil, true
	}
	if runtime != nil {
		if runtime.ParentID != liveWindow.TmuxID {
			return ObjectMatch{}, &Conflict{Baseline: fmt.Sprintf("%s pane %d", windowLabel(baseWindow), index), Live: []string{runtime.RuntimeID}, Reason: "same runtime pane ID belongs to a different window"}, false
		}
		if runtime.SlotID != "" && runtime.SlotID != pane.SlotID {
			return ObjectMatch{}, &Conflict{Baseline: fmt.Sprintf("%s pane %d", windowLabel(baseWindow), index), Live: []string{runtime.RuntimeID}, Reason: "same runtime pane ID carries a different slot ID"}, false
		}
		return paneObjectMatch(index, pane, *runtime, Evidence{Class: EvidenceRuntime, Signal: "pane-id", Value: pane.TmuxID}), nil, true
	}
	return ObjectMatch{}, nil, false
}

func paneObjectMatch(index int, baseline model.Pane, live model.ObjectIdentity, evidence Evidence) ObjectMatch {
	return ObjectMatch{Kind: ObjectPane, BaselineIndex: index, BaselineIdentityID: baseline.SlotID, LiveIdentityID: live.SlotID, BaselineRuntimeID: baseline.TmuxID, LiveRuntimeID: live.RuntimeID, Evidence: []Evidence{evidence}}
}

func validateBaselineIdentities(base model.Session) error {
	if base.SchemaVersion != model.CurrentBaselineSchemaVersion {
		return fmt.Errorf("baseline schema version %d is unsupported; freeze the session again", base.SchemaVersion)
	}
	if base.LogicalID == "" {
		return fmt.Errorf("baseline has no session identity")
	}
	if len(base.Windows) == 0 {
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
	if err := check(ObjectSession, base.TmuxID, base.LogicalID); err != nil {
		return err
	}
	for _, window := range base.Windows {
		if err := check(ObjectWindow, window.TmuxID, window.SlotID); err != nil {
			return err
		}
		if len(window.Panes) == 0 {
			return fmt.Errorf("baseline window %d has no panes", window.Idx)
		}
		for _, pane := range window.Panes {
			if err := check(ObjectPane, pane.TmuxID, pane.SlotID); err != nil {
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

func targetWindowByID(session model.Session, runtimeID string) (model.Window, bool) {
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

func cloneAllowed(src [][]bool) [][]bool {
	copy := make([][]bool, len(src))
	for i := range src {
		copy[i] = append([]bool(nil), src[i]...)
	}
	return copy
}

func semanticCandidates(row []semanticEvidence, live []model.Window) []string {
	var labels []string
	for i, evidence := range row {
		if evidence.allowed {
			labels = append(labels, windowCandidateLabel(live[i]))
		}
	}
	return uniqueStrings(labels)
}

func resolveEquivalentWindowGroup(r *Resolution, base model.Session, live model.Snapshot, baseRows []int, liveRows []model.Window, scores [][]scoreVector, allowed [][]bool, evidence [][]semanticEvidence, used map[string]bool) bool {
	if !equivalentSemanticGroup(scores, allowed) {
		return false
	}
	baseOrder := append([]int(nil), baseRows...)
	liveOrder := append([]model.Window(nil), liveRows...)
	sort.Slice(baseOrder, func(i, j int) bool {
		a, b := base.Windows[baseOrder[i]], base.Windows[baseOrder[j]]
		if a.Idx != b.Idx {
			return a.Idx < b.Idx
		}
		if a.SlotID != b.SlotID {
			return a.SlotID < b.SlotID
		}
		return a.Name < b.Name
	})
	sort.Slice(liveOrder, func(i, j int) bool { return liveOrder[i].TmuxID < liveOrder[j].TmuxID })
	count := minInt(len(baseOrder), len(liveOrder))
	var baselineLabels, liveLabels []string
	var groupEvidence []Evidence
	for i := 0; i < count; i++ {
		bi, lw := baseOrder[i], liveOrder[i]
		baseRow, liveCol := -1, -1
		for row, index := range baseRows {
			if index == bi {
				baseRow = row
				break
			}
		}
		for col, candidate := range liveRows {
			if candidate.TmuxID == lw.TmuxID {
				liveCol = col
				break
			}
		}
		if baseRow < 0 || liveCol < 0 {
			return false
		}
		semantic := evidence[baseRow][liveCol]
		baselineLabels = append(baselineLabels, windowLabel(base.Windows[bi]))
		liveLabels = append(liveLabels, windowCandidateLabel(lw))
		items := append([]Evidence(nil), semantic.items...)
		items = append(items, Evidence{Class: EvidenceSemantic, Signal: "equivalent-group", Value: "interchangeable untagged live window signature"})
		match := ObjectMatch{Kind: ObjectWindow, BaselineIndex: bi, BaselineIdentityID: base.Windows[bi].SlotID, LiveIdentityID: lw.SlotID, BaselineRuntimeID: base.Windows[bi].TmuxID, LiveRuntimeID: lw.TmuxID, Evidence: items}
		panes, paneAmbiguous, paneConflicts := resolvePanes(base, live, bi, lw)
		r.Windows = append(r.Windows, WindowResolution{Match: match, Panes: panes})
		r.Ambiguous = append(r.Ambiguous, paneAmbiguous...)
		r.Conflicts = append(r.Conflicts, paneConflicts...)
		used[lw.TmuxID] = true
		groupEvidence = append(groupEvidence, semantic.items...)
	}
	for _, bi := range baseOrder[count:] {
		r.Missing = append(r.Missing, bi)
	}
	r.Ambiguous = append(r.Ambiguous, Ambiguity{Baseline: strings.Join(baselineLabels, ", "), Live: liveLabels, Reason: "untagged live windows share an interchangeable semantic signature; grouped without replacing processes", Evidence: dedupeEvidence(groupEvidence), EquivalentGroup: true})
	return true
}

func windowCandidateLabel(window model.Window) string {
	return fmt.Sprintf("%s[%s]", windowLabel(window), window.TmuxID)
}

func paneCandidates(row []semanticEvidence, live []model.Pane) []string {
	var ids []string
	for i, evidence := range row {
		if evidence.allowed {
			ids = append(ids, live[i].TmuxID)
		}
	}
	return uniqueStrings(ids)
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func dedupeEvidence(values []Evidence) []Evidence {
	seen := map[string]bool{}
	var out []Evidence
	for _, value := range values {
		key := fmt.Sprintf("%d\x00%s\x00%s", value.Class, value.Signal, value.Value)
		if !seen[key] {
			seen[key] = true
			out = append(out, value)
		}
	}
	return out
}

func (r *Resolution) addConflict(baseline string, live []string, reason string) {
	r.Conflicts = append(r.Conflicts, Conflict{Baseline: baseline, Live: live, Reason: reason})
	r.Status = ResolutionConflict
}

func sameServer(a, b *model.Session) bool {
	return a != nil && b != nil && a.ServerKey != "" && a.ServerKey == b.ServerKey
}

func windowLabel(window model.Window) string {
	if window.Name != "" {
		return fmt.Sprintf("%s@index-%d", window.Name, window.Idx)
	}
	return fmt.Sprintf("window@index-%d", window.Idx)
}
