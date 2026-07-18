package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/GianlucaP106/gotmux/gotmux"
	"github.com/fm39hz/gotomux/internal/project"
	"github.com/fm39hz/gotomux/internal/store"
)

var namedLayouts = map[string]bool{
	"even-horizontal": true,
	"even-vertical":   true,
	"main-horizontal": true,
	"main-vertical":   true,
	"tiled":           true,
}

func IsNamedLayout(s string) bool { return namedLayouts[s] }

func IsLayoutDump(s string) bool {
	return strings.Contains(s, ",") && (strings.Contains(s, "{") || strings.Contains(s, "[") || strings.Contains(s, "x"))
}

// LayoutForStore: named layouts + raw window_layout dumps.
func LayoutForStore(layout string, nPanes int) string {
	if nPanes <= 1 || layout == "" {
		return ""
	}
	if IsNamedLayout(layout) || IsLayoutDump(layout) {
		return layout
	}
	return ""
}

// LayoutForBake: apply stored layout; multi-pane empty → even-horizontal.
func LayoutForBake(layout string, nPanes int) string {
	if nPanes <= 1 {
		return ""
	}
	if layout != "" {
		return layout
	}
	return "even-horizontal"
}

type Ctl struct {
	t *gotmux.Tmux
}

func New() (*Ctl, error) {
	t, err := gotmux.DefaultTmux()
	if err != nil {
		return nil, err
	}
	return &Ctl{t: t}, nil
}

type LiveSession struct {
	Name         string
	Windows      int
	Path         string // session_path — for dedup vs zoxide
	LastAttached int64  // unix; 0 if unknown
	Activity     int64  // unix last pane activity
	Created      int64  // unix session created
	Attached     int    // client count
}

func (c *Ctl) ListLive() ([]LiveSession, error) {
	ss, err := c.t.ListSessions()
	if err != nil {
		return nil, err
	}
	out := make([]LiveSession, 0, len(ss))
	for _, s := range ss {
		out = append(out, LiveSession{
			Name:         s.Name,
			Windows:      s.Windows,
			Path:         s.Path,
			LastAttached: parseUnix(s.LastAttached),
			Activity:     parseUnix(s.Activity),
			Created:      parseUnix(s.Created),
			Attached:     s.Attached,
		})
	}
	return out, nil
}

// parseUnix: tmux/gotmux often expose epoch seconds as decimal string.
func parseUnix(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0
	}
	// strip fractional if any
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func (c *Ctl) Has(name string) bool {
	return c.t.HasSession(name)
}

