package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/GianlucaP106/gotmux/gotmux"
)

type TmuxCtl struct {
	t *gotmux.Tmux
}

func newTmuxCtl() (*TmuxCtl, error) {
	t, err := gotmux.DefaultTmux()
	if err != nil {
		return nil, err
	}
	return &TmuxCtl{t: t}, nil
}

type LiveSession struct {
	Name    string
	Windows int
}

func (c *TmuxCtl) ListLive() ([]LiveSession, error) {
	ss, err := c.t.ListSessions()
	if err != nil {
		return nil, nil
	}
	out := make([]LiveSession, 0, len(ss))
	for _, s := range ss {
		out = append(out, LiveSession{Name: s.Name, Windows: s.Windows})
	}
	return out, nil
}

func (c *TmuxCtl) Has(name string) bool {
	return c.t.HasSession(name)
}

func (c *TmuxCtl) Kill(name string) error {
	s, err := c.t.GetSessionByName(name)
	if err != nil || s == nil {
		return err
	}
	return s.Kill()
}

func (c *TmuxCtl) run(args ...string) error {
	_, err := c.t.Command(args...)
	if err != nil {
		return fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func (c *TmuxCtl) Freeze(name string) (*Preset, error) {
	s, err := c.t.GetSessionByName(name)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("session %q not found", name)
	}

	p := &Preset{Name: name, Cwd: s.Path}
	wins, err := s.ListWindows()
	if err != nil {
		return nil, err
	}
	for _, w := range wins {
		pw := PresetWindow{Idx: w.Index, Name: w.Name, Layout: w.Layout}
		panes, err := w.ListPanes()
		if err != nil {
			return nil, err
		}
		for _, pn := range panes {
			cwd := pn.CurrentPath
			if cwd == "" {
				cwd = pn.Path
			}
			cmd := detectPaneCmd(pn.CurrentCommand, pn.Pid)
			if cmd == "" && pn.StartCommand != "" {
				if b := binBase(pn.StartCommand); b != "" && !shellNames[b] {
					cmd = b
				}
			}
			pw.Panes = append(pw.Panes, PresetPane{
				Idx: pn.Index,
				Cwd: cwd,
				Cmd: cmd,
			})
			if pw.Cwd == "" || pn.Active {
				if cwd != "" {
					pw.Cwd = cwd
				}
			}
		}
		if pw.Cwd == "" {
			pw.Cwd = p.Cwd
		}
		p.Windows = append(p.Windows, pw)
	}
	if p.Cwd == "" && len(p.Windows) > 0 {
		p.Cwd = p.Windows[0].Cwd
	}
	return p, nil
}

// Load mirrors tmuxp semantics:
//
//	session start_directory
//	window: panes[] each with start_directory + optional shell_command
//
// Uses raw tmux commands so shell_command works on new-session / new-window / split-window.
func (c *TmuxCtl) Load(p *Preset) error {
	if c.Has(p.Name) {
		return nil
	}

	sessCwd := p.Cwd
	if sessCwd == "" {
		sessCwd, _ = os.Getwd()
	}

	wins := normalizeWindows(p.Windows, sessCwd)
	w0 := wins[0]
	p0 := w0.Panes[0]

	// new-session -d -s NAME -c CWD -n WINNAME [cmd]
	args := []string{"new-session", "-d", "-s", p.Name, "-c", p0.Cwd}
	if w0.Name != "" {
		args = append(args, "-n", w0.Name)
	}
	if p0.Cmd != "" {
		args = append(args, p0.Cmd)
	}
	if err := c.run(args...); err != nil {
		return err
	}
	c.pinWindowName(p.Name, w0.Name)

	if err := c.splitRest(p.Name, w0.Name, w0.Panes[1:]); err != nil {
		return err
	}
	c.applyLayout(p.Name, w0)

	for _, w := range wins[1:] {
		pn := w.Panes[0]
		args := []string{"new-window", "-t", p.Name, "-d", "-c", pn.Cwd}
		if w.Name != "" {
			args = append(args, "-n", w.Name)
		}
		if pn.Cmd != "" {
			args = append(args, pn.Cmd)
		}
		if err := c.run(args...); err != nil {
			return err
		}
		c.pinWindowName(p.Name, w.Name)
		if err := c.splitRest(p.Name, w.Name, w.Panes[1:]); err != nil {
			return err
		}
		c.applyLayout(p.Name, w)
	}

	// focus first window (tmuxp focus:true on editor)
	_ = c.run("select-window", "-t", sessionTarget(p.Name, wins[0].Name))
	return nil
}

// pinWindowName: match tmuxp automatic-rename:false so names don't become "nvim".
func (c *TmuxCtl) pinWindowName(session, winName string) {
	if winName == "" {
		return
	}
	t := sessionTarget(session, winName)
	_ = c.run("set-option", "-t", t, "automatic-rename", "off")
	_ = c.run("rename-window", "-t", t, winName)
}

// normalizeWindows: empty panes → one shell pane; fill missing cwds from window/session.
func normalizeWindows(wins []PresetWindow, sessCwd string) []PresetWindow {
	if len(wins) == 0 {
		return []PresetWindow{{
			Name:  "",
			Cwd:   sessCwd,
			Panes: []PresetPane{{Cwd: sessCwd}},
		}}
	}
	out := make([]PresetWindow, len(wins))
	for i, w := range wins {
		out[i] = w
		wcwd := w.Cwd
		if wcwd == "" {
			wcwd = sessCwd
		}
		out[i].Cwd = wcwd
		if len(w.Panes) == 0 {
			out[i].Panes = []PresetPane{{Cwd: wcwd}}
			continue
		}
		panes := make([]PresetPane, len(w.Panes))
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

// splitRest: extra panes in same window (horizontal), each with own -c and optional cmd.
func (c *TmuxCtl) splitRest(session, winName string, panes []PresetPane) error {
	if len(panes) == 0 {
		return nil
	}
	target := sessionTarget(session, winName)
	for _, pn := range panes {
		args := []string{"split-window", "-t", target, "-h", "-c", pn.Cwd}
		if pn.Cmd != "" {
			args = append(args, pn.Cmd)
		}
		if err := c.run(args...); err != nil {
			return err
		}
	}
	return nil
}

func sessionTarget(session, winName string) string {
	if winName != "" {
		return session + ":" + winName
	}
	return session + ":0"
}

func (c *TmuxCtl) applyLayout(session string, w PresetWindow) {
	if w.Layout == "" || len(w.Panes) <= 1 {
		return
	}
	target := sessionTarget(session, w.Name)
	// named layouts or dumped window_layout both work with select-layout
	_ = c.run("select-layout", "-t", target, w.Layout)
}

// Connect attaches or switches to session. Creates empty session if missing.
func (c *TmuxCtl) Connect(name, cwd string) error {
	if !c.Has(name) {
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		if err := c.run("new-session", "-d", "-s", name, "-c", cwd); err != nil {
			return err
		}
	}
	s, err := c.t.GetSessionByName(name)
	if err != nil {
		return err
	}
	if os.Getenv("TMUX") != "" {
		return c.t.SwitchClient(&gotmux.SwitchClientOptions{TargetSession: name})
	}
	return s.Attach()
}

func (c *TmuxCtl) ConnectPreset(p *Preset) error {
	if err := c.Load(p); err != nil {
		return err
	}
	return c.Connect(p.Name, p.Cwd)
}

// --- helpers outside tmux ---

func findProjectRoot(start string) string {
	path := start
	for path != "/" {
		if fileExists(filepath.Join(path, "project.godot")) ||
			dirExists(filepath.Join(path, ".git")) ||
			fileExists(filepath.Join(path, "package.json")) ||
			fileExists(filepath.Join(path, "Cargo.toml")) ||
			fileExists(filepath.Join(path, "go.mod")) {
			return path
		}
		path = filepath.Dir(path)
	}
	return start
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func sessionName(root string) string {
	base := filepath.Base(root)
	base = strings.TrimPrefix(base, ".")
	base = strings.ToLower(base)
	base = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r == ' ' || r == '.':
			return '-'
		default:
			return -1
		}
	}, base)
	return base
}

func zoxideList() []string {
	out, err := exec.Command("zoxide", "query", "-l").Output()
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths
}
