// Package reconcile contains the pure matching and planning logic for bringing
// a live tmux session toward a recorded baseline.
package reconcile

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fm39hz/gotomux/internal/classify"
	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/project"
	"github.com/fm39hz/gotomux/internal/tmux"
)

type WindowMatch struct {
	BaseIndex int
	LiveIndex int
	Score     int
	ByRuntime bool
	PaneMap   []PaneMatch
}

type PaneMatch struct {
	BaseIndex int
	LiveIndex int
	Score     int
	ByRuntime bool
}

type Ambiguity struct {
	Baseline string
	Live     []string
	Reason   string
}

type Move struct {
	WindowID string
	From     int
	To       int
	Expected model.Window
}

type CreateWindow struct {
	Index  int
	Window model.Window
}

type RepairWindow struct {
	Index             int
	WindowID          string
	WindowCwd         string
	ExpectedName      string
	ExpectedLayout    string
	ExpectedPaneIDs   []string
	ExpectedPaneCount int
	MissingPanes      []model.Pane
	ExtraPanes        int
	RenameTo          string
	Layout            string
}

type Plan struct {
	ServerKey      string
	Moves          []Move
	Creates        []CreateWindow
	Repairs        []RepairWindow
	Extras         []Move
	Ambiguous      []Ambiguity
	ActiveTo       int
	ActiveSet      bool
	ActiveWindowID string
	Matched        []WindowMatch
	Missing        []int
	Extra          []int
	ExtraPanes     int
	NeedsApply     bool
}

type Executor interface {
	Freeze(ctx context.Context, name string) (*model.Session, error)
	MoveWindowTo(ctx context.Context, session, windowID string, from, to int) error
	NewWindowAt(ctx context.Context, session string, idx int, w model.Window, sessCwd string) error
	KillWindowAt(ctx context.Context, session, windowID string, idx int) error
	AddPaneToWindow(ctx context.Context, session, windowID string, idx int, p model.Pane) error
	RenameWindowAt(ctx context.Context, session, windowID string, idx int, name string) error
	ApplyLayoutAt(ctx context.Context, session, windowID string, idx int, layout string) error
	ShowOption(ctx context.Context, session, name string) (string, error)
	SetOption(ctx context.Context, session, name, value string) error
	UnsetOption(ctx context.Context, session, name string) error
	SelectWindow(ctx context.Context, session string, idx int) error
}

