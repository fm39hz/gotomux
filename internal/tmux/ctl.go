package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fm39hz/gotomux/internal/project"
	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/toolclass"
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

func LayoutForStore(layout string, nPanes int) string {
	if nPanes <= 1 || layout == "" {
		return ""
	}
	if IsNamedLayout(layout) || IsLayoutDump(layout) {
		return layout
	}
	return ""
}

func LayoutForShape(layout string, nPanes int) string {
	if nPanes <= 1 {
		return ""
	}
	if IsNamedLayout(layout) {
		return layout
	}
	if IsLayoutDump(layout) {
		return classifyDump(layout)
	}
	return ""
}

func classifyDump(dump string) string {
	h := strings.Contains(dump, "{")
	v := strings.Contains(dump, "[")
	switch {
	case h && v:
		return "tiled"
	case v:
		return "even-vertical"
	case h:
		return "even-horizontal"
	default:
		return "even-horizontal"
	}
}

func InferSplit(layout string, nPanes int) string {
	if nPanes <= 1 {
		return ""
	}
	if IsNamedLayout(layout) {
		return layout
	}
	if IsLayoutDump(layout) {
		return classifyDump(layout)
	}
	return "even-horizontal"
}

type Ctl struct{}

func New() (*Ctl, error) {
	// Verify tmux is reachable
	if err := exec.Command("tmux", "info").Run(); err != nil {
		return nil, fmt.Errorf("tmux not reachable: %w", err)
	}
	return &Ctl{}, nil
}

func tmuxCmd(args ...string) (string, error) {
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return "", fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSuffix(strings.TrimRight(string(out), "\n"), "\n"), nil
}

func tmuxRun(args ...string) error {
	cmd := exec.Command("tmux", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

const listSessFmt = "S\t#{session_name}\t#{session_windows}\t#{session_path}\t#{session_last_attached}\t#{session_activity}\t#{session_created}\t#{session_attached}"
const listPanesFmt = "P\t#{session_name}\t#{pane_current_command}\t#{?pane_active,1,0}\t#{?pane_dead,1,0}"

type LiveSession struct {
	Name         string
	Windows      int
	Path         string
	LastAttached int64
	Activity     int64
	Created      int64
	Attached     int
	ActiveCmd    string
}

func (c *Ctl) ListLive() ([]LiveSession, error) {
	out, err := exec.Command("tmux",
		"list-sessions", "-F", listSessFmt,
		";",
		"list-panes", "-s", "-F", listPanesFmt,
	).Output()
	if err != nil {
		return nil, fmt.Errorf("list live: %w", err)
	}

	type livePane struct {
		sname  string
		cmd    string
		active bool
		dead   bool
	}

	byName := map[string]LiveSession{}
	var order []string
	orderSeen := map[string]bool{}
	panes := map[string][]livePane{}

	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || len(line) < 2 || line[1] != '\t' {
			continue
		}
		fields := strings.Split(line[2:], "\t")

		switch line[0] {
		case 'S':
			if len(fields) < 7 {
				continue
			}
			name := fields[0]
			nw, _ := strconv.Atoi(fields[1])
			na, _ := strconv.Atoi(fields[6])
			byName[name] = LiveSession{
				Name: name, Windows: nw, Path: fields[2],
				LastAttached: parseUnix(fields[3]), Activity: parseUnix(fields[4]),
				Created: parseUnix(fields[5]), Attached: na,
			}
			if !orderSeen[name] {
				orderSeen[name] = true
				order = append(order, name)
			}
		case 'P':
			if len(fields) < 4 {
				continue
			}
			panes[fields[0]] = append(panes[fields[0]], livePane{
				cmd: fields[1], active: fields[2] == "1", dead: fields[3] == "1",
			})
		}
	}

	activeCmd := map[string]string{}
	busyCmd := map[string]string{}
	for sn, list := range panes {
		for _, p := range list {
			if p.cmd == "" || p.dead {
				continue
			}
			if _, seen := activeCmd[sn]; !seen && p.active {
				activeCmd[sn] = p.cmd
			}
			if _, seen := busyCmd[sn]; !seen && !toolclass.IsShell(p.cmd) {
				busyCmd[sn] = p.cmd
			}
		}
	}

	out2 := make([]LiveSession, 0, len(order))
	for _, name := range order {
		s := byName[name]
		cmd := activeCmd[name]
		if cmd == "" || toolclass.IsShell(cmd) {
			if b, ok := busyCmd[name]; ok {
				cmd = b
			}
		}
		s.ActiveCmd = cmd
		out2 = append(out2, s)
	}
	return out2, nil
}

func parseUnix(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0
	}
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
	return exec.Command("tmux", "has-session", "-t", name).Run() == nil
}