// CurrentSession: attached session name, or empty outside tmux.
// Uses gotmux Command (same socket path) — no extra raw exec import.
func (c *Ctl) CurrentSession() string {
	if os.Getenv("TMUX") == "" {
		return ""
	}
	out, err := c.t.Command("display-message", "-p", "#S")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (c *Ctl) Kill(name string) error {
	if name == "" {
		return fmt.Errorf("kill: empty session name")
	}
	s, err := c.t.GetSessionByName(name)
	if err != nil {
		return fmt.Errorf("kill %q: %w", name, err)
	}
	if s == nil {
		return fmt.Errorf("kill %q: session not found", name)
	}
	if err := s.Kill(); err != nil {
		return fmt.Errorf("kill %q: %w", name, err)
	}
	return nil
}

func (c *Ctl) run(args ...string) error {
	if len(args) == 0 {
		return fmt.Errorf("tmux: empty command")
	}
	if _, err := c.t.Command(args...); err != nil {
		return fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// runChain: one tmux client process, commands separated by "\;" (literal).
// Uses exec directly — gotmux Command error strings are opaque and some
// chained forms confuse its query builder.
func (c *Ctl) runChain(parts ...[]string) error {
	var args []string
	first := true
	for _, p := range parts {
		if len(p) == 0 {
			continue
		}
		if !first {
			args = append(args, ";")
		}
		first = false
		args = append(args, p...)
	}
	if len(args) == 0 {
		return nil
	}
	cmd := exec.Command("tmux", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux chain: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// freezeFmt: one list-panes -s covers all windows/panes of a session.
const freezeFmt = "#{window_index}\t#{window_name}\t#{window_layout}\t#{pane_index}\t#{pane_current_path}\t#{pane_current_command}\t#{pane_start_command}\t#{pane_pid}\t#{pane_active}\t#{session_path}"

// Freeze: 1× list-panes + 1× ps snapshot (portable). No nested ListWindows/ListPanes.
func (c *Ctl) Freeze(name string) (*store.Preset, error) {
	if !project.ValidSessionName(name) {
		return nil, fmt.Errorf("invalid session name %q", name)
	}
	if !c.Has(name) {
		return nil, fmt.Errorf("session %q not found", name)
	}

	raw, err := c.t.Command("list-panes", "-s", "-t", "="+name, "-F", freezeFmt)
	if err != nil {
		return nil, fmt.Errorf("list-panes: %w", err)
	}
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return nil, fmt.Errorf("session %q has no panes", name)
	}

	// Lazy: only snapshot processes when some pane still looks like a shell.
	var procs *procIndex
	needPS := false
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		for len(parts) < 10 {
			parts = append(parts, "")
		}
		if base := binBase(parts[5]); base == "" || shellNames[base] {
			if base := binBase(parts[6]); base == "" || shellNames[base] {
				needPS = true
				break
			}
		}
	}
	if needPS {
		procs = loadProcIndex()
	}

	type winAcc struct {
		idx    int
		name   string
		layout string
		panes  []store.PresetPane
		cwd    string
	}
	order := []int{}
	byIdx := map[int]*winAcc{}
	sessPath := ""

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 10 {
			for len(parts) < 10 {
				parts = append(parts, "")
			}
		}
		wIdx, _ := strconv.Atoi(parts[0])
		wName := parts[1]
		wLayout := parts[2]
		pIdx, _ := strconv.Atoi(parts[3])
		pPath := parts[4]
		pCur := parts[5]
		pStart := parts[6]
		pPid64, _ := strconv.ParseInt(parts[7], 10, 32)
		pActive := parts[8] == "1"
		if sessPath == "" {
			sessPath = parts[9]
		}

		w, ok := byIdx[wIdx]
		if !ok {
			w = &winAcc{idx: wIdx, name: wName, layout: wLayout}
			byIdx[wIdx] = w
			order = append(order, wIdx)
		}
		cmd := detectPaneCmd(pCur, pStart, int32(pPid64), procs)
		w.panes = append(w.panes, store.PresetPane{
			Idx: pIdx,
			Cwd: pPath,
			Cmd: cmd,
		})
		if w.cwd == "" || pActive {
			if pPath != "" {
				w.cwd = pPath
			}
		}
	}

	p := &store.Preset{Name: name, Cwd: sessPath}
	for _, wi := range order {
		w := byIdx[wi]
		if w.cwd == "" {
			w.cwd = p.Cwd
		}
		p.Windows = append(p.Windows, store.PresetWindow{
			Idx:    w.idx,
			Name:   w.name,
			Cwd:    w.cwd,
			Layout: LayoutForStore(w.layout, len(w.panes)),
			Panes:  w.panes,
		})
	}
	if p.Cwd == "" && len(p.Windows) > 0 {
		p.Cwd = p.Windows[0].Cwd
	}
	return p, nil
}

// Load mirrors tmuxp: create session/windows/splits, pin names, select-layout.
// One tmux client process; "\;" separators.
// Targets use window *index* (sess:N) with global base-index — never raw names
// (path-like automatic-rename breaks targets: "can't find pane: cache/").
func (c *Ctl) Load(p *store.Preset) error {
	if !project.ValidSessionName(p.Name) {
		return fmt.Errorf("invalid session name %q", p.Name)
	}
	if c.Has(p.Name) {
		return nil
	}

	sessCwd := p.Cwd
	if sessCwd == "" {
		var err error
		sessCwd, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("load %q: cwd: %w", p.Name, err)
		}
	}
	base := c.windowBaseIndex()

	wins := normalizeWindows(p.Windows, sessCwd)
	var parts [][]string

	appendWin := func(i int, w store.PresetWindow, create []string) {
		parts = append(parts, create)
		t := sessionIndexTarget(p.Name, base+i)
		parts = append(parts, []string{"set-option", "-t", t, "automatic-rename", "off"})
		if safe := safeWindowName(w.Name); safe != "" {
			parts = append(parts, []string{"rename-window", "-t", t, safe})
		}
		for _, pn := range w.Panes[1:] {
			sp := []string{"split-window", "-t", t, "-h", "-c", pn.Cwd}
			if pn.Cmd != "" {
				sp = append(sp, cmdArgs(pn.Cmd)...)
			}
			parts = append(parts, sp)
		}
		if layout := LayoutForBake(w.Layout, len(w.Panes)); layout != "" {
			parts = append(parts, []string{"select-layout", "-t", t, layout})
		}
	}

	w0, p0 := wins[0], wins[0].Panes[0]
	ns := []string{"new-session", "-d", "-s", p.Name, "-c", p0.Cwd}
	if safe := safeWindowName(w0.Name); safe != "" {
		ns = append(ns, "-n", safe)
	}
	if p0.Cmd != "" {
		ns = append(ns, cmdArgs(p0.Cmd)...)
	}
	appendWin(0, w0, ns)

	for i, w := range wins[1:] {
		pn := w.Panes[0]
		nw := []string{"new-window", "-t", p.Name, "-d", "-c", pn.Cwd}
		if safe := safeWindowName(w.Name); safe != "" {
			nw = append(nw, "-n", safe)
		}
		if pn.Cmd != "" {
			nw = append(nw, cmdArgs(pn.Cmd)...)
		}
		appendWin(i+1, w, nw)
	}

	parts = append(parts, []string{"select-window", "-t", sessionIndexTarget(p.Name, base)})

	if err := c.runChain(parts...); err != nil {
		_ = c.Kill(p.Name) // best-effort cleanup of half-created session
		return fmt.Errorf("load %q: %w", p.Name, err)
	}
	return nil
}

// windowBaseIndex: global base-index (many configs use 1).
func (c *Ctl) windowBaseIndex() int {
	out, err := c.t.Command("show-options", "-gv", "base-index")
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// sessionIndexTarget: sess:N — numeric, no slash issues.
func sessionIndexTarget(session string, idx int) string {
	return fmt.Sprintf("%s:%d", session, idx)
}

// safeWindowName: empty if name would break tmux targets (paths, colons, etc.).
func safeWindowName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if filepath.IsAbs(name) || strings.ContainsAny(name, "/:\\") {
		return ""
	}
	// still reject path-like multi-segment
	if strings.Count(name, "/") >= 1 {
		return ""
	}
	return name
}

func normalizeWindows(wins []store.PresetWindow, sessCwd string) []store.PresetWindow {
	if len(wins) == 0 {
		return []store.PresetWindow{{
			Name:  "",
			Cwd:   sessCwd,
			Panes: []store.PresetPane{{Cwd: sessCwd}},
		}}
	}
	out := make([]store.PresetWindow, len(wins))
	for i, w := range wins {
		out[i] = w
		wcwd := w.Cwd
		if wcwd == "" {
			wcwd = sessCwd
		}
		out[i].Cwd = wcwd
		if len(w.Panes) == 0 {
			out[i].Panes = []store.PresetPane{{Cwd: wcwd}}
			continue
		}
		panes := make([]store.PresetPane, len(w.Panes))
		for j, pn := range w.Panes {
			panes[j] = pn
			if panes[j].Cwd == "" {
				panes[j].Cwd = wcwd
			}
		}
		out[i].Panes = panes
	}
	return out
}

func cmdArgs(cmd string) []string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil
	}
	return strings.Fields(cmd)
}