func Apply(ctx context.Context, ops Executor, session string, sessCwd string, p Plan) (report string, retErr error) {
	if len(p.Ambiguous) > 0 {
		return ambiguityReport(p), nil
	}
	if !p.NeedsApply {
		if len(p.Extra) > 0 || p.ExtraPanes > 0 {
			return fmt.Sprintf("already aligned; preserved %d extra window(s) and %d extra pane(s)", len(p.Extra), p.ExtraPanes), nil
		}
		return "", nil
	}
	if p.ServerKey == "" {
		return "", fmt.Errorf("reconcile cannot verify tmux server identity; freeze again after restarting tmux")
	}
	if err := validatePlannedPaths(p, sessCwd); err != nil {
		return "", err
	}
	needsRenumberControl := len(p.Moves)+len(p.Extras)+len(p.Creates) > 0
	if needsRenumberControl {
		restoreOption, err := disableRenumber(ctx, ops, session)
		if err != nil {
			return "", err
		}
		defer func() { retErr = combineRestoreError(retErr, restoreOption()) }()
	}
	for _, move := range p.Moves {
		before, err := observe(ctx, ops, session, p.ServerKey)
		if err != nil {
			return "", err
		}
		w, ok := findWindow(before, move.WindowID, move.From)
		if !ok {
			return "", fmt.Errorf("planned source window at index %d disappeared", move.From)
		}
		if err := verifyWindowSnapshot(w, move.Expected, move.From); err != nil {
			return "", fmt.Errorf("planned source window %d changed: %w", move.From, err)
		}
		if indexOccupied(before, move.To, move.WindowID) {
			return "", fmt.Errorf("planned target window index %d is occupied", move.To)
		}
		if err := ops.MoveWindowTo(ctx, session, move.WindowID, move.From, move.To); err != nil {
			return "", fmt.Errorf("stage window %d -> %d: %w", move.From, move.To, err)
		}
		after, err := observe(ctx, ops, session, p.ServerKey)
		if err != nil {
			return "", err
		}
		w, ok = findWindow(after, move.WindowID, move.To)
		if !ok {
			return "", fmt.Errorf("window did not arrive at planned index %d", move.To)
		}
		if err := verifyWindowSnapshot(w, move.Expected, move.To); err != nil {
			return "", fmt.Errorf("window move verification failed: %w", err)
		}
	}
	for _, create := range p.Creates {
		before, err := observe(ctx, ops, session, p.ServerKey)
		if err != nil {
			return "", err
		}
		if err := expectedWindowAt(before, create.Index); err != nil {
			return "", err
		}
		if err := ops.NewWindowAt(ctx, session, create.Index, create.Window, sessCwd); err != nil {
			return "", fmt.Errorf("recreate window %d: %w", create.Index, err)
		}
		after, err := observe(ctx, ops, session, p.ServerKey)
		if err != nil {
			return "", err
		}
		if err := verifyCreatedWindow(after, create.Index, create.Window, session); err != nil {
			return "", fmt.Errorf("recreated window verification failed: %w", err)
		}
	}
	for _, repair := range p.Repairs {
		expectedName := repair.ExpectedName
		expectedLayout := repair.ExpectedLayout
		expectedIDs := append([]string(nil), repair.ExpectedPaneIDs...)
		expectedCount := repair.ExpectedPaneCount
		for _, pane := range repair.MissingPanes {
			before, err := observe(ctx, ops, session, p.ServerKey)
			if err != nil {
				return "", err
			}
			w, ok := findWindow(before, repair.WindowID, repair.Index)
			if !ok || w.Idx != repair.Index || (expectedName != "" && w.Name != expectedName) || len(w.Panes) != expectedCount || !hasPaneIDs(w, expectedIDs) || !layoutEquivalent(expectedLayout, expectedCount, w.Layout, len(w.Panes)) {
				return "", fmt.Errorf("planned repair window %d changed before adding a pane", repair.Index)
			}
			if pane.Cwd == "" {
				pane.Cwd = repair.WindowCwd
				if pane.Cwd == "" {
					pane.Cwd = sessCwd
				}
			}
			if err := ops.AddPaneToWindow(ctx, session, repair.WindowID, repair.Index, pane); err != nil {
				return "", fmt.Errorf("recreate pane in window %d: %w", repair.Index, err)
			}
			after, err := observe(ctx, ops, session, p.ServerKey)
			if err != nil {
				return "", err
			}
			w, ok = findWindow(after, repair.WindowID, repair.Index)
			if !ok || w.Idx != repair.Index || (expectedName != "" && w.Name != expectedName) || len(w.Panes) != expectedCount+1 || !hasPaneIDs(w, expectedIDs) {
				return "", fmt.Errorf("new pane in window %d did not appear while preserving its existing panes", repair.Index)
			}
			expectedCount++
			expectedIDs = paneIDs(w)
			expectedLayout = w.Layout
		}
		if repair.RenameTo != "" {
			before, err := observe(ctx, ops, session, p.ServerKey)
			if err != nil {
				return "", err
			}
			w, ok := findWindow(before, repair.WindowID, repair.Index)
			if !ok || w.Idx != repair.Index || (expectedName != "" && w.Name != expectedName) || len(w.Panes) != expectedCount || !hasPaneIDs(w, expectedIDs) {
				return "", fmt.Errorf("planned repair window %d changed before rename", repair.Index)
			}
			if err := ops.RenameWindowAt(ctx, session, repair.WindowID, repair.Index, repair.RenameTo); err != nil {
				return "", fmt.Errorf("rename window %d: %w", repair.Index, err)
			}
			after, err := observe(ctx, ops, session, p.ServerKey)
			if err != nil {
				return "", err
			}
			w, ok = findWindow(after, repair.WindowID, repair.Index)
			if !ok || w.Name != repair.RenameTo || len(w.Panes) != expectedCount || !hasPaneIDs(w, expectedIDs) {
				return "", fmt.Errorf("window %d rename was not observed", repair.Index)
			}
			expectedName = repair.RenameTo
		}
		if repair.Layout != "" {
			before, err := observe(ctx, ops, session, p.ServerKey)
			if err != nil {
				return "", err
			}
			w, ok := findWindow(before, repair.WindowID, repair.Index)
			if !ok || w.Idx != repair.Index || (expectedName != "" && w.Name != expectedName) || len(w.Panes) != expectedCount || !hasPaneIDs(w, expectedIDs) {
				return "", fmt.Errorf("planned repair window %d changed before layout update", repair.Index)
			}
			if err := ops.ApplyLayoutAt(ctx, session, repair.WindowID, repair.Index, repair.Layout); err != nil {
				return "", fmt.Errorf("repair layout for window %d: %w", repair.Index, err)
			}
			after, err := observe(ctx, ops, session, p.ServerKey)
			if err != nil {
				return "", err
			}
			w, ok = findWindow(after, repair.WindowID, repair.Index)
			if !ok || w.Idx != repair.Index || (expectedName != "" && w.Name != expectedName) || len(w.Panes) != expectedCount || !hasPaneIDs(w, expectedIDs) || !layoutEquivalent(repair.Layout, expectedCount, w.Layout, len(w.Panes)) {
				return "", fmt.Errorf("window %d split topology was not restored", repair.Index)
			}
		}
	}
	for _, move := range p.Extras {
		before, err := observe(ctx, ops, session, p.ServerKey)
		if err != nil {
			return "", err
		}
		w, ok := findWindow(before, move.WindowID, move.From)
		if !ok || verifyWindowSnapshot(w, move.Expected, move.From) != nil || indexOccupied(before, move.To, move.WindowID) {
			return "", fmt.Errorf("extra window changed before preservation move")
		}
		if err := ops.MoveWindowTo(ctx, session, move.WindowID, move.From, move.To); err != nil {
			return "", fmt.Errorf("preserve extra window at %d: %w", move.From, err)
		}
		after, err := observe(ctx, ops, session, p.ServerKey)
		if err != nil {
			return "", err
		}
		w, ok = findWindow(after, move.WindowID, move.To)
		if !ok || verifyWindowSnapshot(w, move.Expected, move.To) != nil {
			return "", fmt.Errorf("extra window did not arrive at its preservation index")
		}
	}
	if p.ActiveSet {
		state, err := observe(ctx, ops, session, p.ServerKey)
		if err != nil {
			return "", err
		}
		if _, ok := findWindow(state, p.ActiveWindowID, p.ActiveTo); !ok {
			return "", fmt.Errorf("active window target %d is missing", p.ActiveTo)
		}
		_ = ops.SelectWindow(ctx, session, p.ActiveTo)
	}
	moved := map[string]bool{}
	for _, move := range p.Moves {
		moved[fmt.Sprintf("%s:%d", move.WindowID, move.From)] = true
	}
	for _, move := range p.Extras {
		moved[fmt.Sprintf("%s:%d", move.WindowID, move.From)] = true
	}
	return fmt.Sprintf("reconciled: %d window move(s), %d recreated, %d repaired, %d extra window(s) and %d extra pane(s) preserved",
		len(moved), len(p.Creates), len(p.Repairs), len(p.Extra), p.ExtraPanes), nil
}

