package tmux

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/fm39hz/gotomux/internal/model"
)

// Restore building blocks: reshaping a live session without touching the
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

// MoveWindow renumbers a window to a concrete index. It never kills anything:
// the pane tree, processes, and layout are preserved.
func (c *Ctl) MoveWindow(ctx context.Context, session string, from, to int) error {
	return tmuxRun(ctx, "move-window", "-s", windowTarget(session, from), "-t", windowTarget(session, to))
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
	return c.runChain(ctx, parts...)
}

// AddPane repairs a surviving window without disturbing its existing panes.
// Extra panes are deliberately never removed by reconciliation.
func (c *Ctl) AddPane(ctx context.Context, session string, idx int, p model.Pane) error {
	t := windowTarget(session, idx)
	args := []string{"split-window", "-t", t, "-h"}
	if p.Cwd != "" {
		args = append(args, "-c", p.Cwd)
	}
	if cmd := paneCmd(p); cmd != "" {
		args = append(args, cmdArgs(cmd)...)
	}
	return tmuxRun(ctx, args...)
}

func (c *Ctl) RenameWindow(ctx context.Context, session string, idx int, name string) error {
	name = safeWindowName(name, session)
	if name == "" {
		return nil
	}
	return tmuxRun(ctx, "rename-window", "-t", windowTarget(session, idx), name)
}

func (c *Ctl) ApplyLayout(ctx context.Context, session string, idx int, layout string) error {
	if layout == "" {
		return nil
	}
	return tmuxRun(ctx, "select-layout", "-t", windowTarget(session, idx), layout)
}

// ActiveWindow reports the current window index of a session, so a restore
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

// SelectWindow focuses a concrete window index.
func (c *Ctl) SelectWindow(ctx context.Context, session string, idx int) error {
	return tmuxRun(ctx, "select-window", "-t", windowTarget(session, idx))
}
