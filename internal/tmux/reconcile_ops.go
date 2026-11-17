package tmux

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/fm39hz/gotomux/internal/model"
)

// Reconciliation operations reshape a live session without touching the
// processes inside. move-window only renumbers — the pane tree (and every PID
// in it) is untouched. new-window at a concrete index rebuilds a missing
// window exactly like Load does.

// paneCmd: the full command to recreate a pane.
//
// Prefer the frozen absolute executable (CmdPath): panes are spawned by the
// tmux server under the creating client's environment, where a bare name the
// user's interactive shell resolves via config-added PATH entries may not
// exist — the pane dies instantly and the window silently disappears from
// rebuilt sessions. Arguments from StartCmd are kept verbatim; without a
// StartCmd, fall back to the detected bare Cmd.
//
// A frozen path can dangle (tool upgraded, version-manager shim rotated), so
// it is honoured only while it still exists; a dangling path falls back to
// StartCmd/Cmd — the pre-CmdPath behaviour — instead of failing the spawn.
func paneCmd(p model.Pane) string {
	if p.CmdPath != "" && fileExists(p.CmdPath) {
		if p.StartCmd == "" {
			return p.CmdPath
		}
		start := strings.TrimSpace(p.StartCmd)
		rest := ""
		if i := strings.IndexAny(start, " \t"); i >= 0 {
			rest = start[i:]
		}
		return strings.TrimSpace(p.CmdPath + rest)
	}
	if p.StartCmd != "" {
		return p.StartCmd
	}
	return p.Cmd
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// windowBodyParts: post-creation sequence shared by Load and NewWindowAt —
// automatic-rename off first (a rename after that would clobber our name),
// then safe rename, remaining panes as splits, then layout.
func windowBodyParts(t, session string, w model.Window) [][]string {
	parts := [][]string{{"set-option", "-t", t, "automatic-rename", "off"}}
	if safe := safeWindowName(w.Name, session); safe != "" {
		parts = append(parts, []string{"rename-window", "-t", t, safe})
	}
	for _, pn := range w.Panes[1:] {
		sp := []string{"split-window", "-t", t, "-h", "-c", pn.Cwd}
		if cmd := paneCmd(pn); cmd != "" {
			sp = append(sp, cmdArgs(cmd)...)
		}
		parts = append(parts, sp)
	}
	if w.Layout != "" {
		parts = append(parts, []string{"select-layout", "-t", t, w.Layout})
	}
	return parts
}

// MoveWindowTo addresses a surviving window by tmux's stable @window-id when
// available. from is used only when no same-server runtime binding exists.
func (c *Ctl) MoveWindowTo(ctx context.Context, session, windowID string, from, to int) error {
	source := windowTarget(session, from)
	if windowID != "" {
		source = windowID
		if strings.HasPrefix(session, "$") {
			source = session + ":" + windowID
		}
	}
	return tmuxRun(ctx, "move-window", "-d", "-s", source, "-t", windowTarget(session, to))
}

func (c *Ctl) LinkWindowTo(ctx context.Context, session, windowID string, idx int) error {
	if !strings.HasPrefix(windowID, "@") {
		return fmt.Errorf("link-window requires a tmux window ID")
	}
	return tmuxRun(ctx, "link-window", "-d", "-s", windowID, "-t", windowTarget(session, idx))
}

func (c *Ctl) KillWindowAt(ctx context.Context, session, windowID string, idx int) error {
	t := windowTarget(session, idx)
	if windowID != "" {
		t = windowID
	}
	return tmuxRun(ctx, "kill-window", "-t", t)
}

// NewWindowAt creates a window at a concrete index with the same command /
// split / layout sequence Load uses for preset windows. Fails if the index is
// already occupied (callers vacate the target first).
func (c *Ctl) NewWindowAt(ctx context.Context, session string, idx int, w model.Window, sessCwd string) error {
	w = normalizeWindows([]model.Window{w}, sessCwd)[0]
	t := windowTarget(session, idx)
	p0 := w.Panes[0]
	nw := []string{"new-window", "-d", "-t", t, "-c", p0.Cwd}
	if cmd := paneCmd(p0); cmd != "" {
		nw = append(nw, cmdArgs(cmd)...)
	}
	parts := [][]string{nw}
	parts = append(parts, windowBodyParts(t, session, w)...)
	if err := c.runChain(ctx, parts...); err != nil {
		return err
	}
	if w.SlotID == "" && noPaneSlotIDs(w.Panes) {
		return nil
	}
	windowID, err := tmuxCmd(ctx, "display-message", "-p", "-t", windowTarget(session, idx), "#{window_id}")
	if err != nil {
		return fmt.Errorf("observe created window identity: %w", err)
	}
	windowID = strings.TrimSpace(windowID)
	if !strings.HasPrefix(windowID, "@") {
		return fmt.Errorf("created window at index %d returned invalid ID %q", idx, windowID)
	}
	if w.SlotID != "" {
		if err := c.SetStableID(ctx, IdentityWindow, windowID, w.SlotID); err != nil {
			return fmt.Errorf("tag created window %s: %w", windowID, err)
		}
	}
	if len(w.Panes) == 0 {
		return nil
	}
	paneRows, err := tmuxCmd(ctx, "list-panes", "-t", windowID, "-F", "#{pane_id}")
	if err != nil {
		return fmt.Errorf("observe created pane identities: %w", err)
	}
	paneIDs := strings.Split(paneRows, "\n")
	if len(paneIDs) != len(w.Panes) {
		return fmt.Errorf("created window %s has %d panes; expected %d", windowID, len(paneIDs), len(w.Panes))
	}
	for i, pane := range w.Panes {
		if pane.SlotID == "" {
			continue
		}
		paneID := strings.TrimSpace(paneIDs[i])
		if !strings.HasPrefix(paneID, "%") {
			return fmt.Errorf("created pane %d returned invalid ID %q", i, paneID)
		}
		if err := c.SetStableID(ctx, IdentityPane, paneID, pane.SlotID); err != nil {
			return fmt.Errorf("tag created pane %s: %w", paneID, err)
		}
	}
	return nil
}

func noPaneSlotIDs(panes []model.Pane) bool {
	for _, pane := range panes {
		if pane.SlotID != "" {
			return false
		}
	}
	return true
}

func (c *Ctl) AddPaneToWindow(ctx context.Context, session, windowID string, idx int, p model.Pane) error {
	t := windowTarget(session, idx)
	if windowID != "" {
		t = windowID
	}
	args := []string{"split-window", "-t", t, "-h"}
	if p.Cwd != "" {
		args = append(args, "-c", p.Cwd)
	}
	if cmd := paneCmd(p); cmd != "" {
		args = append(args, cmdArgs(cmd)...)
	}
	return tmuxRun(ctx, args...)
}

func (c *Ctl) RenameWindowAt(ctx context.Context, session, windowID string, idx int, name string) error {
	name = safeWindowName(name, session)
	if name == "" {
		return nil
	}
	t := windowTarget(session, idx)
	if windowID != "" {
		t = windowID
	}
	return c.runChain(ctx,
		[]string{"set-option", "-t", t, "automatic-rename", "off"},
		[]string{"rename-window", "-t", t, name},
	)
}

func (c *Ctl) ApplyLayoutAt(ctx context.Context, session, windowID string, idx int, layout string) error {
	if layout == "" {
		return nil
	}
	t := windowTarget(session, idx)
	if windowID != "" {
		t = windowID
	}
	return tmuxRun(ctx, "select-layout", "-t", t, layout)
}

// ActiveWindow reports the current window index of a session, so reconciliation
// can leave the user exactly where they were working. Read via the
// window_active flag (not display-message -t session): current window is
// per-client, so a detached session answers "" to the latter.
func (c *Ctl) ActiveWindow(ctx context.Context, session string) (int, error) {
	out, err := tmuxCmd(ctx, "list-windows", "-t", session, "-F", "#{window_active}\t#{window_index}")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 || parts[0] != "1" {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return 0, fmt.Errorf("active window: %w", err)
		}
		return n, nil
	}
	return 0, fmt.Errorf("active window: %q has no active window", session)
}

// ShowOption reads a session option value (the -v form: bare value). A
// session-only lookup misses options set with `set -g`, so the caller
// falls back by passing "" — which reads the global table.
func (c *Ctl) ShowOption(ctx context.Context, session, name string) (string, error) {
	if session == "" {
		return tmuxCmd(ctx, "show-options", "-g", "-v", name)
	}
	return tmuxCmd(ctx, "show-options", "-t", session, "-v", name)
}

// SetOption sets a session option.
func (c *Ctl) SetOption(ctx context.Context, session, name, value string) error {
	return tmuxRun(ctx, "set-option", "-t", session, name, value)
}

func (c *Ctl) UnsetOption(ctx context.Context, session, name string) error {
	return tmuxRun(ctx, "set-option", "-u", "-t", session, name)
}

// SelectWindow focuses a concrete window index.
func (c *Ctl) SelectWindow(ctx context.Context, session string, idx int) error {
	return tmuxRun(ctx, "select-window", "-t", windowTarget(session, idx))
}