func ambiguityReport(p Plan) string {
	lines := []string{"reconcile skipped: identity is ambiguous"}
	for _, a := range p.Ambiguous {
		lines = append(lines, fmt.Sprintf("  %s: %s (%s)", a.Baseline, strings.Join(a.Live, ", "), a.Reason))
	}
	return strings.Join(lines, "\n")
}

// Build computes a one-to-one maximum-weight assignment. A name or index can
// contribute to a score, but neither can make an edge eligible on its own.
func Build(base, live *model.Session, activeLive int) Plan {
	p := Plan{ActiveTo: -1}
	if base == nil || live == nil || len(base.Windows) == 0 {
		return p
	}
	p.ServerKey = live.ServerKey
	ctx := projectContext(base)
	weights := make([][]int, len(base.Windows))
	allowed := make([][]bool, len(base.Windows))
	edges := make([][]edgeEvidence, len(base.Windows))
	for bi, bw := range base.Windows {
		weights[bi] = make([]int, len(live.Windows))
		allowed[bi] = make([]bool, len(live.Windows))
		edges[bi] = make([]edgeEvidence, len(live.Windows))
		for li, lw := range live.Windows {
			e := scoreWindow(base, bw, live, lw, ctx)
			edges[bi][li] = e
			if e.allowed {
				weights[bi][li], allowed[bi][li] = e.score, true
			} else {
				weights[bi][li] = forbiddenWeight
			}
		}
	}

	assignment, optimum := maxAssignment(weights)
	baseToLive := make([]int, len(base.Windows))
	liveUsed := make([]bool, len(live.Windows))
	for bi, li := range assignment {
		if li < 0 || li >= len(live.Windows) || !allowed[bi][li] {
			baseToLive[bi] = -1
			continue
		}
		// If excluding this edge leaves the global optimum unchanged, more than
		// one complete identity assignment explains the observation. Do not let
		// an arbitrary tie decide which process gets renamed or moved.
		alternate := cloneMatrix(weights)
		alternate[bi][li] = forbiddenWeight
		_, alternateScore := maxAssignment(alternate)
		if alternateScore == optimum && !equivalentWindows(base.Windows[bi], live.Windows[li], ctx) {
			p.Ambiguous = append(p.Ambiguous, Ambiguity{
				Baseline: windowLabel(base.Windows[bi]),
				Live:     competingLabels(edges[bi], live, li),
				Reason:   "multiple one-to-one assignments have equal evidence",
			})
			baseToLive[bi] = -1
			continue
		}
		baseToLive[bi] = li
		liveUsed[li] = true
		paneMap, paneAmbiguous := alignPanes(base, base.Windows[bi], live, live.Windows[li], ctx)
		if paneAmbiguous {
			p.Ambiguous = append(p.Ambiguous, Ambiguity{
				Baseline: windowLabel(base.Windows[bi]),
				Live:     []string{windowLabel(live.Windows[li])},
				Reason:   "pane identity is ambiguous inside the matched window",
			})
		}
		p.Matched = append(p.Matched, WindowMatch{
			BaseIndex: bi, LiveIndex: li, Score: edges[bi][li].score,
			ByRuntime: edges[bi][li].runtime, PaneMap: paneMap,
		})
	}
	for bi, li := range baseToLive {
		if li >= 0 {
			continue
		}
		bw := base.Windows[bi]
		for liveIdx, used := range liveUsed {
			if used || !plausibleWindowCandidate(base, bw, live, live.Windows[liveIdx], ctx) {
				continue
			}
			p.Ambiguous = append(p.Ambiguous, Ambiguity{
				Baseline: windowLabel(bw), Live: []string{windowLabel(live.Windows[liveIdx])},
				Reason: "unmatched windows share enough structure that identity cannot be decided",
			})
		}
	}

	for bi, li := range baseToLive {
		if li < 0 {
			p.Missing = append(p.Missing, bi)
		}
	}
	for li, used := range liveUsed {
		if !used {
			p.Extra = append(p.Extra, li)
		}
	}

	// Ambiguity suspends the whole plan: otherwise an unresolved window might
	// occupy a target index that another operation is about to claim.
	if len(p.Ambiguous) > 0 {
		return p
	}

	maxBase := maxWindowIndex(base.Windows)
	maxLive := maxWindowIndex(live.Windows)
	needsWindowPlacement := len(p.Missing) > 0
	for _, m := range p.Matched {
		if base.Windows[m.BaseIndex].Idx != live.Windows[m.LiveIndex].Idx {
			needsWindowPlacement = true
		}
	}
	for _, li := range p.Extra {
		if live.Windows[li].Idx <= maxBase {
			needsWindowPlacement = true
		}
	}

	staged := make(map[int]int)
	if needsWindowPlacement {
		next := maxLive + 1
		if maxBase+1 > next {
			next = maxBase + 1
		}
		for li, w := range live.Windows {
			if w.Idx > maxBase {
				continue
			}
			staged[li] = next
			expected := w
			p.Moves = append(p.Moves, Move{WindowID: w.TmuxID, From: w.Idx, To: next, Expected: expected})
			next++
		}
	}

	for _, m := range p.Matched {
		bw, lw := base.Windows[m.BaseIndex], live.Windows[m.LiveIndex]
		from := lw.Idx
		if n, ok := staged[m.LiveIndex]; ok {
			from = n
		}
		if needsWindowPlacement && from != bw.Idx {
			expected := lw
			expected.Idx = from
			p.Moves = append(p.Moves, Move{WindowID: lw.TmuxID, From: from, To: bw.Idx, Expected: expected})
		}
		if from == activeLive || lw.Idx == activeLive {
			p.ActiveTo, p.ActiveSet, p.ActiveWindowID = bw.Idx, true, lw.TmuxID
		}

		repair := RepairWindow{
			Index: bw.Idx, WindowID: lw.TmuxID, WindowCwd: bw.Cwd,
			ExpectedName: lw.Name, ExpectedLayout: lw.Layout,
			ExpectedPaneCount: len(lw.Panes),
		}
		for _, pane := range lw.Panes {
			repair.ExpectedPaneIDs = append(repair.ExpectedPaneIDs, pane.TmuxID)
		}
		if len(lw.Panes) > len(bw.Panes) {
			repair.ExtraPanes = len(lw.Panes) - len(bw.Panes)
			p.ExtraPanes += repair.ExtraPanes
		}
		if name := tmux.SafeWindowName(bw.Name, base.Name); name != "" && name != lw.Name {
			repair.RenameTo = name
		}
		if len(lw.Panes) < len(bw.Panes) {
			missing, ambiguous := missingPanes(base, bw, live, lw, ctx)
			if ambiguous {
				p.Ambiguous = append(p.Ambiguous, Ambiguity{
					Baseline: windowLabel(bw), Live: []string{windowLabel(lw)},
					Reason: "cannot tell which baseline pane disappeared",
				})
			} else {
				repair.MissingPanes = missing
			}
		}
		baseLayout := tmux.LayoutForShape(bw.Layout, len(bw.Panes))
		if !layoutEquivalent(bw.Layout, len(bw.Panes), lw.Layout, len(lw.Panes)) && len(lw.Panes) <= len(bw.Panes) {
			if tmux.IsLayoutDump(bw.Layout) {
				repair.Layout = bw.Layout
			} else {
				repair.Layout = baseLayout
			}
		}
		if repair.RenameTo != "" || len(repair.MissingPanes) > 0 || repair.Layout != "" {
			p.Repairs = append(p.Repairs, repair)
		}
	}

	// Unmatched live windows inside the baseline index range were staged to
	// free occupied slots. Keep them after the baseline area.
	tail := maxBase + 1
	if maxLive+1 > tail {
		tail = maxLive + 1
	}
	for _, li := range p.Extra {
		lw := live.Windows[li]
		if stage, ok := staged[li]; ok {
			if stage != tail {
				expected := lw
				expected.Idx = stage
				p.Extras = append(p.Extras, Move{WindowID: lw.TmuxID, From: stage, To: tail, Expected: expected})
			}
			if lw.Idx == activeLive {
				p.ActiveTo, p.ActiveSet, p.ActiveWindowID = tail, true, lw.TmuxID
			}
			tail++
		}
	}
	for _, bi := range p.Missing {
		p.Creates = append(p.Creates, CreateWindow{Index: base.Windows[bi].Idx, Window: base.Windows[bi]})
	}
	p.NeedsApply = len(p.Moves)+len(p.Extras)+len(p.Creates)+len(p.Repairs) > 0
	return p
}