// Connect attaches or switches to session. Creates empty session if missing.
func (c *Ctl) Connect(name, cwd string) error {
	if !project.ValidSessionName(name) {
		return fmt.Errorf("invalid session name %q", name)
	}
	if !c.Has(name) {
		if cwd == "" {
			var err error
			cwd, err = os.Getwd()
			if err != nil {
				return fmt.Errorf("connect %q: cwd: %w", name, err)
			}
		}
		if err := c.run("new-session", "-d", "-s", name, "-c", cwd); err != nil {
			return fmt.Errorf("create session %q: %w", name, err)
		}
	}
	s, err := c.t.GetSessionByName(name)
	if err != nil {
		return fmt.Errorf("get session %q: %w", name, err)
	}
	if s == nil {
		return fmt.Errorf("session %q not found after create", name)
	}
	if os.Getenv("TMUX") != "" {
		if err := c.t.SwitchClient(&gotmux.SwitchClientOptions{TargetSession: name}); err != nil {
			return fmt.Errorf("switch to %q: %w", name, err)
		}
		return nil
	}
	if err := s.Attach(); err != nil {
		return fmt.Errorf("attach %q: %w", name, err)
	}
	return nil
}

func (c *Ctl) ConnectPreset(p *store.Preset) error {
	if p == nil {
		return fmt.Errorf("connect preset: nil")
	}
	if err := c.Load(p); err != nil {
		return fmt.Errorf("load preset %q: %w", p.Name, err)
	}
	return c.Connect(p.Name, p.Cwd)
}
