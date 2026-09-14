package template

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// RestoreSession reconciles a live session toward its recorded baseline layout:
// windows that died (the pane exited, tmux closed the window, and later
// windows renumbered down) are recreated at their baseline index with their
// baseline command, and surviving windows are moved back to their baseline
// indices. Nothing is killed — move-window keeps the pane tree and every
// process inside it intact; only missing windows are created fresh.
//
// Without a baseline, the current layout is adopted as the baseline and
// reported, so a second run actually restores.
//
// The report is user-facing prose ("" means nothing needed doing).
type RestoreOps interface {
	Freeze(ctx context.Context, name string) (*model.Session, error)
	MoveWindow(ctx context.Context, session string, from, to int) error
	NewWindowAt(ctx context.Context, session string, idx int, w model.Window, sessCwd string) error
	AddPane(ctx context.Context, session string, idx int, p model.Pane) error
	RenameWindow(ctx context.Context, session string, idx int, name string) error
	ApplyLayout(ctx context.Context, session string, idx int, layout string) error
	ActiveWindow(ctx context.Context, session string) (int, error)
	SelectWindow(ctx context.Context, session string, idx int) error
	ShowOption(ctx context.Context, session, name string) (string, error)
	SetOption(ctx context.Context, session, name, value string) error
}

func RestoreSession(ctx context.Context, ctl RestoreOps, st store.Storer, name string) (string, error) {
	live, err := ctl.Freeze(ctx, name)
	if err != nil {
		return "", err
	}

	// Baseline is the reconciliation contract. The preset is only a compatibility
	// fallback for sessions created before the baseline table existed.
	base, err := st.GetBaseline(name)
	if err != nil {
		return "", err
	}
	if base == nil {
		if base, err = st.Get(name); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	if base == nil {
		if err := st.SaveBaseline(live); err != nil {
			return "", fmt.Errorf("adopt baseline: %w", err)
		}
		return fmt.Sprintf("no baseline for %q — adopted current layout", name), nil
	}

	active, err := ctl.ActiveWindow(ctx, name)
	if err != nil {
		return "", fmt.Errorf("active window: %w", err)
	}

	plan := planReset(base, live, active)
	if len(plan.moves) == 0 && len(plan.creates) == 0 && len(plan.repairs) == 0 && len(plan.extras) == 0 {
		return "", nil
	}

	// Measured on tmux 3.7b: with renumber-windows on, even move-window
	// (unlink+relink) triggers a renumber pass that shifts indices behind the
	// plan's back. Disable it for the duration of the restore, then restore
	// the flag.
	ren, err := ctl.ShowOption(ctx, name, "renumber-windows")
	if err != nil {
		return "", fmt.Errorf("renumber-windows: %w", err)
	}
	// set -g options are invisible to a session-scoped lookup; fall back to
	// the global table (measured: show-options -t <sess> returns "" for a
	// global-only renumber-windows).
	if strings.TrimSpace(ren) == "" {
		if ren, err = ctl.ShowOption(ctx, "", "renumber-windows"); err != nil {
			return "", fmt.Errorf("renumber-windows (global): %w", err)
		}
	}
	if strings.TrimSpace(ren) == "on" {
		if err := ctl.SetOption(ctx, name, "renumber-windows", "off"); err != nil {
			return "", fmt.Errorf("renumber-windows off: %w", err)
		}
		defer func() { _ = ctl.SetOption(ctx, name, "renumber-windows", "on") }()
	}
	// Execution order is part of the plan's correctness: every move target is
	// empty at the moment of its step (see planReset's invariant comment).
	for _, mv := range plan.moves {
		if err := ctl.MoveWindow(ctx, name, mv[0], mv[1]); err != nil {
			return "", fmt.Errorf("move window %d -> %d: %w", mv[0], mv[1], err)
		}
	}
	for _, cr := range plan.creates {
		if err := ctl.NewWindowAt(ctx, name, cr.idx, cr.w, live.Cwd); err != nil {
			return "", fmt.Errorf("recreate window %d: %w", cr.idx, err)
		}
	}
	for _, rr := range plan.repairs {
		for _, pn := range rr.missing {
			if err := ctl.AddPane(ctx, name, rr.idx, pn); err != nil {
				return "", fmt.Errorf("repair window %d pane: %w", rr.idx, err)
			}
		}
		if rr.rename && rr.name != "" {
			if err := ctl.RenameWindow(ctx, name, rr.idx, rr.name); err != nil {
				return "", fmt.Errorf("rename window %d: %w", rr.idx, err)
			}
		}
		if rr.layout != "" {
			if err := ctl.ApplyLayout(ctx, name, rr.idx, rr.layout); err != nil {
				return "", fmt.Errorf("repair layout window %d: %w", rr.idx, err)
			}
		}
	}
	if plan.activeTo >= 0 {
		_ = ctl.SelectWindow(ctx, name, plan.activeTo)
	}
	return plan.report(), nil
}

type createStep struct {
	idx int
	w   model.Window
}

type restorePlan struct {
	moves    [][2]int       // vacate-then-place pairs, in execution order
	creates  []createStep   // recreated missing windows, at baseline index
	repairs  []repairStep   // existing windows repaired without killing panes
	extras   [][2]int       // unmatched live windows -> tail, relative order kept
	extraW   []model.Window // payloads for extras (index-aligned with extras)
	placed   int            // survivors moved back to their baseline index
	activeTo int            // -1 = leave tmux's choice alone
}

type liveWindow struct {
	w    model.Window
	used bool
	slot int
}

type repairStep struct {
	idx     int
	missing []model.Pane
	name    string
	rename  bool
	layout  string
}

// planReset computes the exact move/create sequence for restoring live to
// base. Pure and deterministic; RestoreSession just executes it.
//
// Invariant every step relies on: each window moves at most once, and every
// target index is empty at the moment of its step (the vacate prefix empties
// the whole baseline zone, so all later targets are provably free).
//
// Matching uses the window name and the complete pane/tool topology, never
// the window index alone: a dead window renumbers everything after it.
// Unmatched live windows are extras — kept, never killed.
func planReset(base, live *model.Session, activeLive int) restorePlan {
	p := restorePlan{activeTo: -1}
	if base == nil || len(base.Windows) == 0 {
		return p
	}
	if exactMatch(base, live) {
		return p
	}

	maxBase := 0
	for _, w := range base.Windows {
		if w.Idx > maxBase {
			maxBase = w.Idx
		}
	}
	maxLive := 0
	for _, w := range live.Windows {
		if w.Idx > maxLive {
			maxLive = w.Idx
		}
	}

	// Vacate phase: every live window at or below maxBase moves up, keeping
	// relative order, so the whole baseline zone empties out.
	wins := make([]liveWindow, len(live.Windows))
	for i, w := range live.Windows {
		wins[i] = liveWindow{w: w, slot: w.Idx}
	}
	free := maxLive + 1
	for i, w := range live.Windows {
		if w.Idx > maxBase {
			continue
		}
		wins[i].slot = free
		p.moves = append(p.moves, [2]int{w.Idx, free})
		free++
	}

	// Place phase: matched survivors to their baseline index; missing baseline
	// windows become creates. Existing but modified windows get a non-destructive
	// repair step instead of being torn down.
	matched := make([]bool, len(base.Windows))
	for bi, bw := range base.Windows {
		li := matchWindow(bw, wins)
		if li < 0 {
			continue
		}
		wins[li].used = true
		matched[bi] = true
		p.placed++
		if wins[li].slot != bw.Idx {
			p.moves = append(p.moves, [2]int{wins[li].slot, bw.Idx})
		}
		if wins[li].w.Idx == activeLive {
			p.activeTo = bw.Idx
		}
		if r, ok := repairWindow(bw, wins[li].w); ok {
			r.idx = bw.Idx
			p.repairs = append(p.repairs, r)
		}
	}
	for bi, bw := range base.Windows {
		if matched[bi] {
			continue
		}
		p.creates = append(p.creates, createStep{idx: bw.Idx, w: bw})
	}

	// Extras go above the baseline and above every window we never touched.
	tail := maxBase + 1
	if maxLive+1 > tail {
		tail = maxLive + 1
	}
	for li := range wins {
		if wins[li].used || wins[li].slot == wins[li].w.Idx {
			// used: placed. slot == original idx: never vacated (lived above
			// the baseline zone) and not matched — already at the tail.
			continue
		}
		p.extras = append(p.extras, [2]int{wins[li].slot, tail})
		ew := wins[li].w
		ew.Idx = tail // the extra lands at the tail index; persist that
		p.extraW = append(p.extraW, ew)
		if wins[li].w.Idx == activeLive {
			p.activeTo = tail
		}
		tail++
	}
	return p
}

// exactMatch: live is already the baseline topology. Cwd and running process
// details are intentionally ignored; reconciliation must not undo normal shell use.
func exactMatch(base, live *model.Session) bool {
	if len(base.Windows) != len(live.Windows) {
		return false
	}
	for i := range base.Windows {
		if base.Windows[i].Idx != live.Windows[i].Idx {
			return false
		}
		if !sameWindowShape(base.Windows[i], live.Windows[i]) {
			return false
		}
	}
	return true
}

func windowIntent(w model.Window) string {
	if len(w.Panes) == 0 {
		return ""
	}
	return tmux.ToolIntent(w.Panes[0].Cmd)
}

func paneIntents(w model.Window) []string {
	out := make([]string, len(w.Panes))
	for i := range w.Panes {
		out[i] = tmux.ToolIntent(w.Panes[i].Cmd)
	}
	return out
}

func layoutClass(w model.Window) string {
	return tmux.LayoutForShape(w.Layout, len(w.Panes))
}

func sameWindowShape(a, b model.Window) bool {
	if a.Name != b.Name || len(a.Panes) != len(b.Panes) || layoutClass(a) != layoutClass(b) {
		return false
	}
	aa, bb := paneIntents(a), paneIntents(b)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}

func matchWindow(base model.Window, live []liveWindow) int {
	// Names are only one weak feature. A modified or auto-renamed window must
	// still match from its pane topology, tools, cwd evidence and layout.
	// Identical candidates form an equivalence class: their identity is not
	// observable, so choosing the lowest live index is the only deterministic
	// choice and does not change the resulting topology.
	best, bestScore := -1, -1
	for i := range live {
		if live[i].used {
			continue
		}
		score := windowMatchScore(base, live[i].w)
		if score < windowMatchThreshold || !windowMatchAllowed(base, live[i].w) {
			continue
		}
		if score > bestScore || (score == bestScore && best >= 0 && live[i].w.Idx < live[best].w.Idx) {
			best, bestScore = i, score
		}
	}
	if best < 0 || bestScore < windowMatchThreshold {
		return -1
	}
	return best
}

const windowMatchThreshold = 5

func windowMatchScore(base, live model.Window) int {
	score := 0
	if base.Name != "" && base.Name == live.Name {
		score++ // useful tie-breaker, never sufficient by itself
	}
	if len(base.Panes) == len(live.Panes) {
		score += 3
	} else {
		d := len(base.Panes) - len(live.Panes)
		if d < 0 {
			d = -d
		}
		if d == 1 {
			score++ // likely a missing/extra pane; still needs other evidence
		}
	}
	if baseLayout, liveLayout := layoutClass(base), layoutClass(live); baseLayout != "" && baseLayout == liveLayout {
		score += 2
	}

	// Ordered tool intents are strong identity evidence. Empty intent means a
	// shell and is deliberately weak by itself; cwd/path evidence separates
	// otherwise identical shell panes when available.
	common := len(base.Panes)
	if len(live.Panes) < common {
		common = len(live.Panes)
	}
	for i := 0; i < common; i++ {
		if tmux.ToolIntent(base.Panes[i].Cmd) == tmux.ToolIntent(live.Panes[i].Cmd) {
			score += 2
		}
		if base.Panes[i].Cwd != "" && base.Panes[i].Cwd == live.Panes[i].Cwd {
			score++
		}
		if base.Panes[i].CmdPath != "" && base.Panes[i].CmdPath == live.Panes[i].CmdPath {
			score += 2
		}
	}
	if base.Cwd != "" && base.Cwd == live.Cwd {
		score++
	}
	return score
}

func windowMatchAllowed(base, live model.Window) bool {
	// If a non-shell tool or a cwd/path signal exists, it is meaningful
	// identity evidence. Without one, only an identical shell topology is safe:
	// two indistinguishable shell windows are an equivalence class, while a
	// changed tool must never be accepted merely because its name survived.
	for i := 0; i < len(base.Panes) && i < len(live.Panes); i++ {
		bi := tmux.ToolIntent(base.Panes[i].Cmd)
		li := tmux.ToolIntent(live.Panes[i].Cmd)
		if bi != "" || li != "" {
			if bi == li && bi != "" {
				return true
			}
		}
		if base.Panes[i].Cwd != "" && base.Panes[i].Cwd == live.Panes[i].Cwd {
			return true
		}
		if base.Panes[i].CmdPath != "" && base.Panes[i].CmdPath == live.Panes[i].CmdPath {
			return true
		}
	}
	if base.Cwd != "" && base.Cwd == live.Cwd {
		return true
	}
	return shellTopologyEquivalent(base, live)
}

func shellTopologyEquivalent(base, live model.Window) bool {
	if len(base.Panes) != len(live.Panes) || layoutClass(base) != layoutClass(live) {
		return false
	}
	for i := range base.Panes {
		if tmux.ToolIntent(base.Panes[i].Cmd) != "" || tmux.ToolIntent(live.Panes[i].Cmd) != "" {
			return false
		}
	}
	return true
}

func repairWindow(base, live model.Window) (repairStep, bool) {
	r := repairStep{rename: base.Name != "" && base.Name != live.Name}
	if len(live.Panes) < len(base.Panes) {
		r.missing = append([]model.Pane(nil), base.Panes[len(live.Panes):]...)
	}
	if r.rename {
		r.name = base.Name
	}
	if len(base.Panes) > 1 && layoutClass(base) != layoutClass(live) {
		r.layout = layoutClass(base)
	}
	return r, r.rename || len(r.missing) > 0 || r.layout != ""
}

func (p restorePlan) report() string {
	return fmt.Sprintf("restored: %d window(s) moved, %d recreated, %d repaired, %d kept as extras",
		p.placed, len(p.creates), len(p.repairs), len(p.extras))
}