// RefreshBindings carries the stable tmux identifiers from a post-apply
// observation into the unchanged baseline semantics.
func RefreshBindings(base, live *model.Session) *model.Session {
	if base == nil || live == nil {
		return base
	}
	copy := cloneSession(base)
	copy.ServerKey = live.ServerKey
	for bi := range copy.Windows {
		var lw *model.Window
		for li := range live.Windows {
			if live.Windows[li].Idx == copy.Windows[bi].Idx {
				lw = &live.Windows[li]
				break
			}
		}
		if lw == nil {
			continue
		}
		bw := &copy.Windows[bi]
		bw.TmuxID = lw.TmuxID
		matches, _ := alignPanes(copy, *bw, live, *lw, projectContext(copy))
		baseUsed, liveUsed := make([]bool, len(bw.Panes)), make([]bool, len(lw.Panes))
		for _, m := range matches {
			baseUsed[m.BaseIndex], liveUsed[m.LiveIndex] = true, true
			bw.Panes[m.BaseIndex].TmuxID = lw.Panes[m.LiveIndex].TmuxID
		}
		var leftBase, leftLive []int
		for i, used := range baseUsed {
			if !used {
				leftBase = append(leftBase, i)
			}
		}
		for i, used := range liveUsed {
			if !used {
				leftLive = append(leftLive, i)
			}
		}
		for i := 0; i < len(leftBase) && i < len(leftLive); i++ {
			bw.Panes[leftBase[i]].TmuxID = lw.Panes[leftLive[i]].TmuxID
		}
	}
	copy.SchemaVersion = 2
	return copy
}