func (c *Ctl) CurrentSession() string {
	if os.Getenv("TMUX") == "" {
		return ""
	}
	out, err := tmuxCmd("display-message", "-p", "#S")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (c *Ctl) Kill(name string) error {
	if name == "" {
		return fmt.Errorf("kill: empty session name")
	}
	return tmuxRun("kill-session", "-t", name)
}

// runChain: one tmux client process, commands separated by "\;".
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

const freezeFmt = "#{window_index}\t#{window_name}\t#{window_layout}\t#{pane_index}\t#{pane_current_path}\t#{pane_current_command}\t#{pane_start_command}\t#{pane_pid}\t#{pane_active}\t#{session_path}"

func (c *Ctl) Freeze(name string) (*store.Preset, error) {
	if !project.ValidSessionName(name) {
		return nil, fmt.Errorf("invalid session name %q", name)
	}
	if !c.Has(name) {
		return nil, fmt.Errorf("session %q not found", name)
	}

	raw, err := tmuxCmd("list-panes", "-s", "-t", "="+name, "-F", freezeFmt)
	if err != nil {
		return nil, fmt.Errorf("list-panes: %w", err)
	}
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return nil, fmt.Errorf("session %q has no panes", name)
	}

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
		if base := toolclass.Base(parts[5]); base == "" || toolclass.IsShell(base) {
			if base := toolclass.Base(parts[6]); base == "" || toolclass.IsShell(base) {
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
		t := windowTarget(p.Name, base+i)
		parts = append(parts, []string{"set-option", "-t", t, "automatic-rename", "off"})
		if safe := safeWindowName(w.Name, p.Name); safe != "" {
			parts = append(parts, []string{"rename-window", "-t", t, safe})
		}
		for _, pn := range w.Panes[1:] {
			sp := []string{"split-window", "-t", t, "-h", "-c", pn.Cwd}
			if pn.Cmd != "" {
				sp = append(sp, cmdArgs(pn.Cmd)...)
			}
			parts = append(parts, sp)
		}
		if w.Layout != "" {
			parts = append(parts, []string{"select-layout", "-t", t, w.Layout})
		}
	}

	w0, p0 := wins[0], wins[0].Panes[0]
	ns := []string{"new-session", "-d", "-s", p.Name, "-c", p0.Cwd}
	if safe := safeWindowName(w0.Name, p.Name); safe != "" {
		ns = append(ns, "-n", safe)
	}
	if p0.Cmd != "" {
		ns = append(ns, cmdArgs(p0.Cmd)...)
	}
	appendWin(0, w0, ns)

	for i, w := range wins[1:] {
		pn := w.Panes[0]
		nw := []string{"new-window", "-t", sessionTarget(p.Name), "-d", "-c", pn.Cwd}
		if safe := safeWindowName(w.Name, p.Name); safe != "" {
			nw = append(nw, "-n", safe)
		}
		if pn.Cmd != "" {
			nw = append(nw, cmdArgs(pn.Cmd)...)
		}
		appendWin(i+1, w, nw)
	}

	parts = append(parts, []string{"select-window", "-t", windowTarget(p.Name, base)})

	if err := c.runChain(parts...); err != nil {
		_ = c.Kill(p.Name)
		return fmt.Errorf("load %q: %w", p.Name, err)
	}
	return nil
}

func (c *Ctl) windowBaseIndex() int {
	out, err := tmuxCmd("show-options", "-gv", "base-index")
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func sessionTarget(session string) string { return "=" + session + ":" }

func windowTarget(session string, idx int) string {
	return fmt.Sprintf("=%s:%d", session, idx)
}

func safeWindowName(name, session string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if filepath.IsAbs(name) || strings.ContainsAny(name, "/:\\") {
		return ""
	}
	if strings.Count(name, "/") >= 1 {
		return ""
	}
	if session != "" && name == session {
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
		if err := tmuxRun("new-session", "-d", "-s", name, "-c", cwd); err != nil {
			return fmt.Errorf("create session %q: %w", name, err)
		}
	}
	if os.Getenv("TMUX") != "" {
		if err := tmuxRun("switch-client", "-t", name); err != nil {
			return fmt.Errorf("switch to %q: %w", name, err)
		}
		return nil
	}
	cmd := exec.Command("tmux", "attach-session", "-t", name)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
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