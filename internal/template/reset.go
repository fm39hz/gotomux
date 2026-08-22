package template

import (
	"context"
	"fmt"
	"strings"

	"github.com/fm39hz/gotomux/internal/model"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// RestoreSession brings a live session back to its recorded baseline layout:
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

	// The canonical layout for a session is its preset — the recorded
	// instance, written when the session was created (bake) or on freeze. It
	// is stable by construction. The baseline table is only a fallback for
	// hand-built sessions; it is never trusted over the preset and is NOT
	// rewritten after a restore (a restore that starts from a wrong baseline
	// would otherwise freeze the wrong layout forever).
	base, err := st.Get(name)
	if err == nil && base != nil && len(base.Windows) > 0 {
		// canonical shape: preset wins
	} else {
		if base, err = st.GetBaseline(name); err != nil {
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
	if len(plan.moves) == 0 && len(plan.creates) == 0 && len(plan.extras) == 0 {
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
	extras   [][2]int       // unmatched live windows -> tail, relative order kept
	extraW   []model.Window // payloads for extras (index-aligned with extras)
	placed   int            // survivors moved back to their baseline index
	activeTo int            // -1 = leave tmux's choice alone
}

// planReset computes the exact move/create sequence for restoring live to
// base. Pure and deterministic; RestoreSession just executes it.
//
// Invariant every step relies on: each window moves at most once, and every
// target index is empty at the moment of its step (the vacate prefix empties
// the whole baseline zone, so all later targets are provably free).
//
// Matching uses tool intent of the lead pane (tmux.ToolIntent), never window
// index: a dead window renumbers everything after it, so index identity is
// meaningless. Unmatched live windows are extras — kept, never killed.
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
	type liveWin struct {
		w    model.Window
		used bool
		slot int // index right after the vacate phase
	}
	wins := make([]liveWin, len(live.Windows))
	for i, w := range live.Windows {
		wins[i] = liveWin{w: w, slot: w.Idx}
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

	// Place phase: matched survivors to their baseline index; missing
	// baseline windows become creates.
	matched := make([]bool, len(base.Windows))
	// Intent alone is not enough: a pane's detected command can be transient
	// (a shell fresh from Load runs its init — zoxide hooks show up as the
	// foreground process). The window name survives renumbering, so it is the
	// fallback identity. Three tiers, greedy in baseline order.
	for tier := range 3 {
		for bi, bw := range base.Windows {
			if matched[bi] {
				continue
			}
			want := windowIntent(bw)
			for li := range wins {
				if wins[li].used {
					continue
				}
				got := windowIntent(wins[li].w)
				if tier == 0 {
					if got != want || wins[li].w.Name != bw.Name {
						continue
					}
				} else if tier == 1 {
					if got != want {
						continue
					}
				} else {
					// name only — intent was unreliable; skip empty names
					if bw.Name == "" || wins[li].w.Name != bw.Name {
						continue
					}
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
				break
			}
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

// exactMatch: live is already the baseline layout (same indices, same lead
// intents, same count) — nothing to do, and nothing should be touched.
func exactMatch(base, live *model.Session) bool {
	if len(base.Windows) != len(live.Windows) {
		return false
	}
	for i := range base.Windows {
		if base.Windows[i].Idx != live.Windows[i].Idx {
			return false
		}
		if windowIntent(base.Windows[i]) != windowIntent(live.Windows[i]) {
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

func (p restorePlan) report() string {
	return fmt.Sprintf("restored: %d window(s) moved, %d recreated, %d kept as extras",
		p.placed, len(p.creates), len(p.extras))
}