func cloneSession(s *model.Session) *model.Session {
	copy := *s
	copy.Windows = append([]model.Window(nil), s.Windows...)
	for i := range copy.Windows {
		copy.Windows[i].Panes = append([]model.Pane(nil), s.Windows[i].Panes...)
	}
	return &copy
}

type edgeEvidence struct {
	score   int
	allowed bool
	runtime bool
}

const forbiddenWeight = -1_000_000

func projectContext(s *model.Session) classify.ProjectContext {
	if s == nil {
		return classify.ProjectContext{}
	}
	return classify.ProjectContext{Root: s.Cwd, Children: project.Children(s.Cwd)}
}

func scoreWindow(base *model.Session, bw model.Window, live *model.Session, lw model.Window, ctx classify.ProjectContext) edgeEvidence {
	if sameServer(base, live) && bw.TmuxID != "" && lw.TmuxID != "" {
		if bw.TmuxID == lw.TmuxID {
			return edgeEvidence{score: 100_000, allowed: true, runtime: true}
		}
		// tmux window IDs are unique inside a server generation. A different
		// ID proves this is another window even if its visible shape is similar.
		return edgeEvidence{score: forbiddenWeight}
	}
	paneWeights := make([][]int, len(bw.Panes))
	hasEvidence := false
	for bi, bp := range bw.Panes {
		paneWeights[bi] = make([]int, len(lw.Panes))
		for li, lp := range lw.Panes {
			score, _ := paneScore(base, bp, live, lp, ctx)
			paneWeights[bi][li] = score
		}
	}
	paneMap, paneScoreTotal := maxAssignment(paneWeights)
	for bi, li := range paneMap {
		if li >= 0 && li < len(lw.Panes) {
			_, evidence := paneScore(base, bw.Panes[bi], live, lw.Panes[li], ctx)
			hasEvidence = hasEvidence || evidence
		}
	}
	score := paneScoreTotal
	if len(bw.Panes) == len(lw.Panes) {
		score += 2
	} else if abs(len(bw.Panes)-len(lw.Panes)) == 1 {
		score++
	}
	if hasLayoutEvidence(bw.Layout, len(bw.Panes), lw.Layout, len(lw.Panes)) && layoutEquivalent(bw.Layout, len(bw.Panes), lw.Layout, len(lw.Panes)) {
		score++
	}
	if bw.Name != "" && bw.Name == lw.Name {
		score++
	}
	baseScope, liveScope := classify.StableScopeKey(bw.Cwd, ctx), classify.StableScopeKey(lw.Cwd, ctx)
	if baseScope != "" && baseScope == liveScope {
		score++
		hasEvidence = true
	} else if (baseScope == "" || liveScope == "") && bw.Cwd != "" && bw.Cwd == lw.Cwd {
		score++
		hasEvidence = true
	}
	allShell := onlyShells(bw) && onlyShells(lw)
	shellTopology := allShell && len(bw.Panes) == len(lw.Panes) && layoutEquivalent(bw.Layout, len(bw.Panes), lw.Layout, len(lw.Panes))
	allowed := hasEvidence || shellTopology || (bw.Name != "" && bw.Name == lw.Name && len(bw.Panes) == len(lw.Panes) && layoutEquivalent(bw.Layout, len(bw.Panes), lw.Layout, len(lw.Panes)) && (bw.Cwd == lw.Cwd || (bw.Cwd != "" && stableEqual(bw.Cwd, lw.Cwd, ctx))))
	return edgeEvidence{score: score, allowed: allowed && score >= 2}
}

func paneScore(base *model.Session, bp model.Pane, live *model.Session, lp model.Pane, ctx classify.ProjectContext) (int, bool) {
	if sameServer(base, live) && bp.TmuxID != "" && bp.TmuxID == lp.TmuxID {
		return 100_000, true
	}
	bt := classify.ClassifyPane(bp.Cmd, bp.Cwd, ctx).Tool
	lt := classify.ClassifyPane(lp.Cmd, lp.Cwd, ctx).Tool
	score, evidence := 0, false
	switch {
	case bt != "" && bt == lt:
		score, evidence = score+2, true
	case bt != "" && lt != "" && bt != lt:
		score -= 2
	}
	bs, ls := classify.StableScopeKey(bp.Cwd, ctx), classify.StableScopeKey(lp.Cwd, ctx)
	if bs != "" && bs == ls {
		score, evidence = score+1, true
	} else if (bs == "" || ls == "") && bp.Cwd != "" && bp.Cwd == lp.Cwd {
		score, evidence = score+1, true
	}
	if bp.CmdPath != "" && bp.CmdPath == lp.CmdPath {
		score, evidence = score+2, true
	}
	return score, evidence
}

func hasLayoutEvidence(a string, aPanes int, b string, bPanes int) bool {
	if tmux.IsLayoutDump(a) && tmux.IsLayoutDump(b) {
		return true
	}
	return tmux.LayoutForShape(a, aPanes) != "" && tmux.LayoutForShape(a, aPanes) == tmux.LayoutForShape(b, bPanes)
}

func alignPanes(base *model.Session, bw model.Window, live *model.Session, lw model.Window, ctx classify.ProjectContext) ([]PaneMatch, bool) {
	weights := make([][]int, len(bw.Panes))
	for i, bp := range bw.Panes {
		weights[i] = make([]int, len(lw.Panes))
		for j, lp := range lw.Panes {
			weights[i][j], _ = paneScore(base, bp, live, lp, ctx)
		}
	}
	assign, optimum := maxAssignment(weights)
	out := make([]PaneMatch, 0, len(assign))
	for bi, li := range assign {
		if li < 0 || li >= len(lw.Panes) || weights[bi][li] <= 0 {
			continue
		}
		alt := cloneMatrix(weights)
		alt[bi][li] = forbiddenWeight
		_, altScore := maxAssignment(alt)
		if altScore == optimum && !equivalentPanes(bw.Panes[bi], lw.Panes[li]) {
			return out, true
		}
		out = append(out, PaneMatch{BaseIndex: bi, LiveIndex: li, Score: weights[bi][li], ByRuntime: sameServer(base, live) && bw.Panes[bi].TmuxID != "" && bw.Panes[bi].TmuxID == lw.Panes[li].TmuxID})
	}
	return out, false
}

func missingPanes(base *model.Session, bw model.Window, live *model.Session, lw model.Window, ctx classify.ProjectContext) ([]model.Pane, bool) {
	matches, ambiguous := alignPanes(base, bw, live, lw, ctx)
	if ambiguous {
		return nil, true
	}
	used := make([]bool, len(bw.Panes))
	for _, m := range matches {
		used[m.BaseIndex] = true
	}
	var missing []model.Pane
	for i, pane := range bw.Panes {
		if !used[i] {
			missing = append(missing, pane)
		}
	}
	if len(missing) != len(bw.Panes)-len(lw.Panes) {
		return nil, true
	}
	return missing, false
}

func equivalentWindows(a, b model.Window, ctx classify.ProjectContext) bool {
	if a.Name != b.Name || len(a.Panes) != len(b.Panes) || !layoutEquivalent(a.Layout, len(a.Panes), b.Layout, len(b.Panes)) {
		return false
	}
	if sa, sb := classify.StableScopeKey(a.Cwd, ctx), classify.StableScopeKey(b.Cwd, ctx); sa != sb {
		return false
	}
	for i := range a.Panes {
		if !equivalentPanes(a.Panes[i], b.Panes[i]) {
			return false
		}
	}
	return true
}

func equivalentPanes(a, b model.Pane) bool {
	toolA := classify.ClassifyPane(a.Cmd, "", classify.ProjectContext{}).Tool
	toolB := classify.ClassifyPane(b.Cmd, "", classify.ProjectContext{}).Tool
	return toolA == toolB && a.Cwd == b.Cwd && a.CmdPath == b.CmdPath && a.StartCmd == b.StartCmd
}

func onlyShells(w model.Window) bool {
	for _, p := range w.Panes {
		if classify.ClassifyPane(p.Cmd, p.Cwd, classify.ProjectContext{}).Tool != "" {
			return false
		}
	}
	return true
}

func stableEqual(a, b string, ctx classify.ProjectContext) bool {
	return classify.StableScopeKey(a, ctx) != "" && classify.StableScopeKey(a, ctx) == classify.StableScopeKey(b, ctx)
}

func plausibleWindowCandidate(base *model.Session, bw model.Window, live *model.Session, lw model.Window, ctx classify.ProjectContext) bool {
	if sameServer(base, live) && bw.TmuxID != "" && lw.TmuxID != "" {
		return false
	}
	if abs(len(bw.Panes)-len(lw.Panes)) > 1 || tmux.LayoutForShape(bw.Layout, len(bw.Panes)) != tmux.LayoutForShape(lw.Layout, len(lw.Panes)) {
		return false
	}
	name := bw.Name != "" && bw.Name == lw.Name
	index := bw.Idx == lw.Idx
	scope := stableEqual(bw.Cwd, lw.Cwd, ctx)
	return (name || index || scope) && (layoutEquivalent(bw.Layout, len(bw.Panes), lw.Layout, len(lw.Panes)) || len(bw.Panes) != len(lw.Panes))
}

func sameServer(a, b *model.Session) bool {
	return a != nil && b != nil && a.ServerKey != "" && a.ServerKey == b.ServerKey
}

// assignment returns a maximum-weight one-to-one assignment. Dummy columns
// represent unmatched baseline rows; dummy rows represent live extras.
func assignment(w [][]int) ([]int, int) { return maxAssignment(w) }

func maxAssignment(weights [][]int) ([]int, int) {
	n, m := len(weights), 0
	for _, row := range weights {
		if len(row) > m {
			m = len(row)
		}
	}
	size := n + m // real rows/columns plus one dummy for every side
	if size == 0 {
		return nil, 0
	}
	cost := make([][]int, size+1)
	for i := 0; i <= size; i++ {
		cost[i] = make([]int, size+1)
	}
	for i := 0; i < n; i++ {
		for j := 0; j < m; j++ {
			weight := weights[i][j]
			if weight == forbiddenWeight {
				cost[i+1][j+1] = 1_000_000
			} else {
				cost[i+1][j+1] = -weight
			}
		}
	}
	// Hungarian algorithm for a square minimum-cost assignment.
	u, v := make([]int, size+1), make([]int, size+1)
	p, way := make([]int, size+1), make([]int, size+1)
	for i := 1; i <= size; i++ {
		p[0] = i
		j0 := 0
		minv := make([]int, size+1)
		used := make([]bool, size+1)
		for j := 1; j <= size; j++ {
			minv[j] = 1_000_000_000
		}
		for {
			used[j0] = true
			i0, delta, j1 := p[j0], 1_000_000_000, 0
			for j := 1; j <= size; j++ {
				if used[j] {
					continue
				}
				cur := cost[i0][j] - u[i0] - v[j]
				if cur < minv[j] {
					minv[j], way[j] = cur, j0
				}
				if minv[j] < delta {
					delta, j1 = minv[j], j
				}
			}
			for j := 0; j <= size; j++ {
				if used[j] {
					u[p[j]] += delta
					v[j] -= delta
				} else {
					minv[j] -= delta
				}
			}
			j0 = j1
			if p[j0] == 0 {
				break
			}
		}
		for {
			j1 := way[j0]
			p[j0] = p[j1]
			j0 = j1
			if j0 == 0 {
				break
			}
		}
	}
	assignment := make([]int, n)
	for i := range assignment {
		assignment[i] = -1
	}
	total := 0
	for j := 1; j <= size; j++ {
		i := p[j] - 1
		col := j - 1
		if i < 0 || i >= n || col >= m || weights[i][col] == forbiddenWeight {
			continue
		}
		assignment[i] = col
		total += weights[i][col]
	}
	return assignment, total
}

func cloneMatrix(in [][]int) [][]int {
	out := make([][]int, len(in))
	for i := range in {
		out[i] = append([]int(nil), in[i]...)
	}
	return out
}

func competingLabels(row []edgeEvidence, live *model.Session, chosen int) []string {
	var labels []string
	for i, e := range row {
		if e.allowed && e.score == row[chosen].score {
			labels = append(labels, windowLabel(live.Windows[i]))
		}
	}
	sort.Strings(labels)
	return labels
}

func windowLabel(w model.Window) string {
	if w.Name != "" {
		return fmt.Sprintf("%s@index-%d", w.Name, w.Idx)
	}
	return fmt.Sprintf("window@index-%d", w.Idx)
}

func maxWindowIndex(w []model.Window) int {
	max := -1
	for _, win := range w {
		if win.Idx > max {
			max = win.Idx
		}
	}
	return max
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
